package mysql

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/migrations"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// nativeScopeMySQL 仅连接显式指定的全新本机隔离库并执行完整迁移。
// 参数：t 为测试上下文。
// 返回值：真实仓储；未配DSN跳过且不连接，业务库名和默认端口拒绝。
func nativeScopeMySQL(t *testing.T) *Repository {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PM_NATIVE_SCOPE_TEST_DSN"))
	if dsn == "" {
		t.Skip("requires explicit isolated PM_NATIVE_SCOPE_TEST_DSN; no connection attempted")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "pm_native_scope_test_") || cfg.Net != "tcp" || (!strings.HasPrefix(cfg.Addr, "127.0.0.1:") && !strings.HasPrefix(cfg.Addr, "localhost:")) || strings.HasSuffix(cfg.Addr, ":3306") {
		t.Fatal("requires a fresh isolated high-port localhost pm_native_scope_test_ database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal("isolated database unavailable")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var count int64
	if err = db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&count).Error; err != nil || count != 0 {
		t.Fatal("requires entirely empty isolated schema")
	}
	for range 2 {
		if err = migration.Run(context.Background(), dsn, migrations.Files); err != nil {
			t.Fatal(err)
		}
	}
	return NewRepository(db)
}

// TestNativeScopeChangeMySQL 验证真实SQL的发起、冻结、异人审批、并发幂等和正常重确认。
// 参数：t 为测试上下文，仅使用显式全新隔离DSN。
// 返回值：无；目录漂移、自批、审计、版本或旧合同回归失败时标记失败。
func TestNativeScopeChangeMySQL(t *testing.T) {
	repo := nativeScopeMySQL(t)
	ctx := context.Background()
	tenant := "scope-qa"
	filter := platform.ScopeFilter{TenantID: tenant, AllowAll: true}
	category := detectionCategoryRecord{TenantID: tenant, Category: "QA范围检测", Enabled: true, SpecialMethod: "REQUIRED", RequiredCodes: "QA-QUAL"}
	if err := repo.db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	business := platform.Principal{TenantID: tenant, IdentityID: "business-qa", UserID: "business-qa", Roles: []string{"business_admin"}, Permissions: map[string]bool{"project.read": true, "project.create": true, "project.decomposition.manage": true, "service_item.confirm": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION", RoleCode: "business_admin"}}}
	director := platform.Principal{TenantID: tenant, IdentityID: "director-qa", UserID: "director-qa", Roles: []string{"technical_director"}, Permissions: map[string]bool{"project.read": true, "project.native_scope.approve": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION", RoleCode: "technical_director"}}}
	svc := &application.Service{Repo: repo, NativeEnabled: true}
	project, err := svc.CreateNativeProject(ctx, business, domain.Project{SourceKind: "NATIVE", SourceID: "scope-qa-project", Name: "合成范围项目", Customer: "合成委托方", CustomerID: "qa-party"}, []domain.ContractService{{Category: category.Category, Site: "合成现场"}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.ReadNativeScope(ctx, business, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.NativeScopeInput{OperationID: "scope-request", ExpectedProjectVersion: state.ProjectVersion, BaseScopeVersion: state.ScopeVersion, Reason: "合成委托范围修订", ServiceItems: []domain.ContractService{{SourceID: state.ServiceItems[0].SourceServiceID, Category: category.Category, Site: "合成现场", Requirement: "新版范围"}}}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	ids := make(chan string, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := svc.RequestNativeScope(ctx, business, project.ID, input)
			errs <- err
			ids <- value.ID
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	changeID := ""
	for id := range ids {
		if changeID != "" && changeID != id {
			t.Fatal("duplicate proposals")
		}
		changeID = id
	}
	if _, err = svc.ConfirmServiceItems(ctx, business, []string{state.ServiceItems[0].ID}); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("pending did not freeze confirmation: %v", err)
	}
	altered := input
	altered.Reason = "不同载荷"
	if _, err = svc.RequestNativeScope(ctx, business, project.ID, altered); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("same key payload conflict: %v", err)
	}
	decision := domain.NativeScopeDecision{OperationID: "scope-approve", ExpectedVersion: 1, Decision: "APPROVE", Comment: "合成技术核验通过"}
	self := director
	self.IdentityID = business.IdentityID
	if _, err = svc.DecideNativeScope(ctx, self, project.ID, changeID, decision); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("self approval: %v", err)
	}
	if err = repo.db.Model(&category).Update("required_codes", "CHANGED").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DecideNativeScope(ctx, director, project.ID, changeID, decision); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("catalog drift: %v", err)
	}
	if err = repo.db.Model(&category).Updates(map[string]any{"required_codes": "QA-QUAL", "updated_at": category.UpdatedAt}).Error; err != nil {
		t.Fatal(err)
	}
	applied, err := svc.DecideNativeScope(ctx, director, project.ID, changeID, decision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DecideNativeScope(ctx, director, project.ID, changeID, decision); err != nil {
		t.Fatal(err)
	}
	state, err = repo.ReadNativeScope(ctx, filter, project.ID)
	if err != nil || state.ScopeVersion != 2 || state.ProjectVersion != 2 || len(state.ServiceItems) != 1 || state.ServiceItems[0].SourceServiceID != input.ServiceItems[0].SourceID || state.ServiceItems[0].Requirement != "新版范围" {
		t.Fatalf("applied state=%+v err=%v", state, err)
	}
	confirmed, err := svc.ConfirmServiceItems(ctx, business, []string{applied.ServiceItems[0].ID})
	if err != nil || confirmed[0].TechReviewStatus != "PENDING" {
		t.Fatalf("new scope lost original special method confirmation: %v %+v", err, confirmed)
	}
	var counts []struct {
		Action string
		Count  int64
	}
	if err = repo.db.Model(&auditOutboxRecord{}).Select("action,COUNT(*) AS count").Where("tenant_id=?", tenant).Group("action").Scan(&counts).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range counts {
		if row.Count != 1 {
			t.Fatalf("duplicate audit %s=%d", row.Action, row.Count)
		}
	}
	var versions int64
	repo.db.Model(&nativeScopeRecord{}).Where("tenant_id=? AND project_id=?", tenant, project.ID).Count(&versions)
	if versions != 2 {
		t.Fatalf("versions=%d", versions)
	}
}
