package httpapi_test

// 业务不变量回归基线（临时验证用，可在验收后转为正式用例）。
//
// 来源：docs/business-model.md §5 列出的 13 条关键不变量。
// 每条不变量对应一个子测试；断言的是「不变量成立」，因此：
//   PASS = 不变量当前成立（系统行为符合 §6 的裁定）
//   FAIL = 不变量被违反（即 §9.1 中的应修缺陷被复现）
//
// 需要 PM_TEST_DSN；只在隔离租户内读写，各子测试自行清理。

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/httpapi"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"io"
	"log/slog"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const invTenant = "PM-INV-A"
const invTenantB = "PM-INV-B"

// 派生状态允许取值的全集（对应 §5 I1 的 12 个节点）。
var allowedProjectStatuses = map[string]bool{
	"待拆解确认": true, "待分配": true, "待实施": true, "实施准备中": true,
	"实施中": true, "异常处理中": true, "现场实施完成": true, "报告编制": true,
	"已完成": true, "已终止": true, "补充协议处理中": true,
}

type invEnv struct {
	t       *testing.T
	db      *gorm.DB
	handler http.Handler
}

func newInvEnv(t *testing.T) *invEnv {
	t.Helper()
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the invariant checks")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	env := &invEnv{t: t, db: db, handler: httpapi.NewRouter(service, switchIdentityFor(invTenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	env.clean()
	t.Cleanup(env.clean)
	return env
}

func (e *invEnv) clean() {
	for _, statement := range []string{
		`DELETE FROM pm_delivery_event WHERE tenant_id IN ('` + invTenant + `','` + invTenantB + `')`,
		`DELETE FROM pm_capability WHERE tenant_id IN ('` + invTenant + `','` + invTenantB + `')`,
		`DELETE FROM pm_impl_plan WHERE tenant_id IN ('` + invTenant + `','` + invTenantB + `')`,
		`DELETE FROM pm_service_item WHERE tenant_id IN ('` + invTenant + `','` + invTenantB + `')`,
		`DELETE FROM pm_project WHERE tenant_id IN ('` + invTenant + `','` + invTenantB + `')`,
		`DELETE FROM pm_sla WHERE tenant_id IN ('` + invTenant + `','` + invTenantB + `')`,
	} {
		e.db.Exec(statement)
	}
}

func (e *invEnv) exec(statements ...string) {
	e.t.Helper()
	for _, statement := range statements {
		if err := e.db.Exec(statement).Error; err != nil {
			e.t.Fatalf("seed 失败: %v\n%s", err, statement)
		}
	}
}

