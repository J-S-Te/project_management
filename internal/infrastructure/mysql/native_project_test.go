package mysql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// nativeFixture 为原生委托事务创建独立SQLite表与服务范围。
// 参数：t 为测试上下文。
// 返回值：仓储、授权范围、项目和一个待确认服务项。
func nativeFixture(t *testing.T) (*Repository, platform.ScopeFilter, domain.Project, []domain.ServiceItem) {
	t.Helper()
	db := equipmentAtomicityDB(t)
	if err := db.AutoMigrate(&nativeProjectCommandRecord{}, &nativeCounterpartyRecord{}, &nativeScopeRecord{}, &nativeScopeChangeRecord{}, &auditOutboxRecord{}, &penetrationWorkPackageRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := domain.Project{ID: "PJ-NATIVE-1", TenantID: "native-test", OwnerIdentityID: "user-1", OwnerOrgID: "org-1", SourceKind: "NATIVE", SourceID: "request-1", ScopeVersion: 1,
		Name: "本地项目", Customer: "委托单位", CustomerID: "party-1", Status: domain.ProjectStatusPendingDecomposition, Version: 1, CreatedAt: now, UpdatedAt: now}
	items := []domain.ServiceItem{{ID: "SI-NATIVE-1", ProjectID: project.ID, TenantID: project.TenantID, SourceServiceID: "NATIVE-001", Site: "现场", Category: "等保测评", Status: domain.ServiceItemStatusPendingConfirm, TestMode: "STANDARD", RequiredCodes: []string{"QUAL-1"}}}
	return NewRepository(db), platform.ScopeFilter{TenantID: project.TenantID, AllowAll: true}, project, items
}

// TestNativeProjectAtomicReplay 验证同载荷重放不会创建第二项目或范围版本。
// 参数：t 为测试上下文。
// 返回值：无；持久结果不符时标记失败。
func TestNativeProjectAtomicReplay(t *testing.T) {
	repo, filter, project, items := nativeFixture(t)
	created, err := repo.CreateNativeProject(context.Background(), filter, project, items, "digest-1")
	if err != nil {
		t.Fatal(err)
	}
	project.ID = "PJ-NATIVE-2"
	replay, err := repo.CreateNativeProject(context.Background(), filter, project, items, "digest-1")
	if err != nil || replay.ID != created.ID || replay.SourceKind != "NATIVE" || replay.ContractID != "" || replay.ScopeVersion != 1 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	for _, model := range []any{&projectRecord{}, &serviceItemRecord{}, &nativeScopeRecord{}, &nativeCounterpartyRecord{}} {
		var count int64
		if err := repo.db.Model(model).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
	if _, err := repo.CreateNativeProject(context.Background(), filter, project, items, "changed-digest"); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("payload conflict=%v", err)
	}
	filter.AllowAll = false
	filter.OrganizationIDs = []string{"other-org"}
	if _, err := repo.CreateNativeProject(context.Background(), filter, project, items, "digest-1"); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("out of scope replay=%v", err)
	}
}

// TestNativeProjectConflictAndRollback 验证本地单位改名及范围落库失败不会留下半成品。
// 参数：t 为测试上下文。
// 返回值：无；原子性失效时标记失败。
func TestNativeProjectConflictAndRollback(t *testing.T) {
	repo, filter, project, items := nativeFixture(t)
	if _, err := repo.CreateNativeProject(context.Background(), filter, project, items, "digest-1"); err != nil {
		t.Fatal(err)
	}
	project.ID = "PJ-NATIVE-2"
	project.SourceID = "request-2"
	project.Customer = "另一单位"
	if _, err := repo.CreateNativeProject(context.Background(), filter, project, items, "digest-2"); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("party mismatch=%v", err)
	}
	var count int64
	repo.db.Model(&nativeProjectCommandRecord{}).Count(&count)
	if count != 1 {
		t.Fatalf("failed command persisted: %d", count)
	}
	project.Customer = "委托单位"
	project.CustomerID = "party-2"
	if err := repo.db.Migrator().DropTable(&nativeScopeRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateNativeProject(context.Background(), filter, project, items, "digest-2"); err == nil {
		t.Fatal("missing scope table should fail")
	}
	repo.db.Model(&projectRecord{}).Count(&count)
	if count != 1 {
		t.Fatalf("partial project persisted: %d", count)
	}
	repo.db.Model(&nativeCounterpartyRecord{}).Count(&count)
	if count != 1 {
		t.Fatalf("partial party persisted: %d", count)
	}
}

