package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// TestComputeSlaItemsMatchesReportPhaseRules 验证报告阶段规则的匹配值与计时基准：
// 规则状态匹配 item.ReportStatus，停留时长以 ReportChangedAt（report_updated_at）为准，
// 命中后填充 ReportStage 与 RuleDimension；状态规则保持原行为。
func TestComputeSlaItemsMatchesReportPhaseRules(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	candidate := domain.SlaOverdueItem{
		ID: "SI-1", ProjectID: "PJ-1", Status: domain.ProjectStatusFieldCompleted,
		ReportStatus: "RECTIFYING",
		// 现场完成已 90 小时，报告整改只停留 30 小时：报告规则必须用后者计时。
		StatusChangedAt: now.Add(-90 * time.Hour),
		ReportChangedAt: now.Add(-30 * time.Hour),
	}
	rules := []domain.Rule{
		{Kind: "sla", Enabled: true, Name: "整改停留", Status: "RECTIFYING", DeadlineHours: 10},
		{Kind: "sla", Enabled: true, Name: "现场完成停留", Status: domain.ProjectStatusFieldCompleted, DeadlineHours: 50},
		{Kind: "sla", Enabled: true, Name: "编制停留", Status: "COMPILING", DeadlineHours: 10},
	}
	items, unparsable := computeSlaItems([]domain.SlaOverdueItem{candidate}, rules, now)
	if unparsable != 0 {
		t.Fatalf("unparsable=%d，期望0", unparsable)
	}
	if len(items) != 2 {
		t.Fatalf("SLA 条目=%+v，期望2条（整改+现场完成）", items)
	}
	reportItem, statusItem := items[0], items[1]
	if reportItem.RuleDimension != domain.SlaDimensionReport || reportItem.ReportStage != "RECTIFYING" {
		t.Fatalf("报告阶段条目=%+v，期望维度 REPORT、阶段 RECTIFYING", reportItem)
	}
	if reportItem.Kind != domain.SlaKindStatusOverdue || reportItem.OverdueHours != 20 || reportItem.DeadlineHours != 10 {
		t.Fatalf("报告阶段超期计算错误: %+v（必须用 report_updated_at 的 30 小时，而不是 status_changed_at 的 90 小时）", reportItem)
	}
	if statusItem.RuleDimension != domain.SlaDimensionStatus || statusItem.ReportStage != "" {
		t.Fatalf("状态条目=%+v，期望维度 STATUS、无报告阶段", statusItem)
	}
	if statusItem.OverdueHours != 40 {
		t.Fatalf("状态条目超期=%d，期望40", statusItem.OverdueHours)
	}
}

// TestComputeSlaItemsReportRuleRequiresReportTiming 验证报告阶段规则缺少计时基准或状态不匹配时不误报。
func TestComputeSlaItemsReportRuleRequiresReportTiming(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	rules := []domain.Rule{{Kind: "sla", Enabled: true, Name: "整改停留", Status: "rectifying", DeadlineHours: 1}}
	// 没有 report_updated_at：不能用零值时间算出天文数字停留时长。
	noTiming := domain.SlaOverdueItem{ID: "SI-1", Status: domain.ProjectStatusFieldCompleted, ReportStatus: "RECTIFYING", StatusChangedAt: now.Add(-100 * time.Hour)}
	if items, _ := computeSlaItems([]domain.SlaOverdueItem{noTiming}, rules, now); len(items) != 0 {
		t.Fatalf("缺少报告计时基准仍产生条目: %+v", items)
	}
	// 报告阶段不匹配（含大小写与空白归一化）：NONE 不命中 RECTIFYING。
	mismatch := domain.SlaOverdueItem{ID: "SI-1", Status: domain.ProjectStatusFieldCompleted, ReportStatus: " none ", ReportChangedAt: now.Add(-100 * time.Hour)}
	if items, _ := computeSlaItems([]domain.SlaOverdueItem{mismatch}, rules, now); len(items) != 0 {
		t.Fatalf("报告阶段不匹配仍产生条目: %+v", items)
	}
	// 报告阶段只对「现场实施完成」成立：现场状态回退后残留的旧报告码不得产生 SLA。
	stale := domain.SlaOverdueItem{ID: "SI-3", Status: domain.ProjectStatusInProgress, ReportStatus: "RECTIFYING", ReportChangedAt: now.Add(-100 * time.Hour)}
	if items, _ := computeSlaItems([]domain.SlaOverdueItem{stale}, rules, now); len(items) != 0 {
		t.Fatalf("未到现场完成的服务项产生报告阶段 SLA: %+v", items)
	}
	// 大小写与首尾空白不影响匹配。
	matched := domain.SlaOverdueItem{ID: "SI-2", Status: domain.ProjectStatusFieldCompleted, ReportStatus: "  Rectifying ", ReportChangedAt: now.Add(-5 * time.Hour)}
	items, _ := computeSlaItems([]domain.SlaOverdueItem{matched}, rules, now)
	if len(items) != 1 || items[0].ReportStage != "RECTIFYING" || items[0].OverdueHours != 4 {
		t.Fatalf("归一化后未命中报告阶段规则: %+v", items)
	}
}

