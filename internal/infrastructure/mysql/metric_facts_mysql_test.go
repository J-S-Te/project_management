package mysql

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/migrations"
	"github.com/oklog/ulid/v2"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// metricMySQLDB 仅从显式私密文件打开回环隔离数据库并执行真实迁移。
// 参数：t 为测试生命周期；只读取PM_METRIC_FACTS_TEST_DSN_FILE。
// 返回值：独立测试库；未配置跳过，非pm_test_回环目标在连接前拒绝。
func metricMySQLDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := os.Getenv("PM_METRIC_FACTS_TEST_DSN_FILE")
	if path == "" {
		t.Skip("PM_METRIC_FACTS_TEST_DSN_FILE required; no business database accessed")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private regular DSN file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read isolated DSN failed")
	}
	dsn := strings.TrimSpace(string(raw))
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid isolated DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || host != "127.0.0.1" || !strings.HasPrefix(cfg.DBName, "pm_test_") || !cfg.ParseTime || cfg.Loc != time.UTC {
		t.Fatal("explicit loopback pm_test_* parseTime UTC database required")
	}
	if err = migration.Run(t.Context(), dsn, migrations.Files); err != nil {
		t.Fatalf("isolated migration failed: %v", err)
	}
	if err = migration.Run(t.Context(), dsn, migrations.Files); err != nil {
		t.Fatalf("isolated migration reapply failed: %v", err)
	}
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated database failed")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	return db
}

