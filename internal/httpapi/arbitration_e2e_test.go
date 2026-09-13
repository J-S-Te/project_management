package httpapi_test

// 候选缺陷裁决测试（临时验证用，可在验收后删除）。
//
// 目的：把静态审计得出的候选缺陷逐条用**运行时证据**判定——能复现的才算成立，
// 不能复现的当场推翻。每个子测试断言的是「缺陷行为」，因此：
//   PASS = 缺陷已复现（成立）
//   FAIL = 未能复现（需按推翻处理，或调整复现条件）
//
// 需要 PM_TEST_DSN；只在隔离租户 PM-ARB-TENANT 内读写，每个子测试自行清理。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/httpapi"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const arbTenant = "PM-ARB-TENANT"

type arbEnv struct {
	t       *testing.T
	db      *gorm.DB
	handler http.Handler
}

func newArbEnv(t *testing.T) *arbEnv {
	t.Helper()
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the arbitration checks")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	env := &arbEnv{t: t, db: db, handler: httpapi.NewRouter(service, switchIdentityFor(arbTenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	env.clean()
	t.Cleanup(env.clean)
	return env
}

func (e *arbEnv) clean() {
	for _, statement := range []string{
		`DELETE FROM pm_delivery_event WHERE tenant_id = '` + arbTenant + `'`,
		`DELETE FROM pm_capability WHERE tenant_id = '` + arbTenant + `'`,
		`DELETE FROM pm_impl_plan WHERE tenant_id = '` + arbTenant + `'`,
		`DELETE FROM pm_service_item WHERE tenant_id = '` + arbTenant + `'`,
		`DELETE FROM pm_project WHERE tenant_id = '` + arbTenant + `'`,
		`DELETE FROM pm_sla WHERE tenant_id = '` + arbTenant + `'`,
	} {
		e.db.Exec(statement)
	}
}

func (e *arbEnv) exec(statements ...string) {
	e.t.Helper()
	for _, statement := range statements {
		if err := e.db.Exec(statement).Error; err != nil {
			e.t.Fatalf("seed 失败: %v\n%s", err, statement)
		}
	}
}

