package mysql

import (
	"context"
	"os"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// AUD-2026-028 等价性验证：分页与派生状态筛选下推 SQL 后，输出必须与
// 「全量读取 + Go 侧派生 + 内存筛选/切片」的历史口径完全一致。
// 需要 PM_TEST_DSN 指向已应用迁移的库；未设置时跳过。
// 夹具复用 derived_status_integration_test.go 的 integrationFixture（覆盖派生的每条分支：
// 最滞后取值、报告阶段、全部归档、无服务项回退、补充协议、终止混合）。
func TestProjectPageEquivalenceAgainstRealDatabase(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the pagination equivalence check")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	cleanup := func() {
		db.WithContext(ctx).Exec(`DELETE FROM pm_penetration_work_package WHERE tenant_id = '` + integrationTenant + `'`)
		db.WithContext(ctx).Exec(`DELETE FROM pm_impl_plan WHERE tenant_id = '` + integrationTenant + `'`)
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
	// 页级补查夹具：实施计划与渗透工作包各挂一个服务项，验证分页后只带回本页的关联数据。
	planSeeds := []string{
		`INSERT INTO pm_impl_plan (id, tenant_id, service_item_id, site_plan, updated_at, updated_by) VALUES
		   ('PL-TEST-LAG-001', '` + integrationTenant + `', 'SI-TEST-LAG-001', 'LAG-场地计划', NOW(3), 'audit-fix')`,
		`INSERT INTO pm_penetration_work_package (id, tenant_id, project_id, parent_service_item_id, auth_scope, test_scope, rollback_plan, created_at, updated_at, updated_by) VALUES
		   ('PK-TEST-DONE-001', '` + integrationTenant + `', 'PJ-TEST-DONE', 'SI-TEST-DONE-001', 'DONE-授权范围', 'DONE-测试范围', 'DONE-回滚预案', NOW(3), NOW(3), 'audit-fix')`,
	}
	for _, statement := range planSeeds {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			t.Fatalf("seed plan fixture: %v", err)
		}
	}

	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: integrationTenant, AllowAll: true}

	// 基准：全量读取（不分页），仓储内已按 Go 侧派生函数刷新状态。
	baseline, baselineTotal, err := repository.ListProjects(ctx, filter, "", "", 0, 0)
	if err != nil {
		t.Fatalf("baseline list: %v", err)
	}
	if baselineTotal != len(baseline) || len(baseline) != 6 {
		t.Fatalf("baseline total = %d, rows = %d, want 6", baselineTotal, len(baseline))
	}
	baselineIDs := projectIDList(baseline)
	derivedStatus := map[string]string{}
	for _, project := range baseline {
		derivedStatus[project.ID] = project.Status
	}

	// 1) 派生状态筛选：SQL 过滤结果必须与 Go 侧派生基准逐行一致（含排序与 total）。
	//    夹具中存储状态列大量为「待分配」而派生值各异——若 SQL 误读存储列会立即暴露。
	statuses := domain.ProjectStatusNodes()
	statuses = append(statuses, domain.ProjectStatusTerminated, domain.ProjectStatusSupplementRequired)
	for _, wanted := range statuses {
		filtered, total, err := repository.ListProjects(ctx, filter, "", wanted, 0, 0)
		if err != nil {
			t.Fatalf("list by status %q: %v", wanted, err)
		}
		expected := make([]string, 0, len(baseline))
		for _, id := range baselineIDs {
			if derivedStatus[id] == wanted {
				expected = append(expected, id)
			}
		}
		if total != len(expected) {
			t.Fatalf("status %q: total = %d, want %d", wanted, total, len(expected))
		}
		if !sameIDs(filtered, expected) {
			t.Fatalf("status %q filtered = %v, want %v", wanted, projectIDList(filtered), expected)
		}
	}
	// 不存在的状态值必须返回空集且 total 为 0。
	none, noneTotal, err := repository.ListProjects(ctx, filter, "", "不存在状态", 0, 0)
	if err != nil || noneTotal != 0 || len(none) != 0 {
		t.Fatalf("unknown status = (%v, %d, %v), want empty", none, noneTotal, err)
	}

	// 2) 分页边界：遍历各页拼接必须与基准顺序完全一致，total 逐页一致，
	//    末页截断、超界页返回空。
	pageSizes := []int{1, 2, 4}
	for _, pageSize := range pageSizes {
		for _, wanted := range []string{"", domain.ProjectStatusCompleted} {
			base, baseTotal, err := repository.ListProjects(ctx, filter, "", wanted, 0, 0)
			if err != nil {
				t.Fatalf("paged baseline %q: %v", wanted, err)
			}
			var collected []domain.Project
			for page := 1; ; page++ {
				items, total, err := repository.ListProjects(ctx, filter, "", wanted, page, pageSize)
				if err != nil {
					t.Fatalf("page %d (size %d, status %q): %v", page, pageSize, wanted, err)
				}
				if total != baseTotal {
					t.Fatalf("page %d total = %d, want %d", page, total, baseTotal)
				}
				if len(items) == 0 {
					break
				}
				collected = append(collected, items...)
				if len(collected) > baseTotal {
					t.Fatalf("paged collection exceeded total: %d > %d", len(collected), baseTotal)
				}
				if len(collected) == baseTotal {
					break
				}
			}
			if !sameIDs(collected, projectIDList(base)) {
				t.Fatalf("paged walk (size %d, status %q) = %v, want %v", pageSize, wanted, projectIDList(collected), projectIDList(base))
			}
		}
	}
	// 超界页：空切片 + total 保持。
	overRange, overTotal, err := repository.ListProjects(ctx, filter, "", "", 99, 2)
	if err != nil || overTotal != baselineTotal || len(overRange) != 0 {
		t.Fatalf("over-range page = (%d rows, total %d, err %v), want (0, %d, nil)", len(overRange), overTotal, err, baselineTotal)
	}

	// 3) 关键字过滤下推与全量口径一致：按名称命中单个项目并可分页取出。
	byName, byNameTotal, err := repository.ListProjects(ctx, filter, "报告用例", "", 0, 0)
	if err != nil || byNameTotal != 1 || len(byName) != 1 || byName[0].ID != "PJ-TEST-REPORT" {
		t.Fatalf("keyword list = (%v, %d, %v), want PJ-TEST-REPORT", projectIDList(byName), byNameTotal, err)
	}
	pagedByName, pagedByNameTotal, err := repository.ListProjects(ctx, filter, "报告用例", "", 1, 10)
	if err != nil || pagedByNameTotal != 1 || len(pagedByName) != 1 || pagedByName[0].ID != "PJ-TEST-REPORT" {
		t.Fatalf("keyword paged list = (%v, %d, %v)", projectIDList(pagedByName), pagedByNameTotal, err)
	}

	// 4) 服务项分页：id 升序遍历各页拼接与全量一致；计划/工作包只带回本页数据；
	//    不存在的项目返回空集。
	itemBaseline, itemBaselineTotal, err := repository.ListServiceItems(ctx, filter, "", 0, 0)
	if err != nil || itemBaselineTotal != len(itemBaseline) || len(itemBaseline) != 9 {
		t.Fatalf("service item baseline = (%d rows, total %d, err %v), want 9", len(itemBaseline), itemBaselineTotal, err)
	}
	var itemCollected []domain.ServiceItem
	for page := 1; len(itemCollected) < int(itemBaselineTotal); page++ {
		items, total, err := repository.ListServiceItems(ctx, filter, "", page, 2)
		if err != nil || total != itemBaselineTotal {
			t.Fatalf("service item page %d = (total %d, err %v), want total %d", page, total, err, itemBaselineTotal)
		}
		if len(items) == 0 {
			t.Fatalf("service item walk ended early at page %d", page)
		}
		itemCollected = append(itemCollected, items...)
	}
	for i, item := range itemCollected {
		if item.ID != itemBaseline[i].ID {
			t.Fatalf("service item walk row %d = %s, want %s", i, item.ID, itemBaseline[i].ID)
		}
		if item.ImplementationPlan != nil != (item.ID == "SI-TEST-LAG-001") {
			t.Fatalf("service item %s plan presence = %v", item.ID, item.ImplementationPlan != nil)
		}
		if item.PenetrationWorkPackage != nil != (item.ID == "SI-TEST-DONE-001") {
			t.Fatalf("service item %s package presence = %v", item.ID, item.PenetrationWorkPackage != nil)
		}
	}
	if plan := itemCollected[2].ImplementationPlan; plan == nil || plan.SitePlan != "LAG-场地计划" {
		t.Fatalf("page-scoped impl plan lookup failed: %+v", itemCollected[2].ImplementationPlan)
	}
	if missing, missingTotal, err := repository.ListServiceItems(ctx, filter, "PJ-TEST-NONE", 1, 10); err != nil || missingTotal != 0 || len(missing) != 0 {
		t.Fatalf("unknown project service items = (%d rows, total %d, err %v), want empty", len(missing), missingTotal, err)
	}
}