func (e *invEnv) call(role, method, path, body string) (int, string) {
	request := newRoleRequest(role, method, path, body)
	recorder := newRecorder()
	e.handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// projectStatusOf 读取项目详情中的派生状态与进度。
func (e *invEnv) projectStatusOf(projectID string) (string, int) {
	e.t.Helper()
	status, body := e.call("business_admin", http.MethodGet, "/api/v1/projects/"+projectID, "")
	if status != http.StatusOK {
		e.t.Fatalf("读取项目失败: HTTP %d %s", status, body)
	}
	var detail struct {
		Data struct {
			Status   string `json:"status"`
			Progress int    `json:"progress"`
			Services int    `json:"services"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		e.t.Fatalf("解析项目详情失败: %v (%s)", err, body)
	}
	return detail.Data.Status, detail.Data.Progress
}

func (e *invEnv) seedProject(id, status string, services int) string {
	return `INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
	        VALUES ('` + id + `', '` + invTenant + `', '` + id + `', '客户', 'C-` + id + `', 'v1', 'NONE', ` + itoa(services) + `, '` + status + `', NOW(3), NOW(3))`
}

func (e *invEnv) seedItem(id, projectID, status, reportStatus, extra string) string {
	if extra != "" {
		extra = ", " + extra
	}
	return `INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
	        VALUES ('` + id + `', '` + invTenant + `', '` + projectID + `', 'S1', 'r', 'STANDARD', '` + status + `', '` + reportStatus + `', 'UNCHECKED', NOW(3), NOW(3))`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// ---- I1 + I2：项目状态唯一取值合法，且遵循「最滞后」原则 ----

func TestInvariantI1I2ProjectStatusDerivation(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-LAG", "已完成", 2),
		env.seedItem("SI-INV-LAG-1", "PJ-INV-LAG", "待实施", "NONE", ""),
		env.seedItem("SI-INV-LAG-2", "PJ-INV-LAG", "现场实施完成", "ARCHIVED", ""),
		env.seedProject("PJ-INV-EXC", "已完成", 2),
		env.seedItem("SI-INV-EXC-1", "PJ-INV-EXC", "异常处理中", "NONE", ""),
		env.seedItem("SI-INV-EXC-2", "PJ-INV-EXC", "现场实施完成", "ARCHIVED", ""),
	)
	for projectID, want := range map[string]string{
		"PJ-INV-LAG": "待实施",   // 最滞后项 rank=2
		"PJ-INV-EXC": "异常处理中", // 最滞后项 rank=5
	} {
		got, _ := env.projectStatusOf(projectID)
		if !allowedProjectStatuses[got] {
			t.Fatalf("I1 被违反：项目 %s 的派生状态 %q 不在允许集合内", projectID, got)
		}
		if got != want {
			t.Fatalf("I2 被违反：项目 %s 期望最滞后状态 %q，实际 %q", projectID, want, got)
		}
		t.Logf("  %s -> %s（符合最滞后原则）", projectID, got)
	}
}

// ---- I3：报告阶段严格单向 ----

func TestInvariantI3ReportChainIsMonotonic(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-REP", "现场实施完成", 1),
		env.seedItem("SI-INV-REP", "PJ-INV-REP", "现场实施完成", "ARCHIVED", ""),
	)
	// 已归档后回推 REVIEWED，必须被拒绝
	status, body := env.call("project_manager", http.MethodPost, "/api/v1/service-items/SI-INV-REP/report-status", `{"phase":"REVIEWED"}`)
	t.Logf("  已归档后回退到 REVIEWED -> HTTP %d", status)
	if status >= 200 && status <= 299 {
		t.Fatalf("I3 被违反：报告阶段允许回退（HTTP %d %s）", status, body)
	}
}

// ---- I4：已终止是终态，不可再变更设备 ----

func TestInvariantI4TerminatedIsFinal(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-TERM", "已终止", 1),
		env.seedItem("SI-INV-TERM", "PJ-INV-TERM", "已终止", "NONE", ""),
	)
	status, body := env.call("project_manager", http.MethodPost, "/api/v1/service-items/SI-INV-TERM/equipment-return", `{"resource_id":"EQ-INV"}`)
	t.Logf("  已终止服务项归还设备 -> HTTP %d", status)
	if status >= 200 && status <= 299 {
		t.Fatalf("I4 被违反：已终止服务项仍可归还设备（HTTP %d %s）", status, body)
	}
}

// ---- I5：发布实施计划的前提是能力校验通过 ----

func TestInvariantI5PlanRequiresPassedCapabilityCheck(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-PLAN", "待分配", 1),
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, project_manager_id, team_lead_id, created_at, updated_at)
		 VALUES ('SI-INV-PLAN', '`+invTenant+`', 'PJ-INV-PLAN', 'S1', 'r', 'STANDARD', '待分配', 'NONE', 'CONFLICT', 'PM-INV', 'LEAD-INV', NOW(3), NOW(3))`,
	)
	status, body := env.call("project_manager", http.MethodPost, "/api/v1/service-items/SI-INV-PLAN/implementation-plan",
		`{"planned_start":"2026-10-01T09:00:00Z","planned_end":"2026-10-05T18:00:00Z","site_plan":"计划","personnel":[{"resource_type":"PERSON","resource_id":"E1","window_start":"2026-10-01","window_end":"2026-10-05"}]}`)
	t.Logf("  能力校验未通过时发布计划 -> HTTP %d %s", status, body)
	if status >= 200 && status <= 299 {
		t.Fatalf("I5 被违反：能力校验未通过仍可发布实施计划")
	}
}

// ---- I6：执行分配的前提是已分配团队负责人 ----

func TestInvariantI6ExecutionAssignRequiresTeamLead(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-LEAD", "待分配", 1),
		env.seedItem("SI-INV-LEAD", "PJ-INV-LEAD", "待分配", "NONE", ""),
	)
	status, body := env.call("team_lead", http.MethodPost, "/api/v1/service-items/SI-INV-LEAD/execution-assignment",
		`{"project_manager_id":"PM-INV","engineer_ids":["E-INV"],"required_codes":[]}`)
	t.Logf("  无团队负责人时执行分配 -> HTTP %d %s", status, body)
	if status >= 200 && status <= 299 {
		t.Fatalf("I6 被违反：没有团队负责人仍可完成执行分配")
	}
}

// ---- I7：特殊方法必须先通过技术总监复核才能发布计划 ----

func TestInvariantI7SpecialMethodRequiresReview(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-SPEC", "待分配", 1),
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, project_manager_id, team_lead_id, special, tech_review_status, created_at, updated_at)
		 VALUES ('SI-INV-SPEC', '`+invTenant+`', 'PJ-INV-SPEC', 'S1', 'r', 'PENETRATION', '待分配', 'NONE', 'PASSED', 'PM-INV', 'LEAD-INV', '是', 'PENDING', NOW(3), NOW(3))`,
	)
	status, body := env.call("project_manager", http.MethodPost, "/api/v1/service-items/SI-INV-SPEC/implementation-plan",
		`{"planned_start":"2026-10-01T09:00:00Z","planned_end":"2026-10-05T18:00:00Z","site_plan":"计划","penetration_test_plan":"专项","personnel":[{"resource_type":"PERSON","resource_id":"E1","window_start":"2026-10-01","window_end":"2026-10-05"}]}`)
	t.Logf("  特殊方法未复核即发布计划 -> HTTP %d %s", status, body)
	if status >= 200 && status <= 299 {
		t.Fatalf("I7 被违反：特殊方法未经复核仍可发布实施计划")
	}
}

// ---- I8：同一设备同一时段只能归属一个服务项 ----

func TestInvariantI8EquipmentSingleOccupancy(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-EQ-A", "待分配", 1),
		env.seedProject("PJ-INV-EQ-B", "待分配", 1),
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, project_manager_id, team_lead_id, created_at, updated_at)
		 VALUES ('SI-INV-EQ-A', '`+invTenant+`', 'PJ-INV-EQ-A', 'S1', 'r', 'STANDARD', '待分配', 'NONE', 'PASSED', 'PM-INV', 'LEAD-INV', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, project_manager_id, team_lead_id, created_at, updated_at)
		 VALUES ('SI-INV-EQ-B', '`+invTenant+`', 'PJ-INV-EQ-B', 'S1', 'r', 'STANDARD', '待分配', 'NONE', 'PASSED', 'PM-INV', 'LEAD-INV', NOW(3), NOW(3))`,
		`INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, capability_codes, valid_from, valid_until, status, usage_scope, updated_at, updated_by)
		 VALUES ('CAP-INV-EQ', '`+invTenant+`', 'EQUIPMENT', 'EQ-INV', '走查设备', JSON_ARRAY('TPL'), DATE_SUB(NOW(3), INTERVAL 1 DAY), DATE_ADD(NOW(3), INTERVAL 1 YEAR), 'ACTIVE', 'ANY', NOW(3), 'seed')`,
		`INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, capability_codes, valid_from, valid_until, status, usage_scope, updated_at, updated_by)
		 VALUES ('CAP-INV-EQP', '`+invTenant+`', 'PERSON', 'E-INV', '走查工程师', JSON_ARRAY('TPL'), DATE_SUB(NOW(3), INTERVAL 1 DAY), DATE_ADD(NOW(3), INTERVAL 1 YEAR), 'ACTIVE', 'ANY', NOW(3), 'seed')`,
	)
	// 按真实链路：先发布实施计划（创建计划行），再做实施准备
	plan := func(itemID, start, end string) {
		body := `{"planned_start":"` + start + `","planned_end":"` + end + `","site_plan":"计划",
		          "personnel":[{"resource_type":"PERSON","resource_id":"E-INV","window_start":"` + start[:10] + `","window_end":"` + end[:10] + `"}]}`
		if status, resp := env.call("project_manager", http.MethodPost, "/api/v1/service-items/"+itemID+"/implementation-plan", body); status < 200 || status > 299 {
			t.Fatalf("发布实施计划失败 %s: HTTP %d %s", itemID, status, resp)
		}
	}
	plan("SI-INV-EQ-A", "2026-10-01T09:00:00Z", "2026-10-05T18:00:00Z")
	plan("SI-INV-EQ-B", "2026-10-03T09:00:00Z", "2026-10-08T18:00:00Z")

	bodyA := `{"travel_request_id":"TR-A","notes":"","equipment":[{"resource_type":"EQUIPMENT","resource_id":"EQ-INV","window_start":"2026-10-01","window_end":"2026-10-05"}]}`
	statusA, respA := env.call("project_manager", http.MethodPost, "/api/v1/service-items/SI-INV-EQ-A/preparation", bodyA)
	if statusA < 200 || statusA > 299 {
		t.Fatalf("前置失败：第一条实施准备未成功 HTTP %d %s", statusA, respA)
	}
	// 诊断：确认设备清单确实落库（否则占用检测无意义）
	var stored string
	_ = env.db.Raw(`SELECT COALESCE(equipment,'') FROM pm_impl_plan WHERE tenant_id=? AND service_item_id=?`, invTenant, "SI-INV-EQ-A").Scan(&stored).Error
	t.Logf("  A 的计划行设备快照: %s", stored)
	if status, body := env.call("project_manager", http.MethodGet, "/api/v1/service-items/SI-INV-EQ-B/equipment-reservations", ""); status == http.StatusOK {
		t.Logf("  系统眼中的占用列表: %s", body)
	}
	// 完全重叠（10-03~10-08 与 10-01~10-05）的第二条必须被拒绝
	bodyB := `{"travel_request_id":"TR-B","notes":"","equipment":[{"resource_type":"EQUIPMENT","resource_id":"EQ-INV","window_start":"2026-10-03","window_end":"2026-10-08"}]}`
	statusB, respB := env.call("project_manager", http.MethodPost, "/api/v1/service-items/SI-INV-EQ-B/preparation", bodyB)
	t.Logf("  重叠时段占用同一设备 -> HTTP %d %s", statusB, respB)
	if statusB >= 200 && statusB <= 299 {
		t.Fatalf("I8 被违反：同一设备在重叠时段被两个服务项占用")
	}
}