// call 以指定角色发起请求，返回状态码与响应体。
func (e *arbEnv) call(role, method, path, body string) (int, string) {
	request := newRoleRequest(role, method, path, body)
	recorder := newRecorder()
	e.handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

func (e *arbEnv) get(role, path string) string {
	e.t.Helper()
	status, body := e.call(role, http.MethodGet, path, "")
	if status != http.StatusOK {
		e.t.Fatalf("GET %s 失败: HTTP %d %s", path, status, body)
	}
	return body
}

func (e *arbEnv) projectStatus(projectID string) (string, int) {
	e.t.Helper()
	body := e.get("business_admin", "/api/v1/projects/"+projectID)
	var detail struct {
		Data struct {
			Status   string `json:"status"`
			Progress int    `json:"progress"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		e.t.Fatalf("解析项目详情失败: %v (%s)", err, body)
	}
	return detail.Data.Status, detail.Data.Progress
}

func (e *arbEnv) slaItems() []struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Kind         string `json:"kind"`
	OverdueHours int64  `json:"overdue_hours"`
} {
	e.t.Helper()
	body := e.get("business_admin", "/api/v1/delivery/sla-overdue")
	var payload struct {
		Data []struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			Kind         string `json:"kind"`
			OverdueHours int64  `json:"overdue_hours"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		e.t.Fatalf("解析 SLA 响应失败: %v (%s)", err, body)
	}
	return payload.Data
}

// ---- PM-PERM-01：project_manager 无法确认拆解（manifest 已移除该权限）----

func TestArbitrationPMPerm01ProjectManagerCannotConfirmDecomposition(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-PERM', '`+arbTenant+`', '权限裁决', '客户', 'C-ARB', 'v1', 'NONE', 1, '待拆解确认', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-ARB-PERM', '`+arbTenant+`', 'PJ-ARB-PERM', 'S1', 'r', 'STANDARD', '待确认', 'NONE', 'UNCHECKED', NOW(3), NOW(3))`,
	)
	status, body := env.call("project_manager", http.MethodPost, "/api/v1/service-items/confirm", `{"ids":["SI-ARB-PERM"]}`)
	t.Logf("project_manager 确认拆解 -> HTTP %d %s", status, body)
	if status != http.StatusForbidden {
		t.Fatalf("候选不成立：project_manager 确认拆解返回 %d，预期 403（manifest 已移除 service_item.confirm）", status)
	}
	// 反向确认：业务管理员同一动作应当成功，排除"接口本身不可用"的解释。
	status, body = env.call("business_admin", http.MethodPost, "/api/v1/service-items/confirm", `{"ids":["SI-ARB-PERM"]}`)
	t.Logf("business_admin 确认拆解 -> HTTP %d", status)
	if status != http.StatusOK {
		t.Fatalf("对照组失败：business_admin 确认拆解返回 %d %s", status, body)
	}
}

// ---- PM-STATUS-01：未知状态把项目拖回「待拆解确认」----

func TestArbitrationPMStatus01UnknownStatusNowSurfaced(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-UNK', '`+arbTenant+`', '未知状态裁决', '客户', 'C-ARB', 'v1', 'NONE', 2, '已完成', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-ARB-UNK-1', '`+arbTenant+`', 'PJ-ARB-UNK', 'S1', 'r', 'STANDARD', '现场实施完成', 'ARCHIVED', 'PASSED', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-ARB-UNK-2', '`+arbTenant+`', 'PJ-ARB-UNK', 'S2', 'r', 'STANDARD', '未来新增状态', 'NONE', 'UNCHECKED', NOW(3), NOW(3))`,
	)
	status, progress := env.projectStatus("PJ-ARB-UNK")
	t.Logf("含 1 个未知状态服务项的项目 -> 状态=%s 进度=%d", status, progress)
	if status != "待拆解确认" {
		t.Fatalf("候选不成立：状态=%s（预期被拖回「待拆解确认」）", status)
	}
	// 修复后：未知状态仍被保守地按最滞后处理（不误报项目更晚期），
	// 但必须显式暴露出来，不能让项目状态静默变化。
	body := env.get("business_admin", "/api/v1/dashboard")
	var dashboard struct {
		Data struct {
			UnknownStatusItems int `json:"unknown_status_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &dashboard); err != nil {
		t.Fatalf("解析看板失败: %v (%s)", err, body)
	}
	t.Logf("看板 unknown_status_items=%d", dashboard.Data.UnknownStatusItems)
	if dashboard.Data.UnknownStatusItems == 0 {
		t.Fatal("未知状态必须被看板显式暴露，否则项目状态会静默变化")
	}
}

// ---- PM-STATUS-02：部分终止的项目仍算「已完成」，且不计入风险项目 ----

func TestArbitrationPMStatus02PartiallyTerminatedNowCountsAsRisk(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-MIX', '`+arbTenant+`', '部分终止裁决', '客户', 'C-ARB', 'v1', 'NONE', 2, '待实施', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-ARB-MIX-1', '`+arbTenant+`', 'PJ-ARB-MIX', 'S1', 'r', 'STANDARD', '已终止', 'NONE', 'PASSED', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-ARB-MIX-2', '`+arbTenant+`', 'PJ-ARB-MIX', 'S2', 'r', 'STANDARD', '现场实施完成', 'ARCHIVED', 'PASSED', NOW(3), NOW(3))`,
	)
	status, progress := env.projectStatus("PJ-ARB-MIX")
	body := env.get("business_admin", "/api/v1/dashboard")
	var dashboard struct {
		Data struct {
			RiskProjects int `json:"risk_projects"`
			ProjectCount int `json:"project_count"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(body), &dashboard)
	t.Logf("1 终止 + 1 归档 -> 状态=%s 进度=%d；看板 risk_projects=%d project_count=%d",
		status, progress, dashboard.Data.RiskProjects, dashboard.Data.ProjectCount)
	if status != "已完成" {
		t.Fatalf("候选不成立：状态=%s（预期「已完成」）", status)
	}
	// 该缺陷已于 2026-09-13 修复（风险口径扩展为"或存在已终止服务项"）：
	// 状态仍是「已完成」（剩余工作确实做完了），但必须计入风险。
	if dashboard.Data.RiskProjects != 1 {
		t.Fatalf("修复后仍未计入风险：risk_projects=%d（预期 1）", dashboard.Data.RiskProjects)
	}
}

// ---- PM-PROG-01：全部终止的项目进度 = 100% ----

func TestArbitrationPMProg01AllTerminatedNowShowsZeroProgress(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-TERM', '`+arbTenant+`', '全终止裁决', '客户', 'C-ARB', 'v1', 'NONE', 1, '待实施', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-ARB-TERM', '`+arbTenant+`', 'PJ-ARB-TERM', 'S1', 'r', 'STANDARD', '已终止', 'NONE', 'PASSED', NOW(3), NOW(3))`,
	)
	status, progress := env.projectStatus("PJ-ARB-TERM")
	t.Logf("全部服务项已终止 -> 状态=%s 进度=%d", status, progress)
	// 该缺陷已于 2026-09-13 修复：项目被放弃不等于交付完成，进度改为 0；
	// "存在终止项"这条信息由风险口径承载（见 PM-STATUS-02）。
	if status != "已终止" || progress != 0 {
		t.Fatalf("修复后进度仍非 0：状态=%s 进度=%d（预期 已终止 / 0）", status, progress)
	}
}

// ---- PM-SLA-02：规则状态与候选状态只做单侧 Trim，带空格则规则静默失效 ----

func TestArbitrationPMSla02WhitespaceStatusNowMatchesRule(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-WS', '`+arbTenant+`', '空格裁决', '客户', 'C-ARB', 'v1', 'NONE', 1, '待实施', NOW(3), NOW(3))`,
		// 状态带一个前导空格：业务上等同「实施中」
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, status_changed_at, created_at, updated_at)
		 VALUES ('SI-ARB-WS', '`+arbTenant+`', 'PJ-ARB-WS', 'S1', 'r', 'STANDARD', ' 实施中', 'NONE', 'PASSED', DATE_SUB(NOW(3), INTERVAL 2 DAY), DATE_SUB(NOW(3), INTERVAL 2 DAY), DATE_SUB(NOW(3), INTERVAL 2 DAY))`,
		`UPDATE pm_service_item SET planned_end = DATE_SUB(NOW(3), INTERVAL 1 DAY) WHERE tenant_id='`+arbTenant+`' AND id='SI-ARB-WS'`,
		`INSERT INTO pm_sla (tenant_id, kind, name, status, deadline_hours, remind_hours, enabled, created_at, updated_at, updated_by)
		 VALUES ('`+arbTenant+`', 'sla', '空格规则', '实施中', 10, 5, 1, NOW(3), NOW(3), 'seed')`,
	)
	items := env.slaItems()
	kinds := make([]string, 0, len(items))
	hasStatusKind := false
	for _, item := range items {
		kinds = append(kinds, item.Kind)
		if strings.HasPrefix(item.Kind, "STATUS_") {
			hasStatusKind = true
		}
	}
	t.Logf("状态带空格的候选 -> SLA 条目 %d 条 %v", len(items), kinds)
	if len(items) == 0 {
		t.Fatalf("复现前提不成立：该服务项本应进入候选集（计划完成时间已过），却返回 0 条")
	}
	// 该缺陷已于 2026-09-13 修复（两侧统一 Trim）：带空格的脏数据现在能正常命中规则。
	if !hasStatusKind {
		t.Fatalf("修复后仍未命中：带空格的状态未匹配状态 SLA 规则")
	}
}

// ---- PM-SLA-03：同一服务项同时命中两条口径，计数虚高 ----

func TestArbitrationPMSla03SameItemCountedTwice(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-DUP', '`+arbTenant+`', '重复裁决', '客户', 'C-ARB', 'v1', 'NONE', 1, '待实施', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, status_changed_at, created_at, updated_at)
		 VALUES ('SI-ARB-DUP', '`+arbTenant+`', 'PJ-ARB-DUP', 'S1', 'r', 'STANDARD', '实施中', 'NONE', 'PASSED', DATE_SUB(NOW(3), INTERVAL 2 DAY), DATE_SUB(NOW(3), INTERVAL 2 DAY), DATE_SUB(NOW(3), INTERVAL 2 DAY))`,
		`UPDATE pm_service_item SET planned_end = DATE_SUB(NOW(3), INTERVAL 1 DAY) WHERE tenant_id='`+arbTenant+`' AND id='SI-ARB-DUP'`,
		`INSERT INTO pm_sla (tenant_id, kind, name, status, deadline_hours, remind_hours, enabled, created_at, updated_at, updated_by)
		 VALUES ('`+arbTenant+`', 'sla', '重复规则', '实施中', 10, 5, 1, NOW(3), NOW(3), 'seed')`,
	)
	items := env.slaItems()
	sameItem := 0
	for _, item := range items {
		if item.ID == "SI-ARB-DUP" {
			sameItem++
		}
	}
	t.Logf("同一服务项在 SLA 列表中占 %d 条（总 %d 条）", sameItem, len(items))
	if sameItem < 2 {
		t.Fatalf("候选不成立：同一服务项只占 %d 条", sameItem)
	}
}

