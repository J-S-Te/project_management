package mysql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// reportStageTenant 隔离报告阶段集成检查的数据，不影响其他夹具租户。
const reportStageTenant = "PM-TEST-REPORT-STAGE"

// reportStageFixture 覆盖项目级报告阶段的每条分支：
//   - PJ-RS-MIX：归档项 + 整改中项 → RECTIFYING（取最滞后阶段）
//   - PJ-RS-NONE：全部待整改 → NONE
//   - PJ-RS-ARCHIVED-ITEM：一个已归档服务项仍是 NONE + 一个整改中项 →
//     必须排除归档项才得到 RECTIFYING（归档项会污染派生，这是既有缺陷）
//   - PJ-RS-PARTIAL：有服务项未到现场完成 → 不进入报告段
//   - PJ-RS-DONE：全部归档 → ARCHIVED
//   - PJ-RS-NOITEM：无服务项 → 不进入报告段
//   - PJ-RS-UNKNOWN：未知报告码 → 不进入报告段
//   - PJ-RS-RECTIFY-2 / PJ-RS-RECTIFY-3：与 MIX/ARCHIVED-ITEM 同阶段，用于分页等价性
//
// 存储状态列统一写「待分配」，确保派生值全部由服务项计算（误读存储列会立即暴露）。
var reportStageFixture = []string{
	`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at) VALUES
	   ('PJ-RS-MIX',           'PM-TEST-REPORT-STAGE', '报告阶段混合', '客户', 'C-RS-MIX',   'v1', 'NONE', 2, '待分配', NOW(3), NOW(3)),
	   ('PJ-RS-NONE',          'PM-TEST-REPORT-STAGE', '报告阶段待整改', '客户', 'C-RS-NONE',  'v1', 'NONE', 1, '待分配', NOW(3), NOW(3)),
	   ('PJ-RS-ARCHIVED-ITEM', 'PM-TEST-REPORT-STAGE', '归档项污染', '客户', 'C-RS-ARCH',  'v1', 'NONE', 2, '待分配', NOW(3), NOW(3)),
	   ('PJ-RS-PARTIAL',       'PM-TEST-REPORT-STAGE', '未到报告段', '客户', 'C-RS-PART',  'v1', 'NONE', 2, '待分配', NOW(3), NOW(3)),
	   ('PJ-RS-DONE',          'PM-TEST-REPORT-STAGE', '已完成', '客户', 'C-RS-DONE',  'v1', 'NONE', 1, '待分配', NOW(3), NOW(3)),
	   ('PJ-RS-NOITEM',        'PM-TEST-REPORT-STAGE', '无服务项', '客户', 'C-RS-NOITEM', 'v1', 'NONE', 0, '待拆解确认', NOW(3), NOW(3)),
	   ('PJ-RS-UNKNOWN',       'PM-TEST-REPORT-STAGE', '未知报告码', '客户', 'C-RS-UNK',   'v1', 'NONE', 1, '待分配', NOW(3), NOW(3)),
	   ('PJ-RS-RECTIFY-2',     'PM-TEST-REPORT-STAGE', '报告阶段整改二', '客户', 'C-RS-R2',    'v1', 'NONE', 1, '待分配', NOW(3), NOW(3)),
	   ('PJ-RS-RECTIFY-3',     'PM-TEST-REPORT-STAGE', '报告阶段整改三', '客户', 'C-RS-R3',    'v1', 'NONE', 1, '待分配', NOW(3), NOW(3))`,
	`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at) VALUES
	   ('SI-RS-MIX-1',  'PM-TEST-REPORT-STAGE', 'PJ-RS-MIX',           'S1', 'r', 'STANDARD', '现场实施完成', 'ARCHIVED',   'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-MIX-2',  'PM-TEST-REPORT-STAGE', 'PJ-RS-MIX',           'S2', 'r', 'STANDARD', '现场实施完成', 'RECTIFYING', 'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-NONE-1', 'PM-TEST-REPORT-STAGE', 'PJ-RS-NONE',          'S1', 'r', 'STANDARD', '现场实施完成', 'NONE',       'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-ARCH-1', 'PM-TEST-REPORT-STAGE', 'PJ-RS-ARCHIVED-ITEM', 'S1', 'r', 'STANDARD', '现场实施完成', 'NONE',       'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-ARCH-2', 'PM-TEST-REPORT-STAGE', 'PJ-RS-ARCHIVED-ITEM', 'S2', 'r', 'STANDARD', '现场实施完成', 'RECTIFYING', 'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-PART-1', 'PM-TEST-REPORT-STAGE', 'PJ-RS-PARTIAL',       'S1', 'r', 'STANDARD', '实施中',       'NONE',       'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-PART-2', 'PM-TEST-REPORT-STAGE', 'PJ-RS-PARTIAL',       'S2', 'r', 'STANDARD', '现场实施完成', 'RECTIFYING', 'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-DONE-1', 'PM-TEST-REPORT-STAGE', 'PJ-RS-DONE',          'S1', 'r', 'STANDARD', '现场实施完成', 'ARCHIVED',   'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-UNK-1',  'PM-TEST-REPORT-STAGE', 'PJ-RS-UNKNOWN',       'S1', 'r', 'STANDARD', '现场实施完成', 'DRAFTING',   'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-R2-1',   'PM-TEST-REPORT-STAGE', 'PJ-RS-RECTIFY-2',     'S1', 'r', 'STANDARD', '现场实施完成', 'RECTIFYING', 'PASSED', NOW(3), NOW(3)),
	   ('SI-RS-R3-1',   'PM-TEST-REPORT-STAGE', 'PJ-RS-RECTIFY-3',     'S1', 'r', 'STANDARD', '现场实施完成', 'RECTIFYING', 'PASSED', NOW(3), NOW(3))`,
	// 归档服务项：archived_at 非空后不得参与项目派生（与 Go 侧 GORM 软删除一致）。
	`UPDATE pm_service_item SET archived_at = NOW(3) WHERE id = 'SI-RS-ARCH-1'`,
}

