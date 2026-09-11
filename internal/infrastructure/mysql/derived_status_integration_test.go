package mysql

import (
	"context"
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

// integrationTenant 隔离集成检查写入的行，避免影响真实业务数据。
const integrationTenant = "PM-TEST-TENANT"

// integrationFixture 覆盖派生的每条分支：最滞后取值、报告阶段、全部归档、无服务项回退、补充协议。
var integrationFixture = []string{
	`DELETE FROM pm_service_item WHERE tenant_id = 'PM-TEST-TENANT'`,
	`DELETE FROM pm_project WHERE tenant_id = 'PM-TEST-TENANT'`,
	`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at) VALUES
	   ('PJ-TEST-LAG',        'PM-TEST-TENANT', '滞后用例', '客户', 'PM-TEST-C-LAG',    'v1', 'NONE',     2, '待分配',        NOW(3), NOW(3)),
	   ('PJ-TEST-REPORT',     'PM-TEST-TENANT', '报告用例', '客户', 'PM-TEST-C-REPORT', 'v1', 'NONE',     2, '待分配',        NOW(3), NOW(3)),
	   ('PJ-TEST-DONE',       'PM-TEST-TENANT', '完成用例', '客户', 'PM-TEST-C-DONE',   'v1', 'NONE',     2, '待分配',        NOW(3), NOW(3)),
	   ('PJ-TEST-NOITEM',     'PM-TEST-TENANT', '无服务项', '客户', 'PM-TEST-C-NOITEM', 'v1', 'NONE',     0, '待拆解确认',    NOW(3), NOW(3)),
	   ('PJ-TEST-SUPPLEMENT', 'PM-TEST-TENANT', '补充协议', '客户', 'PM-TEST-C-SUPP',   'v1', 'REQUIRED', 1, '补充协议处理中', NOW(3), NOW(3)),
	   ('PJ-TEST-MIXED',      'PM-TEST-TENANT', '终止混合', '客户', 'PM-TEST-C-MIXED',  'v1', 'NONE',     2, '待实施',        NOW(3), NOW(3))`,
	`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at) VALUES
	   ('SI-TEST-LAG-001',    'PM-TEST-TENANT', 'PJ-TEST-LAG',        'S1', 'r', 'STANDARD', '待分配',       'NONE',      'UNCHECKED', NOW(3), NOW(3)),
	   ('SI-TEST-LAG-002',    'PM-TEST-TENANT', 'PJ-TEST-LAG',        'S2', 'r', 'STANDARD', '实施中',       'NONE',      'PASSED',    NOW(3), NOW(3)),
	   ('SI-TEST-REPORT-001', 'PM-TEST-TENANT', 'PJ-TEST-REPORT',     'S1', 'r', 'STANDARD', '现场实施完成', 'COMPILING', 'PASSED',    NOW(3), NOW(3)),
	   ('SI-TEST-REPORT-002', 'PM-TEST-TENANT', 'PJ-TEST-REPORT',     'S2', 'r', 'STANDARD', '现场实施完成', 'ISSUED',    'PASSED',    NOW(3), NOW(3)),
	   ('SI-TEST-DONE-001',   'PM-TEST-TENANT', 'PJ-TEST-DONE',       'S1', 'r', 'STANDARD', '现场实施完成', 'ARCHIVED',  'PASSED',    NOW(3), NOW(3)),
	   ('SI-TEST-DONE-002',   'PM-TEST-TENANT', 'PJ-TEST-DONE',       'S2', 'r', 'STANDARD', '现场实施完成', 'ARCHIVED',  'PASSED',    NOW(3), NOW(3)),
	   ('SI-TEST-SUPP-001',   'PM-TEST-TENANT', 'PJ-TEST-SUPPLEMENT', 'S1', 'r', 'STANDARD', '实施中',       'NONE',      'PASSED',    NOW(3), NOW(3)),
	   ('SI-TEST-MIX-001',    'PM-TEST-TENANT', 'PJ-TEST-MIXED',      'S1', 'r', 'STANDARD', '已终止',       'NONE',      'PASSED',    NOW(3), NOW(3)),
	   ('SI-TEST-MIX-002',    'PM-TEST-TENANT', 'PJ-TEST-MIXED',      'S2', 'r', 'STANDARD', '待实施',       'NONE',      'PASSED',    NOW(3), NOW(3))`,
}

// TestDerivedProjectStatusAgainstRealDatabase 用真实 MySQL 校验「项目唯一派生状态」的读取路径：
// 列表、详情、状态筛选与仪表盘必须给出同一个派生值，而不是各自的拼装结果。
// 需要 PM_TEST_DSN 指向已迁移到 000008 的库；未设置时跳过。测试自行写入并清理隔离租户的数据。
func TestDerivedProjectStatusAgainstRealDatabase(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the derived-status integration check")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	cleanup := func() {
		db.WithContext(ctx).Exec(`DELETE FROM pm_service_item WHERE tenant_id = '` + integrationTenant + `'`)
		db.WithContext(ctx).Exec(`DELETE FROM pm_project WHERE tenant_id = '` + integrationTenant + `'`)
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, statement := range integrationFixture {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}

	repo := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: integrationTenant, AllowAll: true}

	projects, err := repo.ListProjects(ctx, filter, "", "")
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	got := map[string]string{}
	progress := map[string]int{}
	for _, project := range projects {
		got[project.ID] = project.Status
		progress[project.ID] = project.Progress
	}
	for id, expected := range map[string]string{
		"PJ-TEST-LAG":        "待分配",
		"PJ-TEST-REPORT":     "报告编制",
		"PJ-TEST-DONE":       "已完成",
		"PJ-TEST-NOITEM":     "待拆解确认",
		"PJ-TEST-SUPPLEMENT": "补充协议处理中",
		"PJ-TEST-MIXED":      "待实施",
	} {
		if got[id] != expected {
			t.Errorf("%s derived status = %q, want %q", id, got[id], expected)
		}
	}

	// 进度必须与状态同源：fixture 的存储 progress 全是 0，读路径必须重新派生。
	for id, expected := range map[string]int{
		"PJ-TEST-LAG":        31, // 待分配(1)+实施中(4) → 5*100/(8*2)
		"PJ-TEST-REPORT":     87, // 现场完成+报告编制中(7)×2 → 14*100/16
		"PJ-TEST-DONE":       100,
		"PJ-TEST-NOITEM":     0,
		"PJ-TEST-SUPPLEMENT": 50, // 实施中(4)
		"PJ-TEST-MIXED":      25, // 已终止项不计入分子分母，仅待实施(2) → 2*100/8（旧口径为 62）
	} {
		if progress[id] != expected {
			t.Errorf("%s derived progress = %d, want %d", id, progress[id], expected)
		}
	}

	// 状态筛选必须作用于派生值：这些行的存储列都是 待分配，按 已完成 过滤应只命中归档项目。
	completed, err := repo.ListProjects(ctx, filter, "", "已完成")
	if err != nil {
		t.Fatalf("list by derived status: %v", err)
	}
	if len(completed) != 1 || completed[0].ID != "PJ-TEST-DONE" {
		t.Fatalf("filtered by derived status = %#v", completed)
	}

	// 详情读取必须与列表给出同一个状态。
	detail, err := repo.GetProject(ctx, filter, "PJ-TEST-REPORT")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if detail.Status != "报告编制" {
		t.Fatalf("detail status = %q, want 报告编制", detail.Status)
	}

	// 仪表盘口径必须与列表一致。
	dashboard, err := repo.Dashboard(ctx, filter)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	for status, expected := range map[string]int{"待分配": 1, "报告编制": 1, "已完成": 1, "待拆解确认": 1, "补充协议处理中": 1, "待实施": 1} {
		if dashboard.StatusCounts[status] != expected {
			t.Fatalf("dashboard status_counts[%s] = %d, want %d (all=%#v)", status, dashboard.StatusCounts[status], expected, dashboard.StatusCounts)
		}
	}
	if dashboard.InFlightProjects != 5 {
		t.Fatalf("in_flight_projects = %d, want 5 (only 已完成 is settled)", dashboard.InFlightProjects)
	}
}