// ---- I9：人员资质不按日期失效，但仍必须启用、身份有效且已关联平台用户 ----

func TestInvariantI9PersonnelDatesDoNotBlockAssignment(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-EXP", "待分配", 1),
		env.seedItem("SI-INV-EXP", "PJ-INV-EXP", "待分配", "NONE", ""),
		// 保留一条历史上已经过期的日期，验证人员派工不再受该日期限制。
		`INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, user_id, capability_codes, valid_from, valid_until, status, usage_scope, identity_status, updated_at, updated_by)
		 VALUES ('CAP-INV-EXP', '`+invTenant+`', 'PERSON', 'E-INV-EXP', '历史过期工程师', 'E-INV-EXP', JSON_ARRAY('TPL-INV'),
		         DATE_SUB(NOW(3), INTERVAL 60 DAY), DATE_SUB(NOW(3), INTERVAL 1 DAY), 'ACTIVE', 'ANY', 'ACTIVE', NOW(3), 'seed')`,
	)
	if status, body := env.call("business_admin", http.MethodPost, "/api/v1/service-items/SI-INV-EXP/team-assignment",
		`{"team_lead_id":"LEAD-INV"}`); status != http.StatusOK {
		t.Fatalf("前置（分配团队负责人）失败: HTTP %d %s", status, body)
	}
	status, body := env.call("team_lead", http.MethodPost, "/api/v1/service-items/SI-INV-EXP/execution-assignment",
		`{"project_manager_id":"PM-INV","engineer_ids":["E-INV-EXP"],"required_codes":["TPL-INV"]}`)
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
		t.Fatalf("解析失败: %v (%s)", err, body)
	}
	t.Logf("  人员历史有效期不限制派工 -> passed=%v conflicts=%v", result.Data.Passed, result.Data.Conflicts)
	if !result.Data.Passed {
		t.Fatalf("I9 被违反：启用且身份有效的人员被历史有效期拦截: %v", result.Data.Conflicts)
	}
}