// TestSlaNotificationTextUsesReportStageLabel 验证通知文案用「报告·整改中」这类阶段名。
func TestSlaNotificationTextUsesReportStageLabel(t *testing.T) {
	item := domain.SlaOverdueItem{ID: "SI-1", Kind: domain.SlaKindStatusOverdue, Status: domain.ProjectStatusFieldCompleted, ReportStage: "RECTIFYING", DeadlineHours: 24, OverdueHours: 3}
	_, content := slaNotificationText(item)
	if !strings.Contains(content, "报告·整改中") {
		t.Fatalf("报告阶段通知文案=%q，期望包含「报告·整改中」", content)
	}
	statusItem := domain.SlaOverdueItem{ID: "SI-2", Kind: domain.SlaKindStatusOverdue, Status: domain.ProjectStatusInProgress, DeadlineHours: 24, OverdueHours: 3}
	if _, content := slaNotificationText(statusItem); !strings.Contains(content, "「"+domain.ProjectStatusInProgress+"」") {
		t.Fatalf("状态通知文案=%q，期望沿用服务项状态名", content)
	}
}

// TestSlaIdempotencyKeyDistinguishesDimensions 验证同一服务项同一天的两类规则通知不会撞键。
func TestSlaIdempotencyKeyDistinguishesDimensions(t *testing.T) {
	base := domain.SlaOverdueItem{ID: "SI-1", Kind: domain.SlaKindStatusOverdue}
	report := base
	report.RuleDimension, report.RuleStatus = domain.SlaDimensionReport, "RECTIFYING"
	status := base
	status.RuleDimension, status.RuleStatus = domain.SlaDimensionStatus, domain.ProjectStatusFieldCompleted
	plan := base
	if slaIdempotencyKey(report, "2026-11-04") == slaIdempotencyKey(status, "2026-11-04") {
		t.Fatal("状态规则与报告阶段规则的幂等键不能相同")
	}
	if slaIdempotencyKey(plan, "2026-11-04") == slaIdempotencyKey(report, "2026-11-04") {
		t.Fatal("计划完成口径与报告阶段规则的幂等键不能相同")
	}
	want := "sla-SI-1-" + domain.SlaKindStatusOverdue + "-REPORT-RECTIFYING-2026-11-04"
	if got := slaIdempotencyKey(report, "2026-11-04"); got != want {
		t.Fatalf("幂等键=%q，期望%q", got, want)
	}
}

