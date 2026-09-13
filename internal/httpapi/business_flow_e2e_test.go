package httpapi_test

// 端到端业务流程走查（临时验证用，可在验收后删除）。
//
// 目的：用真实 MySQL + 真实路由/中间件，按 authz/permission-manifest.json 里的
// **真实角色**逐个切换身份，把「拆解确认 → 任务分配 → 实施计划 → 实施准备 →
// 现场执行 → 报告推进 → 归档」整条链走完，并校验派生状态、进度与 SLA。
//
// 需要 PM_TEST_DSN；未设置时跳过。只在隔离租户 PM-E2E-TENANT 内读写，结束清理。

import (
	"context"
	"encoding/json"
	"fmt"
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

const e2eTenant = "PM-E2E-TENANT"

// 角色 → 权限：严格取自 authz/permission-manifest.json（catalog_version 5）。
// 不得自行发明权限，否则走查结果不代表真实角色能力。
var e2eRolePermissions = map[string][]string{
	"business_admin": {"project.read", "project.create", "service_item.confirm", "project.decomposition.manage", "project.resource.read", "project.team.assign"},
	"team_lead":      {"project.read", "project.resource.read", "project.execution.assign", "project.deviation.review"},
	"project_manager": {"project.read", "project.resource.read", "project.implementation.plan",
		"project.field.complete", "project.report.manage"},
	"engineer":           {"project.read", "project.field.execute", "project.deviation.report"},
	"technical_director": {"project.read", "project.resource.read", "project.deviation.review", "project.special_method.review", "project.report.archive"},
}

// switchIdentity 按请求头 X-E2E-Role 返回对应角色的 Principal，用于在一个路由实例上
// 模拟「不同角色依次点击」。
type switchIdentity struct{}

func (switchIdentity) Authenticate(_ context.Context, request *http.Request) (platform.Principal, error) {
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
		TenantID: e2eTenant, IdentityID: role, UserID: role, DisplayName: role,
		Roles: []string{role}, Permissions: granted,
		DataScopes:            []platform.DataScope{{RoleCode: role, ScopeType: "APPLICATION"}},
		AuthorizationRevision: 1,
		CatalogVersion:        "5",
	}, nil
}