// TestMetricFactsMySQLMigrationConcurrencyAndSnapshot 验证真实迁移、资源行锁、首轮决定与RR完整采集。
// 参数：t 为测试上下文，目标仅能来自显式隔离DSN。
// 返回值：无；并发重叠同时成功、真实首轮漏计或快照不完整时失败。
func TestMetricFactsMySQLMigrationConcurrencyAndSnapshot(t *testing.T) {
	db := metricMySQLDB(t)
	ctx := context.Background()
	tenant := ulid.Make().String()
	repo := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: tenant, AllowAll: true}
	now := time.Now().UTC()
	project := projectRecord{ID: ulid.Make().String(), ContractID: ulid.Make().String(), TenantID: tenant, OwnerOrgID: "org", CreatedAt: now, UpdatedAt: now}
	item := serviceItemRecord{ID: ulid.Make().String(), TenantID: tenant, ProjectID: project.ID, ProjectManagerID: "manager", Status: "现场实施完成", ReportStatus: "COMPILING", Version: 1, EngineerIDs: []byte("[]"), EquipmentIDs: []byte("[]"), RequiredCodes: []byte("[]"), CreatedAt: now, UpdatedAt: now}
	otherProject := project
	otherProject.ID = ulid.Make().String()
	otherProject.ContractID = ulid.Make().String()
	other := item
	other.ID = ulid.Make().String()
	other.ProjectID = otherProject.ID
	resource := capabilityRecord{ID: ulid.Make().String(), TenantID: tenant, ResourceType: "PERSON", ResourceID: ulid.Make().String(), UserID: "person", Status: "ACTIVE", IdentityStatus: "ACTIVE", CapabilityCodes: []byte("[]"), UpdatedAt: now}
	rev := reportRevisionRecord{TenantID: tenant, ServiceItemID: item.ID, SubjectType: "SERVICE_ITEM", SubjectID: item.ID, Status: "COMPILING", ValidityStatus: "ACTIVE", PreparedBy: "author", FileID: "file", FileSHA256: "sha", ReviewRequired: true, CreatedAt: now, UpdatedAt: now}
	for _, value := range []any{&project, &otherProject, &item, &other, &resource, &rev} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("isolated fixture: %v", err)
		}
	}
	round, err := repo.MetricReportReview(ctx, filter, item.ID, "manager", domain.ReportReviewInput{SubjectType: "SERVICE_ITEM", ExpectedVersion: 1, IdempotencyKey: "send"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.MetricReportReview(ctx, filter, item.ID, "quality", domain.ReportReviewInput{SubjectType: "SERVICE_ITEM", ExpectedVersion: 2, RoundID: round.ID, Decision: "PASSED", IdempotencyKey: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	cap, err := repo.PublishMetricCapacity(ctx, filter, "resource-admin", domain.ResourceCapacityInput{ResourceType: "PERSON", ResourceID: resource.ResourceID, BusinessDate: "2026-11-01", OrganizationID: "org", OrganizationWitnessID: "person", Intervals: []domain.MetricInterval{{Start: at, End: at.Add(4 * time.Hour)}}, Note: "隔离容量", IdempotencyKey: "capacity"})
	if err != nil {
		t.Fatal(err)
	}
	var uses []domain.ResourceUsage
	for index, id := range []string{item.ID, other.ID} {
		key := []string{"use-1", "use-2"}[index]
		row, err := repo.CreateMetricUsage(ctx, filter, "manager", true, domain.ResourceUsageInput{ResourceType: "PERSON", ResourceID: resource.ResourceID, ServiceItemID: id, BusinessDate: cap.BusinessDate, Start: at, End: at.Add(time.Hour), Note: "隔离实际用量", Evidence: "独立测试证据", IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		uses = append(uses, row)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for index, row := range uses {
		wg.Add(1)
		go func(index int, row domain.ResourceUsage) {
			defer wg.Done()
			_, err := repo.MutateMetricUsage(ctx, filter, "manager", row.ID, "SUBMIT", domain.ResourceUsageDecision{ExpectedVersion: 1, IdempotencyKey: []string{"submit-use-1", "submit-use-2"}[index]})
			results <- err
		}(index, row)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("overlapping concurrent submits succeeded=%d want=1", success)
	}
	var pending metricUsageRecord
	if err = db.Where("tenant_id=? AND status='PENDING'", tenant).Take(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = repo.MutateMetricUsage(ctx, filter, "quality", pending.ID, "DECISION", domain.ResourceUsageDecision{ExpectedVersion: 2, Decision: "CONFIRMED", Note: "独立确认", IdempotencyKey: "confirm-use"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.ReadMetricFacts(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || snapshot.SchemaVersion != 1 || snapshot.TenantID != tenant || snapshot.CollectionStartedAt == "" {
		t.Fatalf("invalid snapshot %+v", snapshot)
	}
	passed, minutes := 0, int64(0)
	for _, row := range snapshot.Rows {
		if row.Kind == "report_review" && row.Status == "PASSED" {
			passed++
		}
		if row.Kind == "person_utilization" {
			minutes += row.ActualMinutes
		}
	}
	if passed != 1 || minutes != 60 {
		t.Fatalf("passed=%d actual=%d", passed, minutes)
	}
	exportMetricSourceFixture(t, snapshot)
}

// TestMetricFactsMySQLSamePersonArchivesCanonicalLock 验证不同档案同人容量并发唯一与实际时段共同锁。
// 参数：t 为测试上下文，数据库仅来自原隔离DSN。
// 返回值：无；同日两组织同时成功或跨档案重叠同时成功时失败。
func TestMetricFactsMySQLSamePersonArchivesCanonicalLock(t *testing.T) {
	db := metricMySQLDB(t)
	ctx := context.Background()
	tenant := ulid.Make().String()
	repo := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: tenant, AllowAll: true}
	now := time.Now().UTC()
	project := projectRecord{ID: ulid.Make().String(), ContractID: ulid.Make().String(), TenantID: tenant, OwnerOrgID: "org-a", CreatedAt: now, UpdatedAt: now}
	otherProject := project
	otherProject.ID = ulid.Make().String()
	otherProject.ContractID = ulid.Make().String()
	item := serviceItemRecord{ID: ulid.Make().String(), TenantID: tenant, ProjectID: project.ID, Status: "实施中", Version: 1, EngineerIDs: []byte("[]"), EquipmentIDs: []byte("[]"), RequiredCodes: []byte("[]"), CreatedAt: now, UpdatedAt: now}
	other := item
	other.ID = ulid.Make().String()
	other.ProjectID = otherProject.ID
	first := capabilityRecord{ID: ulid.Make().String(), TenantID: tenant, ResourceType: "PERSON", ResourceID: ulid.Make().String(), UserID: "same-person", Status: "ACTIVE", IdentityStatus: "ACTIVE", CapabilityCodes: []byte("[]"), UpdatedAt: now}
	second := first
	second.ID = ulid.Make().String()
	second.ResourceID = ulid.Make().String()
	for _, value := range []any{&project, &otherProject, &item, &other, &first, &second} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for index, resource := range []capabilityRecord{first, second} {
		wg.Add(1)
		go func(index int, resource capabilityRecord) {
			defer wg.Done()
			_, err := repo.PublishMetricCapacity(ctx, filter, "resource-admin", domain.ResourceCapacityInput{ResourceType: "PERSON", ResourceID: resource.ResourceID, BusinessDate: "2026-11-01", OrganizationID: []string{"org-a", "org-b"}[index], OrganizationWitnessID: "same-person", Intervals: []domain.MetricInterval{{Start: at, End: at.Add(4 * time.Hour)}}, Note: "人员明确容量", IdempotencyKey: []string{"cap-a", "cap-b"}[index]})
			results <- err
		}(index, resource)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("same person capacity organizations succeeded=%d want1", succeeded)
	}
	var uses []domain.ResourceUsage
	for index, resource := range []capabilityRecord{first, second} {
		row, err := repo.CreateMetricUsage(ctx, filter, "manager", true, domain.ResourceUsageInput{ResourceType: "PERSON", ResourceID: resource.ResourceID, ServiceItemID: []string{item.ID, other.ID}[index], BusinessDate: "2026-11-01", Start: at, End: at.Add(time.Hour), Note: "实际工作", Evidence: "隔离测试", IdempotencyKey: []string{"use-a", "use-b"}[index]})
		if err != nil {
			t.Fatal(err)
		}
		uses = append(uses, row)
	}
	results = make(chan error, 2)
	for index, row := range uses {
		wg.Add(1)
		go func(index int, row domain.ResourceUsage) {
			defer wg.Done()
			_, err := repo.MutateMetricUsage(ctx, filter, "manager", row.ID, "SUBMIT", domain.ResourceUsageDecision{ExpectedVersion: 1, IdempotencyKey: []string{"submit-a", "submit-b"}[index]})
			results <- err
		}(index, row)
	}
	wg.Wait()
	close(results)
	succeeded = 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("same person cross-archive overlapping submits succeeded=%d want1", succeeded)
	}
	var pending metricUsageRecord
	if err := db.Where("tenant_id=? AND status='PENDING'", tenant).Take(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MutateMetricUsage(ctx, filter, "quality", pending.ID, "DECISION", domain.ResourceUsageDecision{ExpectedVersion: 2, Decision: "CONFIRMED", Note: "独立确认", IdempotencyKey: "confirm"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.ReadMetricFacts(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range snapshot.Rows {
		if row.Kind == "person_utilization" && row.Period == "2026-11" && row.CapacityMinutes > 0 {
			count++
			if row.CapacityMinutes != 240 || row.ActualMinutes != 60 || row.IdentityID != "same-person" {
				t.Fatalf("canonical snapshot %+v", row)
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate capacity identities count=%d", count)
	}
}

// exportMetricSourceFixture 导出真实隔离合成事实的生产者包络，供跨系统解码回归。
// 参数：t 为测试生命周期，snapshot 为真实只读事务刚返回的最小指标事实。
// 返回值：无；仅显式指定的task-1-metric-fixture.json可写，失败时终止测试。
func exportMetricSourceFixture(t *testing.T, snapshot domain.MetricFactsSnapshot) {
	t.Helper()
	destination := os.Getenv("PM_METRIC_FACTS_FIXTURE_FILE")
	if destination == "" {
		return
	}
	if !filepath.IsAbs(destination) || filepath.Base(destination) != "task-1-metric-fixture.json" {
		t.Fatal("explicit metric fixture destination required")
	}
	body, err := json.MarshalIndent(struct {
		Code string                     `json:"code"`
		Data domain.MetricFactsSnapshot `json:"data"`
	}{"OK", snapshot}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, append(body, '\n'), 0600); err != nil {
		t.Fatal("write synthetic source fixture failed")
	}
}