// reportStageFixtureCleanup 清理隔离租户的项目、服务项与 SLA 规则。
func reportStageFixtureCleanup(db *gorm.DB, ctx context.Context) {
	db.WithContext(ctx).Exec(`DELETE FROM pm_sla WHERE tenant_id = '` + reportStageTenant + `'`)
	db.WithContext(ctx).Exec(`DELETE FROM pm_service_item WHERE tenant_id = '` + reportStageTenant + `'`)
	db.WithContext(ctx).Exec(`DELETE FROM pm_project WHERE tenant_id = '` + reportStageTenant + `'`)
}

// openReportStageIntegration 打开真实 MySQL 并写入报告阶段夹具；缺 DSN 时跳过。
func openReportStageIntegration(t *testing.T) (*gorm.DB, *Repository, platform.ScopeFilter) {
	t.Helper()
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the report-stage integration checks")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	reportStageFixtureCleanup(db, ctx)
	t.Cleanup(func() { reportStageFixtureCleanup(db, ctx) })
	for _, statement := range reportStageFixture {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			t.Fatalf("seed report-stage fixture: %v", err)
		}
	}
	return db, NewRepository(db), platform.ScopeFilter{TenantID: reportStageTenant, AllowAll: true}
}

// TestProjectReportStageFilterAgainstRealDatabase 验证报告阶段筛选与分页在真实库上的语义：
// 行集、total 与排序必须与 Go 侧派生基准一致，归档服务项不得污染派生，
// 并且可与派生状态筛选 AND 组合。
func TestProjectReportStageFilterAgainstRealDatabase(t *testing.T) {
	ctx := context.Background()
	_, repository, filter := openReportStageIntegration(t)

	baseline, baselineTotal, err := repository.ListProjects(ctx, filter, domain.ProjectListQuery{})
	if err != nil {
		t.Fatalf("baseline list: %v", err)
	}
	if baselineTotal != len(baseline) || len(baseline) != 9 {
		t.Fatalf("baseline total=%d rows=%d，期望9", baselineTotal, len(baseline))
	}
	byStage := map[string][]string{}
	order := make([]string, 0, len(baseline))
	for _, project := range baseline {
		byStage[project.ReportStage] = append(byStage[project.ReportStage], project.ID)
		order = append(order, project.ID)
	}
	if len(byStage[""]) != 3 {
		t.Fatalf("不可判定报告阶段的项目=%v，期望3个（未到报告段/无服务项/未知码）", byStage[""])
	}
	// 取最滞后阶段：归档 + 整改中 → 整改中；归档项被排除后归档项项目也应是整改中。
	if got := byStage[domain.ReportPhaseRectifying]; len(got) != 4 {
		t.Fatalf("整改中项目=%v，期望4个（MIX/ARCHIVED-ITEM/RECTIFY-2/RECTIFY-3）", got)
	}
	if got := byStage[domain.ReportPhaseNone]; len(got) != 1 || got[0] != "PJ-RS-NONE" {
		t.Fatalf("待整改项目=%v，期望 [PJ-RS-NONE]", got)
	}
	if got := byStage[domain.ReportPhaseArchived]; len(got) != 1 || got[0] != "PJ-RS-DONE" {
		t.Fatalf("已归档项目=%v，期望 [PJ-RS-DONE]", got)
	}

	// 每个报告阶段：SQL 过滤结果必须与 Go 派生基准逐行一致（id DESC 排序 + total）。
	for _, stage := range []string{domain.ReportPhaseNone, domain.ReportPhaseRectifying, domain.ReportPhaseCompiling, domain.ReportPhaseReviewed, domain.ReportPhaseIssued, domain.ReportPhaseArchived} {
		filtered, total, err := repository.ListProjects(ctx, filter, domain.ProjectListQuery{ReportStage: stage})
		if err != nil {
			t.Fatalf("list by report stage %q: %v", stage, err)
		}
		expected := byStage[stage]
		if total != len(expected) {
			t.Fatalf("report stage %q: total=%d，期望%d", stage, total, len(expected))
		}
		if !sameIDs(filtered, expected) {
			t.Fatalf("report stage %q filtered=%v，期望%v", stage, projectIDList(filtered), expected)
		}
	}
	// 未知码与"未进入报告段"不能被任何阶段筛选命中。
	for _, stage := range []string{domain.ReportPhaseNone, domain.ReportPhaseRectifying, domain.ReportPhaseArchived} {
		filtered, _, err := repository.ListProjects(ctx, filter, domain.ProjectListQuery{ReportStage: stage})
		if err != nil {
			t.Fatal(err)
		}
		for _, project := range filtered {
			if project.ReportStage != stage {
				t.Fatalf("filter %q 返回了 %q 的项目 %s", stage, project.ReportStage, project.ID)
			}
		}
	}

	// 分页等价性：整改中阶段按 1/2 条分页遍历必须与全量顺序一致，total 恒定。
	for _, pageSize := range []int{1, 2} {
		var collected []domain.Project
		for page := 1; ; page++ {
			items, total, err := repository.ListProjects(ctx, filter, domain.ProjectListQuery{ReportStage: domain.ReportPhaseRectifying, Page: page, PageSize: pageSize})
			if err != nil {
				t.Fatalf("page %d (size %d): %v", page, pageSize, err)
			}
			if total != len(byStage[domain.ReportPhaseRectifying]) {
				t.Fatalf("page %d total=%d，期望%d", page, total, len(byStage[domain.ReportPhaseRectifying]))
			}
			if len(items) == 0 {
				break
			}
			collected = append(collected, items...)
			if len(collected) >= total {
				break
			}
		}
		if !sameIDs(collected, byStage[domain.ReportPhaseRectifying]) {
			t.Fatalf("paged walk (size %d)=%v，期望%v", pageSize, projectIDList(collected), byStage[domain.ReportPhaseRectifying])
		}
	}

	// 双筛选组合：报告编制 + 整改中 只应命中 MIX/ARCHIVED-ITEM/RECTIFY-2/3。
	combined, combinedTotal, err := repository.ListProjects(ctx, filter, domain.ProjectListQuery{
		Status: domain.ProjectStatusReporting, ReportStage: domain.ReportPhaseRectifying,
	})
	if err != nil {
		t.Fatalf("combined filter: %v", err)
	}
	expectedCombined := make([]string, 0, 4)
	for _, id := range order {
		for _, project := range baseline {
			if project.ID == id && project.Status == domain.ProjectStatusReporting && project.ReportStage == domain.ReportPhaseRectifying {
				expectedCombined = append(expectedCombined, id)
			}
		}
	}
	if combinedTotal != len(expectedCombined) || !sameIDs(combined, expectedCombined) {
		t.Fatalf("combined=(%v, %d)，期望 (%v, %d)", projectIDList(combined), combinedTotal, expectedCombined, len(expectedCombined))
	}
	// 报告编制 + 待整改 不应命中任何项目：NONE 项的项目状态仍是现场实施完成。
	if items, total, err := repository.ListProjects(ctx, filter, domain.ProjectListQuery{Status: domain.ProjectStatusReporting, ReportStage: domain.ReportPhaseNone}); err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("不存在的组合=(%v, %d, %v)，期望空集", projectIDList(items), total, err)
	}
}