// TestNativeProjectPersistsAuditOnce 验证原生项目和审计意图同事务且重放不重复投递。
// 参数：t 为测试上下文。
// 返回值：无；缺少建项审计时标记失败。
func TestNativeProjectPersistsAuditOnce(t *testing.T) {
	repo, filter, project, items := nativeFixture(t)
	for range 2 {
		if _, err := repo.CreateNativeProject(context.Background(), filter, project, items, "digest"); err != nil {
			t.Fatal(err)
		}
	}
	var rows []auditOutboxRecord
	if err := repo.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ResourceID != project.ID || rows[0].Action != "PROJECT_MANAGEMENT:NATIVE_PROJECT_CREATED" {
		t.Fatalf("audit=%+v", rows)
	}
}

// TestNativeProjectReadModelsKeepSource 验证普通列表、详情和服务项读取保留原生来源且不要求合同。
// 参数：t 为测试上下文。
// 返回值：无；原生项目被误判损坏或丢失来源时标记失败。
func TestNativeProjectReadModelsKeepSource(t *testing.T) {
	repo, filter, project, items := nativeFixture(t)
	if _, err := repo.CreateNativeProject(context.Background(), filter, project, items, "digest"); err != nil {
		t.Fatal(err)
	}
	detail, err := repo.GetProject(context.Background(), filter, project.ID)
	if err != nil || detail.SourceKind != "NATIVE" || detail.SourceID != project.SourceID || detail.ContractID != "" {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	list, total, err := repo.ListProjects(context.Background(), filter, domain.ProjectListQuery{Keyword: "", Status: "", Page: 1, PageSize: 10})
	if err != nil || total != 1 || len(list) != 1 || list[0].ScopeVersion != 1 {
		t.Fatalf("list=%+v total=%d err=%v", list, total, err)
	}
	services, total, err := repo.ListServiceItems(context.Background(), filter, project.ID, 1, 10)
	if err != nil || total != 1 || len(services) != 1 || services[0].Status != domain.ServiceItemStatusPendingConfirm {
		t.Fatalf("services=%+v total=%d err=%v", services, total, err)
	}
}

// TestNativeProjectApplicationConfirmsWithoutContract 验证原生应用建项后可以完成真实事务拆解确认。
// 参数：t 为测试上下文。
// 返回值：无；合同缺失阻断或特殊方法门禁丢失时标记失败。
func TestNativeProjectApplicationConfirmsWithoutContract(t *testing.T) {
	repo, filter, project, _ := nativeFixture(t)
	if err := repo.db.AutoMigrate(&detectionCategoryRecord{}, &deliveryEventRecord{}); err != nil {
		t.Fatal(err)
	}
	entry := detectionCategoryRecord{TenantID: filter.TenantID, Category: "受控特殊类别", Enabled: true, SpecialMethod: domain.SpecialMethodRequired, RequiredCodes: "QUAL-1"}
	if err := repo.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	service := &application.Service{Repo: repo, NativeEnabled: true}
	user := platform.Principal{TenantID: filter.TenantID, IdentityID: "user-1", UserID: "user-1", Roles: []string{"business_admin"}, Permissions: map[string]bool{"project.create": true, "service_item.confirm": true}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}}}
	created, err := service.CreateNativeProject(context.Background(), user, project, []domain.ContractService{{Category: entry.Category, Site: "现场", TestMode: "STANDARD"}})
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := repo.ListServiceItems(context.Background(), filter, created.ID, 1, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	confirmed, err := service.ConfirmServiceItems(context.Background(), user, []string{items[0].ID})
	if err != nil || len(confirmed) != 1 {
		t.Fatalf("confirm=%+v err=%v", confirmed, err)
	}
	if confirmed[0].Status != "待分配" || confirmed[0].TechReviewStatus != "PENDING" || confirmed[0].TestMode != "PENETRATION" {
		t.Fatalf("special gate lost: %+v", confirmed[0])
	}
}