// ---- I12：项目 services 计数与实际服务项条数一致 ----

func TestInvariantI12ServiceCountMatchesItems(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-CNT", "待拆解确认", 1),
		env.seedItem("SI-INV-CNT-1", "PJ-INV-CNT", "待确认", "NONE", ""),
	)
	// 通过拆解调整把服务项替换为 2 条
	status, body := env.call("business_admin", http.MethodPost, "/api/v1/projects/PJ-INV-CNT/decomposition-adjustments",
		`{"reason":"走查调整","supplement_contract_id":"SC-INV-1","items":[
		   {"source_id":"ADJ-1","batch":"B1","site":"场所1","category":"类别1","test_mode":"STANDARD"},
		   {"source_id":"ADJ-2","batch":"B2","site":"场所2","category":"类别2","test_mode":"STANDARD"}]}`)
	if status < 200 || status > 299 {
		t.Fatalf("拆解调整失败: HTTP %d %s", status, body)
	}
	var services int
	var items int64
	if err := env.db.Raw(`SELECT services FROM pm_project WHERE tenant_id=? AND id=?`, invTenant, "PJ-INV-CNT").Scan(&services).Error; err != nil {
		t.Fatalf("读取 services 失败: %v", err)
	}
	if err := env.db.Raw(`SELECT COUNT(*) FROM pm_service_item WHERE tenant_id=? AND project_id=?`, invTenant, "PJ-INV-CNT").Scan(&items).Error; err != nil {
		t.Fatalf("统计服务项失败: %v", err)
	}
	t.Logf("  拆解调整后 services=%d，实际服务项=%d", services, items)
	if int64(services) != items {
		t.Fatalf("I12 被违反：services=%d 与实际服务项 %d 不一致", services, items)
	}
}