// TestMonitoringCountsMatchDerivedRows 验证监控快照的 status_counts / report_stage_counts
// 与同一筛选集的行派生值一致（不得退化为按存储列 pm_project.status 分组），
// 且 report_stage 筛选同样作用到计数。
func TestMonitoringCountsMatchDerivedRows(t *testing.T) {
	ctx := context.Background()
	_, repository, filter := openReportStageIntegration(t)

	for _, query := range []domain.ProjectMonitoringQuery{
		{Page: 1, PageSize: 100},
		{Page: 1, PageSize: 100, ReportStage: domain.ReportPhaseRectifying},
		{Page: 1, PageSize: 100, Status: "待分配"},
	} {
		data, err := repository.LoadProjectMonitoringPage(ctx, filter, query)
		if err != nil {
			t.Fatalf("load monitoring page %+v: %v", query, err)
		}
		if data.Total != len(data.Projects) {
			t.Fatalf("%+v: total=%d 行=%d（小夹具下应一致）", query, data.Total, len(data.Projects))
		}
		wantStatus := map[string]int{}
		wantReport := map[string]int{}
		for _, project := range data.Projects {
			wantStatus[project.Status]++
			if project.ReportStage != "" {
				wantReport[project.ReportStage]++
			}
		}
		if !sameCounts(data.StatusCounts, wantStatus) {
			t.Fatalf("%+v: status_counts=%v，期望派生行计数 %v", query, data.StatusCounts, wantStatus)
		}
		if !sameCounts(data.ReportStageCounts, wantReport) {
			t.Fatalf("%+v: report_stage_counts=%v，期望派生行计数 %v", query, data.ReportStageCounts, wantReport)
		}
		if query.ReportStage != "" {
			for _, project := range data.Projects {
				if project.ReportStage != query.ReportStage {
					t.Fatalf("%+v: 行 %s report_stage=%q", query, project.ID, project.ReportStage)
				}
			}
		}
	}
}