// ---- PM-CONC-01：接口不返回任何版本号，客户端无从回传，冲突无法被检测 ----

func TestArbitrationPMConc01NowExposesVersionSignal(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-VER', '`+arbTenant+`', '版本裁决', '客户', 'C-ARB', 'v1', 'NONE', 1, '待实施', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-ARB-VER', '`+arbTenant+`', 'PJ-ARB-VER', 'S1', 'r', 'STANDARD', '待分配', 'NONE', 'PASSED', NOW(3), NOW(3))`,
	)
	itemBody := env.get("business_admin", "/api/v1/service-items?project_id=PJ-ARB-VER")
	projectBody := env.get("business_admin", "/api/v1/projects/PJ-ARB-VER")
	hasVersion := strings.Contains(itemBody, `"version"`) || strings.Contains(projectBody, `"version"`)
	t.Logf("服务项/项目响应中是否含 version 字段: %v", hasVersion)
	// 该缺陷已于 2026-09-13 修复（暴露并发版本号 + 条件更新）：响应必须带 version。
	if !hasVersion {
		t.Fatalf("修复后仍未暴露 version：客户端无法识别版本陈旧")
	}
	// 库里也确实没有该列时，客户端不可能构造条件更新。
	var count int64
	err := env.db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'pm_service_item' AND column_name IN ('version','revision','row_version')`).Scan(&count).Error
	if err != nil {
		t.Fatalf("查询列失败: %v", err)
	}
	t.Logf("pm_service_item 上的版本列数量: %d", count)
	if count != 1 {
		t.Fatalf("修复后应存在 1 个版本列，实际 %d", count)
	}
}