// ---- I10：派生态不接受客户端写入 ----

func TestInvariantI10DerivedStateNotClientWritable(t *testing.T) {
	env := newInvEnv(t)
	status, body := env.call("business_admin", http.MethodPost, "/api/v1/projects",
		`{"name":"写入试试","customer":"客户","contract":"C-INV-10","contract_id":"C-INV-10","contract_version":"v1","status":"已完成","progress":100,"supplement_status":"REQUIRED"}`)
	t.Logf("  提交带 status=已完成 / progress=100 的创建请求 -> HTTP %d", status)
	if status < 200 || status > 299 {
		t.Fatalf("创建项目失败: HTTP %d %s", status, body)
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v (%s)", err, body)
	}
	if strings.TrimSpace(created.Data.ID) == "" {
		t.Fatalf("创建响应未返回项目 ID: %s", body)
	}
	got, progress := env.projectStatusOf(created.Data.ID)
	t.Logf("  实际落库状态=%s 进度=%d", got, progress)
	if got == "已完成" || progress == 100 {
		t.Fatalf("I10 被违反：客户端传入的派生值被采纳（状态=%s 进度=%d）", got, progress)
	}
	// 补充协议分支也不得由客户端直接指定
	var supplement string
	if err := env.db.Raw(`SELECT supplement_status FROM pm_project WHERE tenant_id=? AND id=?`, invTenant, created.Data.ID).Scan(&supplement).Error; err != nil {
		t.Fatalf("读取 supplement_status 失败: %v", err)
	}
	t.Logf("  supplement_status=%s", supplement)
	if supplement == "REQUIRED" {
		t.Fatalf("I10 被违反：客户端可指定 supplement_status=REQUIRED")
	}
}

// ---- I11：租户隔离 ----

func TestInvariantI11TenantIsolation(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-INV-TEN', '`+invTenant+`', 'A租户项目', '客户', 'C-TEN', 'v1', 'NONE', 1, '待分配', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-INV-TEN', '`+invTenant+`', 'PJ-INV-TEN', 'S1', 'r', 'STANDARD', '待分配', 'NONE', 'UNCHECKED', NOW(3), NOW(3))`,
	)
	// 以另一个租户（B）的身份访问 A 租户的数据
	repository := store.NewRepository(env.db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	otherTenant := httpapi.NewRouter(service, switchIdentityFor(invTenantB), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// 详情接口
	recorder := newRecorder()
	otherTenant.ServeHTTP(recorder, newRoleRequest("business_admin", http.MethodGet, "/api/v1/projects/PJ-INV-TEN", ""))
	t.Logf("  B 租户读取 A 租户项目详情 -> HTTP %d", recorder.Code)
	if recorder.Code == http.StatusOK || strings.Contains(recorder.Body.String(), "A租户项目") {
		t.Fatalf("I11 被违反：跨租户可读取项目详情（HTTP %d %s）", recorder.Code, recorder.Body.String())
	}
	// 列表接口
	recorder = newRecorder()
	otherTenant.ServeHTTP(recorder, newRoleRequest("business_admin", http.MethodGet, "/api/v1/service-items", ""))
	t.Logf("  B 租户列出服务项 -> HTTP %d，响应含 A 租户服务项=%v", recorder.Code, strings.Contains(recorder.Body.String(), "SI-INV-TEN"))
	if strings.Contains(recorder.Body.String(), "SI-INV-TEN") {
		t.Fatalf("I11 被违反：跨租户列表泄露了服务项")
	}
}

// ---- I14：服务项暴露并发版本号，且每次状态变更递增 ----
//
// 事件写路径已在事务内加行锁，单字段覆盖不会发生；这里保证的是"客户端能拿到版本信号"：
// 没有版本号时，后提交者会静默覆盖前者基于旧版本的意图，双方都收到成功。
func TestInvariantI14ServiceItemExposesBumpedVersion(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-VER", "待分配", 1),
		env.seedItem("SI-INV-VER", "PJ-INV-VER", "待分配", "NONE", ""),
	)
	readVersion := func() uint64 {
		status, body := env.call("business_admin", http.MethodGet, "/api/v1/service-items?project_id=PJ-INV-VER", "")
		if status != http.StatusOK {
			t.Fatalf("列出服务项失败: HTTP %d %s", status, body)
		}
		var payload struct {
			Data struct {
				Items []struct {
					ID      string `json:"id"`
					Version uint64 `json:"version"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("解析服务项失败: %v (%s)", err, body)
		}
		for _, item := range payload.Data.Items {
			if item.ID == "SI-INV-VER" {
				return item.Version
			}
		}
		t.Fatalf("响应中没有目标服务项: %s", body)
		return 0
	}

	before := readVersion()
	if before == 0 {
		t.Fatal("I14 被违反：服务项响应必须带 version，客户端才能识别版本陈旧")
	}
	// 一次真实状态变更后版本必须递增。
	if status, body := env.call("business_admin", http.MethodPost, "/api/v1/service-items/SI-INV-VER/team-assignment",
		`{"team_lead_id":"LEAD-INV"}`); status != http.StatusOK {
		t.Fatalf("分配团队负责人失败: HTTP %d %s", status, body)
	}
	after := readVersion()
	t.Logf("  服务项 version: %d -> %d", before, after)
	if after <= before {
		t.Fatalf("I14 被违反：状态变更后 version 未递增（%d -> %d）", before, after)
	}
}