// call 以指定角色发起一次请求，返回状态码与响应体。
func e2eCall(handler http.Handler, role, method, path, body string) (int, string) {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-E2E-Role", role)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// step 执行一步并记录；非 2xx 一律中断，因为流程无法继续。
func e2eStep(t *testing.T, handler http.Handler, role, method, path, body string) string {
	t.Helper()
	status, response := e2eCall(handler, role, method, path, body)
	t.Logf("  [%s] %s %s -> %d", role, method, path, status)
	if len(response) <= 800 {
		t.Logf("      %s", response)
	} else {
		t.Logf("      %s…（截断）", response[:800])
	}
	if status < 200 || status > 299 {
		t.Fatalf("流程在「%s %s」中断（角色 %s）: HTTP %d %s", method, path, role, status, response)
	}
	return response
}

func TestBusinessFlowEndToEndWithRealRoles(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the end-to-end business flow walkthrough")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	cleanup := func() {
		for _, statement := range []string{
			`DELETE FROM pm_delivery_event WHERE tenant_id = '` + e2eTenant + `'`,
			`DELETE FROM pm_capability WHERE tenant_id = '` + e2eTenant + `'`,
			`DELETE FROM pm_impl_plan WHERE tenant_id = '` + e2eTenant + `'`,
			`DELETE FROM pm_service_item WHERE tenant_id = '` + e2eTenant + `'`,
			`DELETE FROM pm_project WHERE tenant_id = '` + e2eTenant + `'`,
			`DELETE FROM pm_sla WHERE tenant_id = '` + e2eTenant + `'`,
		} {
			db.WithContext(ctx).Exec(statement)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// ---- 测试数据：项目 + 1 个服务项 + 人员资质 + 设备 ----
	seed := []string{
		`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		 VALUES ('PJ-E2E-001', '` + e2eTenant + `', '端到端走查项目', '走查客户', 'C-E2E-001', 'v1', 'NONE', 1, '待拆解确认', NOW(3), NOW(3))`,
		`INSERT INTO pm_service_item (id, tenant_id, project_id, batch, site, category, source_service_id, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at)
		 VALUES ('SI-E2E-001', '` + e2eTenant + `', 'PJ-E2E-001', 'B1', '杭州机房', '等级保护', 'S1', '走查服务项', 'STANDARD', '待确认', 'NONE', 'UNCHECKED', NOW(3), NOW(3))`,
		`INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, capability_codes, valid_from, valid_until, status, usage_scope, updated_at, updated_by)
		 VALUES ('CAP-E2E-LEAD', '` + e2eTenant + `', 'PERSON', 'PM-E2E-LEAD', '走查团队负责人', JSON_ARRAY('TPL-E2E'), DATE_SUB(NOW(3), INTERVAL 1 DAY), DATE_ADD(NOW(3), INTERVAL 1 YEAR), 'ACTIVE', 'ANY', NOW(3), 'seed')`,
		`INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, capability_codes, valid_from, valid_until, status, usage_scope, updated_at, updated_by)
		 VALUES ('CAP-E2E-PM', '` + e2eTenant + `', 'PERSON', 'PM-E2E-PM', '走查项目经理', JSON_ARRAY('TPL-E2E'), DATE_SUB(NOW(3), INTERVAL 1 DAY), DATE_ADD(NOW(3), INTERVAL 1 YEAR), 'ACTIVE', 'ANY', NOW(3), 'seed')`,
		`INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, capability_codes, valid_from, valid_until, status, usage_scope, updated_at, updated_by)
		 VALUES ('CAP-E2E-ENG', '` + e2eTenant + `', 'PERSON', 'PM-E2E-ENG', '走查工程师', JSON_ARRAY('TPL-E2E'), DATE_SUB(NOW(3), INTERVAL 1 DAY), DATE_ADD(NOW(3), INTERVAL 1 YEAR), 'ACTIVE', 'ANY', NOW(3), 'seed')`,
		`INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, capability_codes, valid_from, valid_until, status, usage_scope, updated_at, updated_by)
		 VALUES ('CAP-E2E-EQ', '` + e2eTenant + `', 'EQUIPMENT', 'EQ-E2E-001', '走查设备', JSON_ARRAY('TPL-E2E'), DATE_SUB(NOW(3), INTERVAL 1 DAY), DATE_ADD(NOW(3), INTERVAL 1 YEAR), 'ACTIVE', 'ANY', NOW(3), 'seed')`,
	}
	for _, statement := range seed {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			t.Fatalf("seed 失败: %v", err)
		}
	}

	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := httpapi.NewRouter(service, switchIdentity{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	item := "/api/v1/service-items/SI-E2E-001"
	project := "/api/v1/projects/PJ-E2E-001"

	// ---- 步骤 1：业务管理员确认拆解（service_item.confirm 仅 business_admin 持有）----
	t.Log("步骤 1：业务管理员确认拆解")
	e2eStep(t, handler, "business_admin", http.MethodPost, "/api/v1/service-items/confirm",
		`{"ids":["SI-E2E-001"]}`)

	// ---- 步骤 2：业务管理员分配团队负责人 ----
	t.Log("步骤 2：业务管理员分配团队负责人")
	e2eStep(t, handler, "business_admin", http.MethodPost, item+"/team-assignment",
		`{"team_lead_id":"PM-E2E-LEAD"}`)

	// ---- 步骤 3：团队负责人分配项目经理与工程师（触发能力校验）----
	t.Log("步骤 3：团队负责人分配项目经理与工程师")
	e2eStep(t, handler, "team_lead", http.MethodPost, item+"/execution-assignment",
		`{"project_manager_id":"PM-E2E-PM","engineer_ids":["PM-E2E-ENG"],"required_codes":["TPL-E2E"]}`)

	// ---- 步骤 4：项目经理发布实施计划（含实施人员清单）----
	t.Log("步骤 4：项目经理发布实施计划")
	e2eStep(t, handler, "project_manager", http.MethodPost, item+"/implementation-plan",
		`{"planned_start":"2026-09-20T09:00:00Z","planned_end":"2026-09-25T18:00:00Z","site_plan":"现场实施步骤",
		  "personnel":[{"resource_type":"PERSON","resource_id":"PM-E2E-ENG","window_start":"2026-09-20","window_end":"2026-09-25","note":"走查"}]}`)

	// ---- 步骤 5：项目经理发起实施准备（含设备清单）----
	t.Log("步骤 5：项目经理发起实施准备")
	e2eStep(t, handler, "project_manager", http.MethodPost, item+"/preparation",
		`{"travel_request_id":"TR-E2E-001","notes":"走查",
		  "equipment":[{"resource_type":"EQUIPMENT","resource_id":"EQ-E2E-001","window_start":"2026-09-20","window_end":"2026-09-25","note":"走查"}]}`)

	// ---- 步骤 6：工程师提交现场记录 ----
	t.Log("步骤 6：工程师提交现场记录")
	e2eStep(t, handler, "engineer", http.MethodPost, item+"/field-records",
		`{"raw_data":"{\"result\":\"pass\"}","environment":"现场","evidence_urls":[]}`)

	// ---- 步骤 7：项目经理确认现场实施完成 ----
	t.Log("步骤 7：项目经理确认现场实施完成")
	e2eStep(t, handler, "project_manager", http.MethodPost, item+"/field-complete", "")

	// ---- 步骤 8：报告阶段推进（project.report.manage）----
	t.Log("步骤 8：项目经理推进报告阶段（现场完成已自动置 COMPILING）→ REVIEWED → ISSUED")
	for _, phase := range []string{"REVIEWED", "ISSUED"} {
		e2eStep(t, handler, "project_manager", http.MethodPost, item+"/report-status", fmt.Sprintf(`{"phase":%q}`, phase))
	}

	// ---- 步骤 9：技术总监归档（project.report.archive）----
	t.Log("步骤 9：技术总监归档报告")
	e2eStep(t, handler, "technical_director", http.MethodPost, item+"/report-status", `{"phase":"ARCHIVED"}`)

	// ---- 步骤 10：校验派生状态与进度 ----
	t.Log("步骤 10：校验派生项目状态与进度")
	body := e2eStep(t, handler, "business_admin", http.MethodGet, project, "")
	var detail struct {
		Data struct {
			Status   string `json:"status"`
			Progress int    `json:"progress"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatalf("解析项目详情失败: %v (%s)", err, body)
	}
	if detail.Data.Status != "已完成" {
		t.Fatalf("流程走完后项目状态应为「已完成」，实际为 %q（进度 %d）", detail.Data.Status, detail.Data.Progress)
	}
	t.Logf("  派生结果：状态=%s 进度=%d", detail.Data.Status, detail.Data.Progress)

	// ---- 步骤 11：SLA 口径（配置规则后观察状态停留时长）----
	t.Log("步骤 11：配置 SLA 规则后校验状态停留口径")
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO pm_sla (tenant_id, kind, name, status, deadline_hours, remind_hours, enabled, created_at, updated_at, updated_by)
		 VALUES ('` + e2eTenant + `', 'sla', '走查规则', '实施中', 10, 5, 1, NOW(3), NOW(3), 'seed')`).Error; err != nil {
		t.Fatalf("插入 SLA 规则失败: %v", err)
	}
	// 让服务项回到「实施中」并立即查询：刚进入该状态，既不应超期也不应临近。
	if err := db.WithContext(ctx).Exec(
		`UPDATE pm_service_item SET status='实施中', status_changed_at=NOW(3), updated_at=NOW(3), planned_end=DATE_SUB(NOW(3), INTERVAL 1 DAY) WHERE tenant_id='` + e2eTenant + `' AND id='SI-E2E-001'`).Error; err != nil {
		t.Fatalf("重置服务项状态失败: %v", err)
	}
	slaBody := e2eStep(t, handler, "business_admin", http.MethodGet, "/api/v1/delivery/sla-overdue", "")
	var sla struct {
		Data []struct {
			Kind         string `json:"kind"`
			OverdueHours int64  `json:"overdue_hours"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(slaBody), &sla); err != nil {
		t.Fatalf("解析 SLA 响应失败: %v (%s)", err, slaBody)
	}
	t.Logf("  刚进入状态即返回 %d 条 SLA 项", len(sla.Data))
	for _, entry := range sla.Data {
		t.Logf("    kind=%s overdue_hours=%d", entry.Kind, entry.OverdueHours)
	}
	// 期望：刚进入「实施中」（<10 小时），不应出现任何超期项。
	for _, entry := range sla.Data {
		if entry.Kind == "STATUS_DEADLINE_OVERDUE" {
			t.Errorf("SLA 口径错误：服务项刚进入「实施中」，却被判定为状态停留超期 %d 小时", entry.OverdueHours)
		}
	}
}