func projectIDList(projects []domain.Project) []string {
	ids := make([]string, 0, len(projects))
	for _, project := range projects {
		ids = append(ids, project.ID)
	}
	return ids
}

func sameIDs(projects []domain.Project, expected []string) bool {
	ids := projectIDList(projects)
	if len(ids) != len(expected) {
		return false
	}
	for i := range ids {
		if ids[i] != expected[i] {
			return false
		}
	}
	return true
}

// TestProjectDerivedStatusSQLCoversStatusMatrix 用状态全组合验证 SQL 派生过滤与
// Go 侧 domain.DeriveProjectStatus 逐行一致：每个用例是单服务项项目，覆盖全部
// 线性阶段、报告阶段细分、已终止、未知状态与空状态（未知/空必须按最滞后=待拆解确认）。
func TestProjectDerivedStatusSQLCoversStatusMatrix(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the derived-status matrix check")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	const matrixTenant = "PM-TEST-MATRIX"
	cleanup := func() {
		db.WithContext(ctx).Exec(`DELETE FROM pm_service_item WHERE tenant_id = ?`, matrixTenant)
		db.WithContext(ctx).Exec(`DELETE FROM pm_project WHERE tenant_id = ?`, matrixTenant)
	}
	cleanup()
	t.Cleanup(cleanup)

	cases := []struct {
		itemStatus   string
		reportStatus string
		want         string
	}{
		{"待确认", "NONE", domain.ProjectStatusPendingDecomposition},
		{"待复核", "", domain.ProjectStatusPendingDecomposition},
		{"待分配", "NONE", domain.ProjectStatusPendingAllocation},
		{"待实施", "NONE", domain.ProjectStatusPendingExecution},
		{"实施准备中", "NONE", domain.ProjectStatusPreparing},
		{"实施中", "NONE", domain.ProjectStatusInProgress},
		{"异常处理中", "NONE", domain.ProjectStatusException},
		{"现场实施完成", "NONE", domain.ProjectStatusFieldCompleted},
		{"现场实施完成", "COMPILING", domain.ProjectStatusReporting},
		{"现场实施完成", "REVIEWED", domain.ProjectStatusReporting},
		{"现场实施完成", "ISSUED", domain.ProjectStatusReporting},
		{"现场实施完成", "ARCHIVED", domain.ProjectStatusCompleted},
		{"现场实施完成", "archived", domain.ProjectStatusCompleted}, // 大小写不敏感
		{"现场实施完成", "", domain.ProjectStatusFieldCompleted},
		{"已终止", "NONE", domain.ProjectStatusTerminated},
		{"神秘未知状态", "", domain.ProjectStatusPendingDecomposition}, // 未知按最滞后
		{"", "", domain.ProjectStatusPendingDecomposition},
	}
	for index, test := range cases {
		projectID := "PJ-MATRIX-" + string(rune('A'+index))
		itemID := "SI-MATRIX-" + string(rune('A'+index))
		if err := db.WithContext(ctx).Exec(
			`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
			 VALUES (?, ?, '矩阵用例', '客户', ?, 'v1', 'NONE', 1, '待分配', NOW(3), NOW(3))`,
			projectID, matrixTenant, "PM-MATRIX-C-"+projectID).Error; err != nil {
			t.Fatalf("seed project %s: %v", projectID, err)
		}
		if err := db.WithContext(ctx).Exec(
			`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
			 VALUES (?, ?, ?, 'S1', 'r', 'STANDARD', ?, ?, 'UNCHECKED', NOW(3), NOW(3))`,
			itemID, matrixTenant, projectID, test.itemStatus, test.reportStatus).Error; err != nil {
			t.Fatalf("seed item %s: %v", itemID, err)
		}
		// 多服务项取最滞后：实施中 + 待确认 的项目必须与单 待确认 一致（等级 0）。
		if index == 6 { // 异常处理中用例追加一个更早阶段的兄弟项
			if err := db.WithContext(ctx).Exec(
				`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
				 VALUES (?, ?, 'PJ-MATRIX-G', 'S2', 'r', 'STANDARD', '待确认', 'NONE', 'UNCHECKED', NOW(3), NOW(3))`,
				"SI-MATRIX-G2", matrixTenant).Error; err != nil {
				t.Fatalf("seed sibling item: %v", err)
			}
			cases[index].want = domain.ProjectStatusPendingDecomposition
		}
	}
	// 补充协议短路：无服务项 + supplement REQUIRED。
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-MATRIX-SUPP', ?, '补充协议矩阵', '客户', 'PM-MATRIX-C-SUPP', 'v1', 'required', 0, '已完成', NOW(3), NOW(3))`, matrixTenant).Error; err != nil {
		t.Fatalf("seed supplement project: %v", err)
	}

	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: matrixTenant, AllowAll: true}
	all, total, err := repository.ListProjects(ctx, filter, "", "", 0, 0)
	if err != nil || total != len(cases)+1 {
		t.Fatalf("matrix baseline = (%d rows, total %d, err %v), want %d", len(all), total, err, len(cases)+1)
	}
	derived := map[string]string{}
	for _, project := range all {
		derived[project.ID] = project.Status
	}
	// 逐用例断言：SQL 过滤该状态时必须包含（且仅包含）派生值命中的行。
	for index, test := range cases {
		projectID := "PJ-MATRIX-" + string(rune('A'+index))
		if derived[projectID] != test.want {
			t.Fatalf("project %s (%q/%q) derived = %q, want %q", projectID, test.itemStatus, test.reportStatus, derived[projectID], test.want)
		}
		filtered, _, err := repository.ListProjects(ctx, filter, "", test.want, 0, 0)
		if err != nil {
			t.Fatalf("filter %q: %v", test.want, err)
		}
		if !containsID(filtered, projectID) {
			t.Fatalf("status %q filter lost project %s (derived %q): %v", test.want, projectID, derived[projectID], projectIDList(filtered))
		}
		for _, other := range filtered {
			if other.ID != projectID && derived[other.ID] != test.want {
				t.Fatalf("status %q filter included %s with derived %q", test.want, other.ID, derived[other.ID])
			}
		}
	}
	if derived["PJ-MATRIX-SUPP"] != domain.ProjectStatusSupplementRequired {
		t.Fatalf("supplement short-circuit derived = %q, want %q", derived["PJ-MATRIX-SUPP"], domain.ProjectStatusSupplementRequired)
	}
	supplementFiltered, _, err := repository.ListProjects(ctx, filter, "", domain.ProjectStatusSupplementRequired, 0, 0)
	if err != nil || len(supplementFiltered) != 1 || supplementFiltered[0].ID != "PJ-MATRIX-SUPP" {
		t.Fatalf("supplement filter = %v (err %v)", projectIDList(supplementFiltered), err)
	}
	// 大小写不敏感的 REQUIRED：上面夹具用小写 'required'，同样必须命中补充协议分支。
}

func containsID(projects []domain.Project, id string) bool {
	for _, project := range projects {
		if project.ID == id {
			return true
		}
	}
	return false
}
