package mysql

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// BIZ-PROJECT-04 的仓储侧回归：补充协议分支退出必须以事件账本里的证据为准。
//   - 只有范围变更检测把项目钉进补充协议、没有应用补充协议目录时，确认拆解必须被拒绝，
//     supplement_status 保持 REQUIRED（修复前会被无条件清零）；
//   - 补充协议关账事件可以清零 REQUIRED，并让项目状态回到派生口径。
//
// 需要 PM_TEST_DSN 指向已迁移的隔离测试库；未设置时跳过（不接触任何业务库）。
func TestSupplementExitAgainstRealDatabase(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the supplement-exit integration check")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	cleanup := func() {
		db.WithContext(ctx).Exec(`DELETE FROM pm_delivery_event WHERE tenant_id = '` + integrationTenant + `'`)
		db.WithContext(ctx).Exec(`DELETE FROM pm_service_item WHERE tenant_id = '` + integrationTenant + `'`)
		db.WithContext(ctx).Exec(`DELETE FROM pm_project WHERE tenant_id = '` + integrationTenant + `'`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at) VALUES
	   ('PJ-TEST-CLOSE', '` + integrationTenant + `', '补充协议退出', '客户', 'PM-TEST-C-CLOSE', 'v1', 'REQUIRED', 1, '补充协议处理中', NOW(3), NOW(3))`).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, batch, site, category, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at) VALUES
	   ('SI-TEST-CLOSE-001', '` + integrationTenant + `', 'PJ-TEST-CLOSE', 'S1', 'B1', '现场A', '等保测评', 'r', 'STANDARD', '待确认', 'NONE', 'UNCHECKED', NOW(3), NOW(3))`).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}

	repo := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: integrationTenant, AllowAll: true}

	// ① 没有补充协议目录应用证据：确认拆解必须失败关闭，且不得清除 supplement_status。
	if _, err := repo.ConfirmServiceItems(ctx, filter, []string{"SI-TEST-CLOSE-001"}, "tester"); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("err = %v, want ErrPrecondition（无拆解调整证据不得退出补充协议）", err)
	}
	project, err := repo.GetProject(ctx, filter, "PJ-TEST-CLOSE")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if project.Status != domain.ProjectStatusSupplementRequired {
		t.Fatalf("确认被拒后项目状态 = %q, want %q", project.Status, domain.ProjectStatusSupplementRequired)
	}

	// ② 受控关账事件：清零 REQUIRED，并把状态交回派生口径。
	closeEvent := domain.DeliveryEvent{
		ID: ulid.Make().String(), TenantID: integrationTenant, ProjectID: "PJ-TEST-CLOSE",
		Type: application.EventSupplementClosed, ActorUserID: "tester", CreatedAt: time.Now().UTC(),
		Payload: map[string]any{
			"reason":                 "补充协议已回写",
			"supplement_contract_id": "SC-TEST-CLOSE",
			"scope_snapshot":         []string{"S1|B1|现场A|等保测评"},
		},
	}
	if err := repo.ApplyDeliveryEvent(ctx, closeEvent); err != nil {
		t.Fatalf("apply supplement closure: %v", err)
	}
	var supplement string
	if err := db.WithContext(ctx).Raw(`SELECT supplement_status FROM pm_project WHERE tenant_id = ? AND id = ?`, integrationTenant, "PJ-TEST-CLOSE").
		Scan(&supplement).Error; err != nil {
		t.Fatalf("read supplement_status: %v", err)
	}
	if supplement != "NONE" {
		t.Fatalf("关账后 supplement_status = %q, want NONE", supplement)
	}
	project, err = repo.GetProject(ctx, filter, "PJ-TEST-CLOSE")
	if err != nil {
		t.Fatalf("get project after closure: %v", err)
	}
	if project.Status != domain.ProjectStatusPendingDecomposition {
		t.Fatalf("关账后项目状态 = %q, want %q（应回到派生口径）", project.Status, domain.ProjectStatusPendingDecomposition)
	}

	// ③ 关账后再次确认拆解：正常路径，不再被补充协议校验拦截。
	if _, err := repo.ConfirmServiceItems(ctx, filter, []string{"SI-TEST-CLOSE-001"}, "tester"); err != nil {
		t.Fatalf("关账后确认拆解应放行: %v", err)
	}
	project, err = repo.GetProject(ctx, filter, "PJ-TEST-CLOSE")
	if err != nil {
		t.Fatalf("get project after confirm: %v", err)
	}
	if project.Status != domain.ProjectStatusPendingAllocation {
		t.Fatalf("确认后项目状态 = %q, want %q", project.Status, domain.ProjectStatusPendingAllocation)
	}
}