// TestReportPhaseSLAThreeWayConsistency 守护「规则目录 / Go 侧判定 / 通知文案」
// 三处使用同一份报告阶段清单：目录包含全部 5 个码且不含 NONE；每个码都能被
// computeSlaItems 命中并标注 ReportStage；通知文案使用 domain 的展示名。
func TestReportPhaseSLAThreeWayConsistency(t *testing.T) {
	_, statuses := RuleConfigurationCatalog()
	for _, code := range domain.ReportPhaseSLACodes() {
		if !ruleOptionExists(statuses, code) {
			t.Fatalf("SLA 规则目录缺少报告阶段 %q", code)
		}
	}
	if ruleOptionExists(statuses, domain.ReportPhaseNone) {
		t.Fatal("NONE 不应作为 SLA 停留阶段（由现场实施完成规则覆盖）")
	}
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for _, code := range domain.ReportPhaseSLACodes() {
		candidate := domain.SlaOverdueItem{
			ID: "SI-" + code, Status: domain.ProjectStatusFieldCompleted,
			ReportStatus: code, ReportChangedAt: now.Add(-5 * time.Hour),
		}
		rules := []domain.Rule{{Kind: "sla", Enabled: true, Name: "规则-" + code, Status: code, DeadlineHours: 1}}
		items, _ := computeSlaItems([]domain.SlaOverdueItem{candidate}, rules, now)
		if len(items) != 1 {
			t.Fatalf("%s 未产生 SLA 条目", code)
		}
		if items[0].ReportStage != code || items[0].RuleDimension != domain.SlaDimensionReport {
			t.Fatalf("%s 条目维度=%q stage=%q", code, items[0].RuleDimension, items[0].ReportStage)
		}
		_, content := slaNotificationText(items[0])
		if !strings.Contains(content, "报告·"+domain.ReportPhaseLabel(code)) {
			t.Fatalf("%s 通知文案=%q，期望包含 %q", code, content, "报告·"+domain.ReportPhaseLabel(code))
		}
	}
}

// TestMonitoringMatchesAppliesReportStage 验证监控内存回退与数据库分页使用同一报告阶段口径。
func TestMonitoringMatchesAppliesReportStage(t *testing.T) {
	row := domain.MonitoringProject{Project: domain.Project{ID: "PJ-1", Status: domain.ProjectStatusReporting, ReportStage: "RECTIFYING"}}
	if !monitoringMatches(row, domain.ProjectMonitoringQuery{}) {
		t.Fatal("无筛选时应命中")
	}
	if !monitoringMatches(row, domain.ProjectMonitoringQuery{ReportStage: " rectifying "}) {
		t.Fatal("报告阶段筛选应大小写不敏感")
	}
	if monitoringMatches(row, domain.ProjectMonitoringQuery{ReportStage: "COMPILING"}) {
		t.Fatal("报告阶段不一致时不应命中")
	}
	// 未进入报告段的项目（report_stage 为空）只在没有筛选时出现。
	empty := domain.MonitoringProject{Project: domain.Project{ID: "PJ-2", Status: domain.ProjectStatusInProgress}}
	if monitoringMatches(empty, domain.ProjectMonitoringQuery{ReportStage: "NONE"}) {
		t.Fatal("未进入报告段的项目不应被报告阶段筛选命中")
	}
	// 与现有状态筛选 AND 组合。
	if !monitoringMatches(row, domain.ProjectMonitoringQuery{Status: domain.ProjectStatusReporting, ReportStage: "RECTIFYING"}) {
		t.Fatal("状态与报告阶段应可组合")
	}
	if monitoringMatches(row, domain.ProjectMonitoringQuery{Status: domain.ProjectStatusInProgress, ReportStage: "RECTIFYING"}) {
		t.Fatal("状态不匹配时组合筛选不应命中")
	}
}

// capturingProjectRepository 记录应用层传给仓储的项目列表查询，用于断言归一化结果。
type capturingProjectRepository struct {
	scopeRepository
	lastQuery domain.ProjectListQuery
	calls     int
}

func (r *capturingProjectRepository) ListProjects(_ context.Context, _ platform.ScopeFilter, query domain.ProjectListQuery) ([]domain.Project, int, error) {
	r.calls++
	r.lastQuery = query
	return nil, 0, nil
}