// ---- I15：声明陈旧版本时写入被拒（409），避免静默覆盖他人修改 ----
//
// 服务端已在事务内加行锁并做条件更新，不会产生脏写；本条保证的是"客户端能被告知"：
// 拿着旧版本提交的一方必须收到冲突，而不是覆盖掉对方刚提交的内容。
func TestInvariantI15StaleVersionWriteRejected(t *testing.T) {
	env := newInvEnv(t)
	env.exec(
		env.seedProject("PJ-INV-STALE", "待分配", 1),
		env.seedItem("SI-INV-STALE", "PJ-INV-STALE", "待分配", "NONE", ""),
	)
	readVersion := func() uint64 {
		status, body := env.call("business_admin", http.MethodGet, "/api/v1/service-items?project_id=PJ-INV-STALE", "")
		if status != http.StatusOK {
			t.Fatalf("列出服务项失败: HTTP %d %s", status, body)
		}
		var payload struct {
			Data struct {
				Items []struct {
					ID      string `json:"id"`
					Version uint64 `json:"version"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("解析服务项失败: %v (%s)", err, body)
		}
		for _, item := range payload.Data.Items {
			if item.ID == "SI-INV-STALE" {
				return item.Version
			}
		}
		t.Fatalf("响应中没有目标服务项: %s", body)
		return 0
	}
	stale := readVersion()

	// 先由另一个角色成功写入一次，使版本前进。
	if status, body := env.call("business_admin", http.MethodPost, "/api/v1/service-items/SI-INV-STALE/team-assignment",
		`{"team_lead_id":"LEAD-INV"}`); status != http.StatusOK {
		t.Fatalf("首次分配失败: HTTP %d %s", status, body)
	}
	// 用旧版本再写：必须 409，而不是覆盖。
	status, body := env.call("business_admin", http.MethodPost, "/api/v1/service-items/SI-INV-STALE/team-assignment",
		`{"team_lead_id":"LEAD-OTHER","expected_version":`+itoa(int(stale))+`}`)
	t.Logf("  用陈旧版本(%d)提交 -> HTTP %d %s", stale, status, body)
	if status != http.StatusConflict {
		t.Fatalf("I15 被违反：陈旧版本写入未被拒绝（HTTP %d）", status)
	}
	// 用最新版本提交应当成功。
	current := readVersion()
	if status, body := env.call("business_admin", http.MethodPost, "/api/v1/service-items/SI-INV-STALE/team-assignment",
		`{"team_lead_id":"LEAD-OTHER","expected_version":`+itoa(int(current))+`}`); status != http.StatusOK {
		t.Fatalf("最新版本提交应成功：HTTP %d %s", status, body)
	}
}