// TestDecompositionAdjustmentAndSupplementResetAgainstRealDatabase 在真实库上验证两条 P0 修复：
// 拆解调整的服务项必须由仓储补齐主键；拆解确认完成后项目必须退出补充协议分支。
func TestDecompositionAdjustmentAndSupplementResetAgainstRealDatabase(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the decomposition-adjustment integration check")
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
	   ('PJ-TEST-ADJUST', '` + integrationTenant + `', '调整用例', '客户', 'PM-TEST-C-ADJUST', 'v1', 'NONE', 1, '待拆解确认', NOW(3), NOW(3))`).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at) VALUES
	   ('SI-TEST-ADJUST-001', '` + integrationTenant + `', 'PJ-TEST-ADJUST', 'S1', 'r', 'STANDARD', '待确认', 'NONE', 'UNCHECKED', NOW(3), NOW(3))`).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}

	repo := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: integrationTenant, AllowAll: true}
	// 应用层构造的调整清单不带 id（编号由仓储顺延），payload 与 application 层保持一致。
	event := domain.DeliveryEvent{
		ID: ulid.Make().String(), TenantID: integrationTenant, ProjectID: "PJ-TEST-ADJUST",
		Type: application.EventDecompositionAdjusted, ActorUserID: "tester", CreatedAt: time.Now().UTC(),
		Payload: map[string]any{
			"reason":                 "客户追加系统",
			"supplement_contract_id": "SC-TEST-001",
			"service_items": []map[string]any{
				{"source_service_id": "S1", "batch": "B1", "site": "现场A", "category": "等保", "test_mode": "STANDARD", "status": "待确认"},
				{"source_service_id": "S2", "batch": "B1", "site": "现场B", "category": "渗透", "test_mode": "STANDARD", "status": "待确认"},
			},
		},
	}
	if err := repo.ApplyDeliveryEvent(ctx, event); err != nil {
		t.Fatalf("apply decomposition adjustment: %v", err)
	}

	// P0-1：主键必须补齐且互不重复，否则两行冲突整单回滚、单行落成 id='' 的孤儿行。
	items, err := repo.ListServiceItems(ctx, filter, "PJ-TEST-ADJUST")
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("adjusted items = %d, want 2", len(items))
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.ID == "" {
			t.Fatal("拆解调整产生了空主键服务项：事件分发按 service_item_id != \"\" 判定，该服务项之后永远走不动")
		}
		if seen[item.ID] {
			t.Fatalf("服务项主键重复：%s", item.ID)
		}
		seen[item.ID] = true
	}

	// P0-2：确认前项目处于补充协议分支。
	project, err := repo.GetProject(ctx, filter, "PJ-TEST-ADJUST")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if project.Status != domain.ProjectStatusSupplementRequired {
		t.Fatalf("调整后项目状态 = %q, want %q", project.Status, domain.ProjectStatusSupplementRequired)
	}

	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if _, err := repo.ConfirmServiceItems(ctx, filter, ids, "tester"); err != nil {
		t.Fatalf("confirm adjusted items: %v", err)
	}
	project, err = repo.GetProject(ctx, filter, "PJ-TEST-ADJUST")
	if err != nil {
		t.Fatalf("get project after confirm: %v", err)
	}
	if project.Status != domain.ProjectStatusPendingAllocation {
		t.Fatalf("确认后项目状态 = %q, want %q（supplement_status 不回写会让项目永久钉在补充协议分支）", project.Status, domain.ProjectStatusPendingAllocation)
	}
}