// TestListProjectsPageValidatesAndNormalizesReportStage 验证列表查询契约：
// report_stage 大小写/空白归一化后下推，非法值返回 422 校验错误且不触达仓储。
func TestListProjectsPageValidatesAndNormalizesReportStage(t *testing.T) {
	repository := &capturingProjectRepository{}
	service := &Service{Repo: repository}
	principal := principalWith("project.read", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION"})

	if _, _, err := service.ListProjectsPage(context.Background(), principal, domain.ProjectListQuery{ReportStage: "  compiling ", Page: 1, PageSize: 20}); err != nil {
		t.Fatalf("合法报告阶段被拒绝: %v", err)
	}
	if repository.lastQuery.ReportStage != "COMPILING" {
		t.Fatalf("下推的报告阶段=%q，期望归一化为 COMPILING", repository.lastQuery.ReportStage)
	}
	if repository.lastQuery.Page != 1 || repository.lastQuery.PageSize != 20 {
		t.Fatalf("分页参数丢失: %+v", repository.lastQuery)
	}

	before := repository.calls
	_, _, err := service.ListProjectsPage(context.Background(), principal, domain.ProjectListQuery{ReportStage: "DRAFTING"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("非法报告阶段错误=%v，期望校验错误(422)", err)
	}
	if repository.calls != before {
		t.Fatal("非法报告阶段不应触达仓储")
	}
	// 既有 status 参数保持宽松：未知值照常下推，由 SQL 谓词返回空集。
	if _, _, err := service.ListProjectsPage(context.Background(), principal, domain.ProjectListQuery{Status: "不存在状态"}); err != nil {
		t.Fatalf("宽松 status 参数不应报错: %v", err)
	}
	if repository.lastQuery.Status != "不存在状态" {
		t.Fatalf("status 参数被改动: %q", repository.lastQuery.Status)
	}
}

// TestScanSlaNotificationsPublishesBothDimensions 端到端验证：同一服务项同一天
// 同时命中「现场实施完成」状态规则与「RECTIFYING」报告阶段规则时，两条通知
// 都会入队且幂等键不同；报告阶段通知文案使用阶段名。
func TestScanSlaNotificationsPublishesBothDimensions(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	repo := &assigneeRepository{
		hookRepository: hookRepository{
			overdue: []domain.SlaOverdueItem{{
				ID: "SI-1", ProjectID: "PJ-1", Status: domain.ProjectStatusFieldCompleted,
				ReportStatus:    domain.ReportPhaseRectifying,
				StatusChangedAt: now.Add(-90 * time.Hour),
				ReportChangedAt: now.Add(-30 * time.Hour),
			}},
			rules: []domain.Rule{
				{Kind: "sla", Enabled: true, Name: "现场完成停留", Status: domain.ProjectStatusFieldCompleted, DeadlineHours: 50},
				{Kind: "sla", Enabled: true, Name: "整改停留", Status: domain.ReportPhaseRectifying, DeadlineHours: 10},
			},
		},
		item: domain.ServiceItem{TenantID: "t1", ProjectID: "PJ-1", TeamLeadID: "u-lead"},
	}
	service := &Service{Repo: repo}

	published, err := service.ScanSlaNotifications(context.Background(), "t1", now)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if published != 2 || len(repo.enqueued) != 2 {
		t.Fatalf("同一服务项两个维度应各入队一条，实际 published=%d %+v", published, repo.enqueued)
	}
	keys := map[string]bool{}
	var reportContent string
	for _, message := range repo.enqueued {
		if keys[message.IdempotencyKey] {
			t.Fatalf("幂等键重复: %q", message.IdempotencyKey)
		}
		keys[message.IdempotencyKey] = true
		if strings.Contains(message.Content, "报告·") {
			reportContent = message.Content
		}
	}
	if reportContent == "" {
		t.Fatalf("报告阶段通知文案缺失阶段名: %+v", repo.enqueued)
	}
	if !strings.Contains(reportContent, "报告·整改中") {
		t.Fatalf("报告阶段通知文案=%q", reportContent)
	}
}