// TestMonitoringReportStageSLASelection 验证报告阶段停留能被 sla/risk 筛选命中，
// 并且 ListSlaOverdue 带回报告状态与报告计时基准。
// 参数：t 为测试上下文。
// 返回值：无；报告阶段 SLA 未进入筛选或计时列缺失时失败。
func TestMonitoringReportStageSLASelection(t *testing.T) {
	ctx := context.Background()
	db, repository, filter := openReportStageIntegration(t)

	// 整改中已停留 3 小时，规则时限 1 小时 → 超期；同时给一个现场状态规则确保两维度并存。
	for _, statement := range []string{
		`UPDATE pm_service_item SET report_updated_at = DATE_SUB(UTC_TIMESTAMP(), INTERVAL 3 HOUR) WHERE id = 'SI-RS-MIX-2'`,
		`INSERT INTO pm_sla (tenant_id, kind, name, ` + "`status`" + `, deadline_hours, remind_hours, enabled, created_at, updated_at, updated_by) VALUES
		   ('PM-TEST-REPORT-STAGE', 'sla', '整改停留', 'RECTIFYING', 1, 0, TRUE, NOW(3), NOW(3), 'tester')`,
	} {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			t.Fatalf("seed sla fixture: %v", err)
		}
	}

	candidates, err := repository.ListSlaOverdue(ctx, filter)
	if err != nil {
		t.Fatalf("list sla overdue: %v", err)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.ID != "SI-RS-MIX-2" {
			continue
		}
		if candidate.ReportStatus != domain.ReportPhaseRectifying {
			t.Fatalf("SLA 候选报告状态=%q，期望 RECTIFYING", candidate.ReportStatus)
		}
		if candidate.ReportChangedAt.IsZero() || time.Since(candidate.ReportChangedAt) < 2*time.Hour {
			t.Fatalf("SLA 候选缺少报告计时基准: %+v", candidate.ReportChangedAt)
		}
		found = true
	}
	if !found {
		t.Fatal("ListSlaOverdue 未返回整改中的服务项")
	}

	data, err := repository.LoadProjectMonitoringPage(ctx, filter, domain.ProjectMonitoringQuery{Page: 1, PageSize: 100, SLAOnly: true})
	if err != nil {
		t.Fatalf("monitoring sla filter: %v", err)
	}
	if !containsID(data.Projects, "PJ-RS-MIX") {
		t.Fatalf("sla 筛选未命中报告阶段超期项目，实际 %v", projectIDList(data.Projects))
	}
	for _, candidate := range data.SLACandidates {
		if candidate.ID == "SI-RS-MIX-2" && candidate.ReportChangedAt.IsZero() {
			t.Fatal("监控 SLA 候选缺少 report_updated_at 计时基准")
		}
	}

	risky, err := repository.LoadProjectMonitoringPage(ctx, filter, domain.ProjectMonitoringQuery{Page: 1, PageSize: 100, RiskOnly: true})
	if err != nil {
		t.Fatalf("monitoring risk filter: %v", err)
	}
	if !containsID(risky.Projects, "PJ-RS-MIX") {
		t.Fatalf("risk 筛选未沿用 sla 逻辑命中报告阶段超期项目，实际 %v", projectIDList(risky.Projects))
	}
}

// sameCounts 比较两个计数映射。
func sameCounts(got, want map[string]int) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