// ---- PM-DATE-01：资质到期当天即不可分配 ----

func TestArbitrationPMDate01ExpiryDayNowAssignable(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-DATE', '`+arbTenant+`', '到期裁决', '客户', 'C-ARB', 'v1', 'NONE', 1, '待分配', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-ARB-DATE', '`+arbTenant+`', 'PJ-ARB-DATE', 'S1', 'r', 'STANDARD', '待分配', 'NONE', 'PASSED', NOW(3), NOW(3))`,
		// 有效期至「今天 00:00」——即到期日当天
		`INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, capability_codes, valid_from, valid_until, status, usage_scope, updated_at, updated_by)
		 VALUES ('CAP-ARB-DATE', '`+arbTenant+`', 'PERSON', 'PM-ARB-ENG', '到期日工程师', JSON_ARRAY('TPL-ARB'),
		         DATE_SUB(NOW(3), INTERVAL 30 DAY), DATE_FORMAT(NOW(3), '%Y-%m-%d 00:00:00'), 'ACTIVE', 'ANY', NOW(3), 'seed')`,
	)
	// 先分配团队负责人：EventExecutionTeamAssigned 的前置不变量要求 TeamLeadID 非空。
	if status, body := env.call("business_admin", http.MethodPost, "/api/v1/service-items/SI-ARB-DATE/team-assignment",
		`{"team_lead_id":"PM-ARB-LEAD"}`); status != http.StatusOK {
		t.Fatalf("前置步骤（分配团队负责人）失败: HTTP %d %s", status, body)
	}
	status, body := env.call("team_lead", http.MethodPost, "/api/v1/service-items/SI-ARB-DATE/execution-assignment",
		`{"project_manager_id":"PM-ARB-PM","engineer_ids":["PM-ARB-ENG"],"required_codes":["TPL-ARB"]}`)
	t.Logf("到期当天分配 -> HTTP %d %s", status, body)
	if status != http.StatusOK {
		t.Fatalf("请求失败: HTTP %d %s", status, body)
	}
	var result struct {
		Data struct {
			Passed    bool     `json:"passed"`
			Conflicts []string `json:"conflicts"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 该缺陷已于 2026-09-13 修复：有效期按日期口径判定且含当日，
	// 到期日当天仍可用（此前 valid_until >= now 会让到期日 00:00 起即失效，差一天）。
	if !result.Data.Passed {
		t.Fatalf("修复后到期当天仍不可用：conflicts=%v", result.Data.Conflicts)
	}
}

