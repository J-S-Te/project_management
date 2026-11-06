package mysql

import (
	"context"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// 本文件用 DryRun SQL 捕获断言报告阶段相关的查询形状：
// 聚合 join 只加一次、归档项被排除、报告 Phase 列存在、绑定顺序正确，
// 以及监控计数确实按派生表达式分组而不是存储列。

// TestListProjectsReportStageFilterPushesDerivedPhaseIntoSQL 验证报告阶段筛选下推 SQL 的形状与参数顺序。
// 参数：t 为测试上下文。
// 返回值：无；SQL 缺少派生列、重复 join、漏排归档项或参数顺序错误时测试失败。
func TestListProjectsReportStageFilterPushesDerivedPhaseIntoSQL(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	// 筛选值大小写与首尾空白不影响绑定：归一化后 RECTIFYING 绑定 phase=1。
	if _, _, err := repository.ListProjects(context.Background(), filter, domain.ProjectListQuery{ReportStage: " rectifying ", Page: 1, PageSize: 25}); err != nil {
		t.Fatalf("list projects by report stage: %v", err)
	}
	if len(capture.queries) != 2 {
		t.Fatalf("expected COUNT + page query, got %d: %v", len(capture.queries), capture.queries)
	}
	for _, sql := range capture.queries {
		if got := strings.Count(sql, "LEFT JOIN"); got != 1 {
			t.Fatalf("聚合 join 必须在单个查询里只加一次，实际 %d 次:\n%s", got, sql)
		}
		if !strings.Contains(sql, "aggregated.archived_at IS NULL") {
			t.Fatalf("聚合子查询必须排除归档服务项:\n%s", sql)
		}
		if !strings.Contains(sql, "AS lowest_report_phase") {
			t.Fatalf("聚合子查询缺少 lowest_report_phase 派生列:\n%s", sql)
		}
		if !strings.Contains(sql, "UPPER(TRIM(COALESCE(aggregated.report_status, ''))) = 'ARCHIVED' THEN 5") {
			t.Fatalf("报告 Phase 分支未由报告阶段表生成:\n%s", sql)
		}
		if !strings.Contains(sql, "COALESCE(derived_status.lowest_rank, 0) BETWEEN 6 AND 8") {
			t.Fatalf("报告阶段谓词必须要求全部非终止项已到现场完成:\n%s", sql)
		}
		if !strings.Contains(sql, "COALESCE(derived_status.lowest_report_phase, -1) = 1") {
			t.Fatalf("报告阶段必须按 int 阶段序号绑定:\n%s", sql)
		}
	}
	// 绑定顺序：Joins 的 tenantID 在前，报告阶段 Phase 在后。
	pageSQL := capture.queries[1]
	tenantIndex := strings.Index(pageSQL, "aggregated.tenant_id = 'tenant-1'")
	phaseIndex := strings.Index(pageSQL, "COALESCE(derived_status.lowest_report_phase, -1) = 1")
	if tenantIndex < 0 || phaseIndex < 0 || tenantIndex > phaseIndex {
		t.Fatalf("绑定顺序错误：tenant=%d phase=%d:\n%s", tenantIndex, phaseIndex, pageSQL)
	}
}

// TestListProjectsCombinesStatusAndReportStageFilters 验证两个筛选可组合且共用一次 join。
// 参数：t 为测试上下文。
// 返回值：无；重复 join、缺谓词或绑定顺序错误时测试失败。
func TestListProjectsCombinesStatusAndReportStageFilters(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, _, err := repository.ListProjects(context.Background(), filter, domain.ProjectListQuery{
		Status: domain.ProjectStatusReporting, ReportStage: "COMPILING", Page: 2, PageSize: 10,
	}); err != nil {
		t.Fatalf("list projects by status and report stage: %v", err)
	}
	if len(capture.queries) != 2 {
		t.Fatalf("expected COUNT + page query, got %d: %v", len(capture.queries), capture.queries)
	}
	pageSQL := capture.queries[1]
	for _, sql := range capture.queries {
		if got := strings.Count(sql, "LEFT JOIN"); got != 1 {
			t.Fatalf("组合筛选必须在单个查询里只加一次 join，实际 %d 次:\n%s", got, sql)
		}
		if !strings.Contains(sql, "END = '"+domain.ProjectStatusReporting+"'") {
			t.Fatalf("缺少派生状态谓词:\n%s", sql)
		}
		if !strings.Contains(sql, "COALESCE(derived_status.lowest_report_phase, -1) = 2") {
			t.Fatalf("缺少报告阶段谓词:\n%s", sql)
		}
	}
	tenantIndex := strings.Index(pageSQL, "aggregated.tenant_id = 'tenant-1'")
	statusIndex := strings.Index(pageSQL, "END = '"+domain.ProjectStatusReporting+"'")
	phaseIndex := strings.Index(pageSQL, "COALESCE(derived_status.lowest_report_phase, -1) = 2")
	if tenantIndex < 0 || statusIndex < 0 || phaseIndex < 0 || !(tenantIndex < statusIndex && statusIndex < phaseIndex) {
		t.Fatalf("绑定顺序错误：tenant=%d status=%d phase=%d:\n%s", tenantIndex, statusIndex, phaseIndex, pageSQL)
	}
	for _, expected := range []string{"LIMIT 10", "OFFSET 10"} {
		if !strings.Contains(pageSQL, expected) {
			t.Fatalf("分页参数缺失 %q:\n%s", expected, pageSQL)
		}
	}
}

// TestListProjectsKeywordAndReportStageComposeWithAnd 验证关键字过滤整组带括号，
// 与报告阶段谓词按 AND 组合（裸 OR 会因优先级把筛选行"或"回来并抬高 total）。
func TestListProjectsKeywordAndReportStageComposeWithAnd(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, _, err := repository.ListProjects(context.Background(), filter, domain.ProjectListQuery{Keyword: "机房", ReportStage: "RECTIFYING", Page: 1, PageSize: 10}); err != nil {
		t.Fatalf("list projects by keyword and report stage: %v", err)
	}
	if len(capture.queries) != 2 {
		t.Fatalf("expected COUNT + page query, got %d: %v", len(capture.queries), capture.queries)
	}
	for _, sql := range capture.queries {
		// GORM 会把带多参数的表达式再包一层括号，因此这里同时接受单层/双层。
		if !strings.Contains(sql, "AND (id LIKE '%机房%' OR name LIKE '%机房%'") &&
			!strings.Contains(sql, "AND ((id LIKE '%机房%' OR name LIKE '%机房%'") {
			t.Fatalf("关键字整组 OR 必须加括号:\n%s", sql)
		}
		keywordIndex := strings.Index(sql, "(id LIKE '%机房%'")
		phaseIndex := strings.Index(sql, "COALESCE(derived_status.lowest_report_phase, -1) = 1")
		if keywordIndex < 0 || phaseIndex < 0 || keywordIndex > phaseIndex {
			t.Fatalf("关键字谓词必须在报告阶段谓词之前: keyword=%d phase=%d:\n%s", keywordIndex, phaseIndex, sql)
		}
	}
}

// TestListProjectsUnknownReportStageSkipsQuery 验证未知报告阶段码不会退化成"匹配未知行"。
// 参数：t 为测试上下文。
// 返回值：无；未知码仍发出查询或返回错误时测试失败。
func TestListProjectsUnknownReportStageSkipsQuery(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	items, total, err := repository.ListProjects(context.Background(), filter, domain.ProjectListQuery{ReportStage: "BOGUS"})
	if err != nil || len(items) != 0 || total != 0 {
		t.Fatalf("unknown report stage = (%d rows, total %d, err %v), want empty", len(items), total, err)
	}
	if len(capture.queries) != 0 {
		t.Fatalf("未知报告阶段不应发出查询（-1 会误配未知行）: %v", capture.queries)
	}
}

// TestMonitoringCountsGroupByDerivedStatusAndReportPhase 验证监控计数按派生表达式分组。
// 参数：t 为测试上下文。
// 返回值：无；仍按存储列 pm_project.status 分组或缺少报告阶段计数时测试失败。
func TestMonitoringCountsGroupByDerivedStatusAndReportPhase(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, err := repository.LoadProjectMonitoringPage(context.Background(), filter, domain.ProjectMonitoringQuery{Page: 1, PageSize: 20, ReportStage: "REVIEWED"}); err != nil {
		t.Fatalf("load monitoring page: %v", err)
	}
	joined := strings.Join(capture.queries, "\n")
	if strings.Contains(joined, "GROUP BY pm_project.status") {
		t.Fatalf("状态计数不得 group by 存储列 pm_project.status:\n%s", joined)
	}
	var statusCountSQL, reportCountSQL string
	for _, sql := range capture.queries {
		if got := strings.Count(sql, "LEFT JOIN"); got > 1 {
			t.Fatalf("监控查询必须只加一次聚合 join，实际 %d 次:\n%s", got, sql)
		}
		if strings.Contains(sql, "AS status, COUNT(*) AS count") {
			statusCountSQL = sql
		}
		if strings.Contains(sql, "AS phase, COUNT(*) AS count") {
			reportCountSQL = sql
		}
	}
	if statusCountSQL == "" || !strings.Contains(statusCountSQL, "CASE") || !strings.Contains(statusCountSQL, "derived_status") {
		t.Fatalf("状态计数必须对派生状态表达式分组:\n%s", statusCountSQL)
	}
	if reportCountSQL == "" || !strings.Contains(reportCountSQL, "lowest_report_phase") || !strings.Contains(reportCountSQL, "BETWEEN 0 AND 5") {
		t.Fatalf("报告阶段计数必须对派生 Phase 分组:\n%s", reportCountSQL)
	}
	if !strings.Contains(joined, "COALESCE(derived_status.lowest_report_phase, -1) = 3") {
		t.Fatalf("监控报告阶段筛选必须按 int 阶段序号绑定:\n%s", joined)
	}
}

// TestMonitoringSLAPredicateCoversReportPhaseDimension 验证监控 SLA 谓词与报告阶段表一致：
// 5 个报告阶段码全部出现、报告阶段用 report_updated_at 计时、状态维度保持 status_changed_at，
// 且进入条件限定在现场实施完成。这是「三处一致」中 SQL 侧的守护。
// 参数：t 为测试上下文。
// 返回值：无；码清单或计时列与 domain 不一致时测试失败。
func TestMonitoringSLAPredicateCoversReportPhaseDimension(t *testing.T) {
	predicate, args := monitoringSLAPredicate()
	codes := domain.ReportPhaseSLACodes()
	if len(codes) != 5 {
		t.Fatalf("SLA 报告阶段码数量=%d，期望5", len(codes))
	}
	for _, code := range codes {
		if !strings.Contains(predicate, "'"+code+"'") {
			t.Fatalf("SLA 谓词缺少报告阶段码 %q:\n%s", code, predicate)
		}
	}
	if strings.Contains(predicate, "'"+domain.ReportPhaseNone+"'") {
		t.Fatalf("NONE 不应作为 SLA 停留阶段（由现场实施完成规则覆盖）:\n%s", predicate)
	}
	for _, expected := range []string{
		"pm_service_item.status_changed_at", "pm_service_item.report_updated_at",
		"TRIM(pm_service_item.status) = '" + domain.ProjectStatusFieldCompleted + "'",
		"UPPER(TRIM(COALESCE(pm_service_item.report_status, '')))",
	} {
		if !strings.Contains(predicate, expected) {
			t.Fatalf("SLA 谓词缺少 %q:\n%s", expected, predicate)
		}
	}
	if len(args) != 2 || args[0] != domain.ProjectStatusTerminated || args[1] != domain.ProjectStatusFieldCompleted {
		t.Fatalf("SLA 谓词绑定参数=%v，期望 [已终止 现场实施完成]", args)
	}
	if got := strings.Count(predicate, "?"); got != 2 {
		t.Fatalf("SLA 谓词占位符数量=%d，期望2（与 args 一致）:\n%s", got, predicate)
	}
}