// ---- PM-SLA-05（本次新发现）：状态 SLA 只覆盖「计划完成时间已过」的服务项 ----

func TestArbitrationPMSla05StatusRuleNowCoversFuturePlannedEnd(t *testing.T) {
	env := newArbEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-ARB-FUT', '`+arbTenant+`', '未到期裁决', '客户', 'C-ARB', 'v1', 'NONE', 1, '待实施', NOW(3), NOW(3))`,
		// 停留在「实施中」已 30 天，规则时限 10 小时，但计划完成时间在未来
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, status_changed_at, created_at, updated_at)
		 VALUES ('SI-ARB-FUT', '`+arbTenant+`', 'PJ-ARB-FUT', 'S1', 'r', 'STANDARD', '实施中', 'NONE', 'PASSED', DATE_SUB(NOW(3), INTERVAL 30 DAY), DATE_SUB(NOW(3), INTERVAL 30 DAY), DATE_SUB(NOW(3), INTERVAL 30 DAY))`,
		`UPDATE pm_service_item SET planned_end = DATE_ADD(NOW(3), INTERVAL 30 DAY) WHERE tenant_id='`+arbTenant+`' AND id='SI-ARB-FUT'`,
		`INSERT INTO pm_sla (tenant_id, kind, name, status, deadline_hours, remind_hours, enabled, created_at, updated_at, updated_by)
		 VALUES ('`+arbTenant+`', 'sla', '未来计划规则', '实施中', 10, 5, 1, NOW(3), NOW(3), 'seed')`,
	)
	items := env.slaItems()
	overdue := 0
	for _, item := range items {
		if item.Kind == "STATUS_DEADLINE_OVERDUE" {
			overdue++
		}
	}
	t.Logf("已停留 30 天（时限 10 小时）、计划完成时间在未来 -> SLA 条目 %d 条，其中状态停留超期 %d 条", len(items), overdue)
	// 该缺陷已于 2026-09-13 修复（候选集与状态口径解耦）：计划时间未到但已停留超时的项现在会被判超期。
	if overdue == 0 {
		t.Fatalf("修复后仍未覆盖：计划完成时间未到但状态已停留超时的服务项没有产生超期项")
	}
}

// ---- 按租户隔离的身份切换与请求构造辅助 ----

// tenantIdentity 与 business_flow_e2e_test.go 的 switchIdentity 同构，
// 仅把租户参数化，便于裁决测试使用独立租户。
type tenantIdentity struct{ tenant string }

func switchIdentityFor(tenant string) tenantIdentity { return tenantIdentity{tenant: tenant} }

func (identity tenantIdentity) Authenticate(_ context.Context, request *http.Request) (platform.Principal, error) {
	role := strings.TrimSpace(request.Header.Get("X-E2E-Role"))
	permissions, ok := e2eRolePermissions[role]
	if !ok {
		return platform.Principal{}, platform.ErrUnauthenticated
	}
	granted := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		granted[permission] = true
	}
	return platform.Principal{
		TenantID: identity.tenant, IdentityID: role, UserID: role, DisplayName: role,
		Roles: []string{role}, Permissions: granted,
		DataScopes:            []platform.DataScope{{RoleCode: role, ScopeType: "APPLICATION"}},
		AuthorizationRevision: 1,
		CatalogVersion:        "5",
	}, nil
}

func newRoleRequest(role, method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-E2E-Role", role)
	return request
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }
