package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

type identity struct {
	p   platform.Principal
	err error
}

type integrationVerifier struct {
	err      error
	token    string
	identity platform.ServiceTokenIdentity
}

type approvedContractVerifier struct{}

func (approvedContractVerifier) Get(_ context.Context, id string) (platform.ApprovedContract, error) {
	return platform.ApprovedContract{ID: id, Number: "HT-1", CustomerName: "示例客户", Version: 1, Status: "approved", ApprovalPassed: true}, nil
}

func (v *integrationVerifier) VerifyClientCredentials(_ context.Context, token string) (platform.ServiceTokenIdentity, error) {
	v.token = token
	if v.identity.TenantID == "" {
		v.identity = platform.ServiceTokenIdentity{TenantID: "tenant-contract", ApplicationCode: "contract_management", EnvironmentCode: "test"}
	}
	return v.identity, v.err
}

func (i identity) Authenticate(context.Context, *http.Request) (platform.Principal, error) {
	return i.p, i.err
}

type repo struct {
	projects       []domain.Project
	items          []domain.ServiceItem
	rules          []domain.Rule
	events         []domain.DeliveryEvent
	capabilities   []domain.Capability
	reservations   []domain.EquipmentReservation
	dashboard      domain.Dashboard
	dashboardScope platform.ScopeFilter
}

func (r *repo) FindProjectByContractVersion(_ context.Context, filter platform.ScopeFilter, contract, version string) (domain.Project, error) {
	for _, p := range r.projects {
		if p.TenantID == filter.TenantID && p.Contract == contract && p.ContractVersion == version {
			return p, nil
		}
	}
	return domain.Project{}, application.ErrNotFound
}
func (r *repo) ActivateContract(_ context.Context, p domain.Project, items []domain.ServiceItem, event domain.DeliveryEvent) error {
	r.projects = append(r.projects, p)
	r.items = append(r.items, items...)
	r.events = append(r.events, event)
	return nil
}
func (r *repo) SyncContractStampStatus(_ context.Context, p domain.Project, uploaded bool, event domain.DeliveryEvent) error {
	for index := range r.projects {
		if r.projects[index].ID == p.ID {
			r.projects[index].ContractVersion = p.ContractVersion
		}
	}
	r.events = append(r.events, event)
	return nil
}
func (r *repo) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.events = append(r.events, event)
	return nil
}
func (r *repo) ListSlaOverdue(context.Context, platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	return []domain.SlaOverdueItem{}, nil
}
func (r *repo) ListDeliveryEvents(_ context.Context, _ platform.ScopeFilter, project string) ([]domain.DeliveryEvent, error) {
	return r.events, nil
}
func (r *repo) FindProjectForDeviation(_ context.Context, _ platform.ScopeFilter, _ string) (string, string, error) {
	if len(r.projects) == 0 {
		return "", "", application.ErrNotFound
	}
	return r.projects[0].ID, "", nil
}
func (r *repo) UpsertCapability(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	r.capabilities = append(r.capabilities, item)
	return item, nil
}
func (r *repo) DeleteEquipment(_ context.Context, _ string, resourceID string) error {
	for index, item := range r.capabilities {
		if item.ResourceType == "EQUIPMENT" && item.ResourceID == resourceID {
			r.capabilities = append(r.capabilities[:index], r.capabilities[index+1:]...)
			return nil
		}
	}
	return application.ErrNotFound
}
func (r *repo) UpdateCapabilityIdentities(context.Context, string, map[string]string, time.Time) error {
	return nil
}

func (r *repo) ListCapabilities(_ context.Context, tenant, typ string) ([]domain.Capability, error) {
	return r.capabilities, nil
}
func (r *repo) ListEquipmentReservations(_ context.Context, tenant, exclude string) ([]domain.EquipmentReservation, error) {
	return r.reservations, nil
}
func (r *repo) FindCapabilities(_ context.Context, tenant, at string, ids []string) ([]domain.Capability, error) {
	return r.capabilities, nil
}

func (r *repo) ListProjects(context.Context, platform.ScopeFilter, string, string) ([]domain.Project, error) {
	return r.projects, nil
}
func (r *repo) GetProject(_ context.Context, _ platform.ScopeFilter, id string) (domain.Project, error) {
	for _, p := range r.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.Project{}, application.ErrNotFound
}
func (r *repo) CreateProject(_ context.Context, p domain.Project) error {
	r.projects = append(r.projects, p)
	return nil
}
func (r *repo) CreateProjectWithServiceItems(_ context.Context, p domain.Project, items []domain.ServiceItem) error {
	r.projects = append(r.projects, p)
	r.items = append(r.items, items...)
	return nil
}
func (r *repo) ListServiceItems(context.Context, platform.ScopeFilter, string) ([]domain.ServiceItem, error) {
	return r.items, nil
}
func (r *repo) GetServiceItem(_ context.Context, _ platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	for _, item := range r.items {
		if item.ID == id {
			return item, nil
		}
	}
	return domain.ServiceItem{}, application.ErrNotFound
}
func (r *repo) ConfirmServiceItems(_ context.Context, _ platform.ScopeFilter, ids []string, _ string) ([]domain.ServiceItem, error) {
	return r.items, nil
}
func (r *repo) ListRules(context.Context, string, string) ([]domain.Rule, error) { return r.rules, nil }
func (r *repo) CreateRule(_ context.Context, item domain.Rule) (domain.Rule, error) {
	item.ID = 1
	r.rules = append(r.rules, item)
	return item, nil
}
func (r *repo) UpdateRule(_ context.Context, _ string, _ string, id int64, item domain.Rule) (domain.Rule, error) {
	item.ID = id
	return item, nil
}
func (r *repo) SetRuleEnabled(_ context.Context, _ string, _ string, id int64, enabled bool, _ string) (domain.Rule, error) {
	return domain.Rule{ID: id, Enabled: enabled}, nil
}

// DeleteRule 按 kind + 租户 + 主键删除：类型不匹配时视为不存在，与真实仓储一致
// （六套配置表各自成表，主键在不同表里重复）。
func (r *repo) DeleteRule(_ context.Context, _ string, kind string, id int64) (domain.Rule, error) {
	for index, rule := range r.rules {
		if rule.ID == id && (rule.Kind == kind || rule.Kind == "") {
			r.rules = append(append([]domain.Rule{}, r.rules[:index]...), r.rules[index+1:]...)
			return rule, nil
		}
	}
	return domain.Rule{}, application.ErrNotFound
}
func (r *repo) Dashboard(_ context.Context, filter platform.ScopeFilter) (domain.Dashboard, error) {
	r.dashboardScope = filter
	if r.dashboard.StatusCounts == nil {
		r.dashboard.ProjectCount = len(r.projects)
		r.dashboard.StatusCounts = map[string]int{}
	}
	return r.dashboard, nil
}

type audit struct{ events []platform.AuditEvent }

func (a *audit) Report(_ context.Context, e platform.AuditEvent) error {
	a.events = append(a.events, e)
	return nil
}

func router(t *testing.T, permissions map[string]bool, reporter platform.AuditReporter) http.Handler {
	t.Helper()
	return routerWithItems(t, permissions, reporter, []domain.ServiceItem{{ID: "SI-1", Status: "待分配"}})
}

func routerWithItems(t *testing.T, permissions map[string]bool, reporter platform.AuditReporter, items []domain.ServiceItem) http.Handler {
	t.Helper()
	repository := &repo{items: items}
	service := &application.Service{Repo: repository, Contracts: approvedContractVerifier{}}
	id := identity{p: platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", DisplayName: "测试用户", Roles: []string{"admin"}, Permissions: permissions, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}}
	return httpapi.NewRouter(service, id, reporter, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestAssignmentRevocationRequiresPermissionAndReason(t *testing.T) {
	items := []domain.ServiceItem{{ID: "SI-1", Status: "待分配", TeamLeadID: "lead-1", ProjectManagerID: "manager-1", EngineerIDs: []string{"engineer-1"}}}
	denied := perform(routerWithItems(t, map[string]bool{}, nil, items), http.MethodPost, "/api/v1/service-items/SI-1/team-assignment/revoke", `{"reason":"调整负责人"}`)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("missing revoke permission status=%d body=%s", denied.Code, denied.Body.String())
	}

	handler := routerWithItems(t, map[string]bool{"project.team.revoke": true, "project.execution.revoke": true}, nil, items)
	missingReason := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/team-assignment/revoke", `{}`)
	if missingReason.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing reason status=%d body=%s", missingReason.Code, missingReason.Body.String())
	}
	team := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/team-assignment/revoke", `{"reason":"负责人分配有误","expected_version":0}`)
	if team.Code != http.StatusOK {
		t.Fatalf("team revoke status=%d body=%s", team.Code, team.Body.String())
	}
	execution := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/execution-assignment/revoke", `{"reason":"执行团队需重排"}`)
	if execution.Code != http.StatusOK {
		t.Fatalf("execution revoke status=%d body=%s", execution.Code, execution.Body.String())
	}
}

func TestReturnToDecompositionRequiresPermissionReasonAndPendingAllocation(t *testing.T) {
	items := []domain.ServiceItem{{ID: "SI-1", Status: "待分配", Version: 3, TeamLeadID: "lead-1", ProjectManagerID: "manager-1", EngineerIDs: []string{"engineer-1"}}}
	path := "/api/v1/service-items/SI-1/decomposition-return"
	denied := perform(routerWithItems(t, map[string]bool{}, nil, items), http.MethodPost, path, `{"reason":"重新确认范围"}`)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("missing decomposition permission status=%d body=%s", denied.Code, denied.Body.String())
	}

	handler := routerWithItems(t, map[string]bool{"project.decomposition.manage": true}, nil, items)
	missingReason := perform(handler, http.MethodPost, path, `{}`)
	if missingReason.Code != http.StatusUnprocessableEntity || !strings.Contains(missingReason.Body.String(), "请填写退回拆解确认的原因") {
		t.Fatalf("missing reason status=%d body=%s", missingReason.Code, missingReason.Body.String())
	}
	returned := perform(handler, http.MethodPost, path, `{"reason":"重新确认范围","expected_version":3}`)
	if returned.Code != http.StatusOK || !strings.Contains(returned.Body.String(), application.EventDecompositionReturned) {
		t.Fatalf("return status=%d body=%s", returned.Code, returned.Body.String())
	}

	invalid := []domain.ServiceItem{{ID: "SI-1", Status: "待实施", Version: 3}}
	blocked := perform(routerWithItems(t, map[string]bool{"project.decomposition.manage": true}, nil, invalid), http.MethodPost, path, `{"reason":"错误回退","expected_version":3}`)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "仅可将待分配服务项退回拆解确认") {
		t.Fatalf("invalid state status=%d body=%s", blocked.Code, blocked.Body.String())
	}
}

func perform(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestProjectCreationAndRead(t *testing.T) {
	handler := router(t, map[string]bool{"project.create": true, "project.read": true}, nil)
	response := perform(handler, http.MethodPost, "/api/v1/projects", `{"name":"新项目","customer":"示例客户","contract":"HT-1","contract_id":"approved-1","service_items":[{"site":"杭州机房"}]}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Data domain.Project `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.ID == "" {
		t.Fatal("project id missing")
	}
	response = perform(handler, http.MethodGet, "/api/v1/projects", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
}

// 字段级权限的角色下拉只能选目录内的角色：接口必须下发全部应用角色（含中文展示名），
// 并且不依赖当前主体自己的角色 —— 配置者通常不是被配置的那个人。
func TestRoleCatalogExposesEveryApplicationRole(t *testing.T) {
	response := perform(router(t, map[string]bool{"project.read": true}, nil), http.MethodGet, "/api/v1/role-catalog", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			Roles []struct {
				Code string `json:"code"`
				Name string `json:"name"`
			} `json:"roles"`
			CatalogVersion string `json:"catalog_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, response.Body.String())
	}
	if len(body.Data.Roles) < 10 {
		t.Fatalf("role count = %d, want the full application catalog", len(body.Data.Roles))
	}
	// /api/v1/navigation 返回的是「当前主体的角色」，两者不能混用：这里必须是全量目录。
	if len(body.Data.Roles) == 1 {
		t.Fatalf("role catalog collapsed to the caller role: %s", response.Body.String())
	}
	codes := map[string]string{}
	for _, role := range body.Data.Roles {
		if role.Code == "" || role.Name == "" {
			t.Fatalf("role entry is incomplete: %+v", role)
		}
		if _, duplicate := codes[role.Code]; duplicate {
			t.Fatalf("duplicate role %q", role.Code)
		}
		codes[role.Code] = role.Name
	}
	for _, code := range []string{"admin", "system_admin", "business_admin", "team_lead", "technical_director", "project_manager", "engineer", "penetration_engineer", "quality_manager", "device_admin"} {
		if codes[code] == "" {
			t.Fatalf("role %q missing from catalog: %s", code, response.Body.String())
		}
	}
	if codes["admin"] != "项目系统管理员" {
		t.Fatalf("admin name = %q, want 项目系统管理员", codes["admin"])
	}
}

// 没有 project.read 的主体不得读角色目录（与 /navigation、/rules 的准入条件一致）。
func TestRoleCatalogRequiresProjectRead(t *testing.T) {
	response := perform(router(t, map[string]bool{}, nil), http.MethodGet, "/api/v1/role-catalog", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// ruleRouter 构造带预置规则的处理器：删除用例需要同时断言响应与仓储中的剩余行。
func ruleRouter(t *testing.T, permissions map[string]bool, rules []domain.Rule) (http.Handler, *repo) {
	t.Helper()
	repository := &repo{rules: rules}
	service := &application.Service{Repo: repository}
	id := identity{p: platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", DisplayName: "测试用户", Roles: []string{"admin"}, Permissions: permissions, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-1"}}, AuthorizationRevision: 1, CatalogVersion: "2"}}
	return httpapi.NewRouter(service, id, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), repository
}

// 规则配置必须可删除：删除按 kind 定位配置表，租户 + 主键限定，删掉的规则不再出现在列表里。
func TestDeleteRuleRemovesConfiguration(t *testing.T) {
	rules := []domain.Rule{
		{ID: 3, Kind: "split-rules", Name: "走查拆解规则", Scope: "浙江", Enabled: true},
		{ID: 3, Kind: "sla", Name: "走查 SLA", Status: "实施中", Enabled: true},
	}
	handler, repository := ruleRouter(t, map[string]bool{"project_rule.manage": true, "project.read": true}, rules)

	response := perform(handler, http.MethodDelete, "/api/v1/rules/3?kind=split-rules", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, response.Body.String())
	}
	// 返回被删除的那一条：前端据此提示名称，测试据此确认删的是目标行而不是同 ID 的其它类型。
	if payload.Data.ID != 3 || payload.Data.Kind != "split-rules" || payload.Data.Name != "走查拆解规则" {
		t.Fatalf("删除响应不回显被删规则: %s", response.Body.String())
	}
	if len(repository.rules) != 1 || repository.rules[0].Kind != "sla" {
		t.Fatalf("应只删掉 split-rules 那条，剩余 %+v", repository.rules)
	}

	response = perform(handler, http.MethodGet, "/api/v1/rules?kind=split-rules", "")
	if strings.Contains(response.Body.String(), "走查拆解规则") {
		t.Fatalf("删除后列表仍返回该规则: %s", response.Body.String())
	}
	response = perform(handler, http.MethodGet, "/api/v1/rules?kind=sla", "")
	if !strings.Contains(response.Body.String(), "走查 SLA") {
		t.Fatalf("其它类型的同 ID 规则被误删: %s", response.Body.String())
	}
}

func TestDeleteRuleRejectsUnknownAndUnauthorized(t *testing.T) {
	rules := []domain.Rule{{ID: 3, Kind: "split-rules", Name: "走查拆解规则"}}
	handler, _ := ruleRouter(t, map[string]bool{"project_rule.manage": true}, rules)

	// 重复删除：必须 404，而不是把已经不存在的行报成成功。
	perform(handler, http.MethodDelete, "/api/v1/rules/3?kind=split-rules", "")
	response := perform(handler, http.MethodDelete, "/api/v1/rules/3?kind=split-rules", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("重复删除 status=%d body=%s", response.Code, response.Body.String())
	}
	// 类型不匹配：同 ID 命中别的表时不能删（六套配置表主键各自自增）。
	response = perform(handler, http.MethodDelete, "/api/v1/rules/3?kind=sla", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("类型不匹配 status=%d body=%s", response.Code, response.Body.String())
	}
	// 缺少 kind：删除必须显式给出类型，不能猜默认表。
	response = perform(handler, http.MethodDelete, "/api/v1/rules/3", "")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺少 kind status=%d body=%s", response.Code, response.Body.String())
	}
	// 非法编号。
	response = perform(handler, http.MethodDelete, "/api/v1/rules/abc?kind=sla", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("非法编号 status=%d body=%s", response.Code, response.Body.String())
	}
	// 无规则管理权限：路由层直接拒绝。
	blind := router(t, map[string]bool{"project.read": true}, nil)
	response = perform(blind, http.MethodDelete, "/api/v1/rules/3?kind=split-rules", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("越权删除 status=%d body=%s", response.Code, response.Body.String())
	}
	// 只有 project_rule.manage 的角色不能删除字段级权限规则（应用层按 kind 判权）。
	fieldPermission := router(t, map[string]bool{"project_rule.manage": true}, nil)
	response = perform(fieldPermission, http.MethodDelete, "/api/v1/rules/3?kind=permissions", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("字段级权限删除 status=%d body=%s", response.Code, response.Body.String())
	}
}

// 权限矩阵与栏目可见性必须对齐，三类缺口都会变成线上问题：
// ①孤儿模块：某个写模块没有任何持有对应权限的角色能看到它（能力无处使用）；
// ②孤儿权限：某个角色持有权限，但它把守的模块一个都看不到；
// ③能看不能做：角色能看到某写模块，却不持有该模块写操作的任何权限（能填不能提交）。
func TestRoleNavigationAlignsWithPermissionMatrix(t *testing.T) {
	// 模块 → 写操作权限：一个权限可以对应多个模块（project.resource.manage 同时把守
	// 资质与能力、站点档案），但每个模块都必须至少有一个持有者能看到。
	modules := map[string][]string{
		"split-rules": {"project_rule.manage"}, "warning-rules": {"project_rule.manage"},
		"automations": {"project_rule.manage"}, "sla": {"project_rule.manage"},
		"standards": {"project_rule.manage"}, "capability-codes": {"project_rule.manage"}, "permissions": {"project.field_permission.manage"},
		"qualifications": {"project.resource.manage"}, "sites": {"project.resource.manage"},
		"decomposition":  {"service_item.confirm", "project.decomposition.manage"},
		"planning":       {"project.implementation.plan"},
		"preparation":    {"project.implementation.plan"},
		"methods":        {"project.special_method.review"},
		"reports":        {"project.report.manage", "project.report.archive"},
		"allocation":     {"project.team.assign", "project.execution.assign"},
		"assignments":    {"project.execution.assign"},
		"inbox":          {"project.execution.assign"},
		"equipment":      {"project.device.manage"},
		"implementation": {"project.field.execute", "project.field.complete"},
		"exceptions":     {"project.deviation.report", "project.deviation.review"},
	}
	// 只读入口不算缺口：能看到这些模块但不持有写权限是允许的（看板、列表、评估视图）。
	readOnlyAllowed := map[string]bool{
		"qualifications": true, "sites": true, "equipment": true, "implementation": true,
		"standards": true, "reports": true, "decomposition": true, "exceptions": true,
		"inbox": true, "allocation": true, "assignments": true, "planning": true, "preparation": true,
		"methods": true,
	}
	rolePermissions := map[string][]string{
		"admin":                {"project_rule.manage", "project.field_permission.manage", "project.resource.manage", "service_item.confirm", "project.decomposition.manage", "project.implementation.plan", "project.special_method.review", "project.report.manage", "project.report.archive", "project.team.assign", "project.execution.assign", "project.field.execute", "project.field.complete", "project.device.manage", "project.resource.read", "project.device.read", "project.deviation.report", "project.deviation.review"},
		"system_admin":         {"project_rule.manage", "project.field_permission.manage", "project.resource.manage", "service_item.confirm", "project.decomposition.manage", "project.implementation.plan", "project.special_method.review", "project.report.manage", "project.report.archive", "project.team.assign", "project.execution.assign", "project.field.execute", "project.field.complete", "project.device.manage", "project.resource.read", "project.device.read", "project.deviation.report", "project.deviation.review"},
		"business_admin":       {"service_item.confirm", "project.decomposition.manage", "project.team.assign", "project.resource.read"},
		"team_lead":            {"project.execution.assign", "project.deviation.review", "project.resource.read"},
		"technical_director":   {"project.special_method.review", "project.report.archive", "project.deviation.review", "project.resource.read"},
		"project_manager":      {"project.implementation.plan", "project.report.manage", "project.field.complete", "project.resource.read"},
		"quality_manager":      {"project_rule.manage", "project.report.manage", "project.report.archive", "project.resource.manage", "project.resource.read"},
		"device_admin":         {"project.device.manage", "project.device.read", "project.resource.manage", "project.resource.read"},
		"engineer":             {"project.field.execute", "project.deviation.report"},
		"penetration_engineer": {"project.field.execute", "project.deviation.report"},
	}
	visible := map[string]map[string]bool{}
	for role := range rolePermissions {
		body := navigationBodyForRole(t, role)
		visible[role] = map[string]bool{}
		for section := range modules {
			if strings.Contains(body, `"`+section+`"`) {
				visible[role][section] = true
			}
		}
	}
	holds := func(role, permission string) bool {
		for _, held := range rolePermissions[role] {
			if held == permission {
				return true
			}
		}
		return false
	}
	// ① 每个写模块至少有一个持有者能看到（孤儿模块会永远无法维护）。
	for section, permissions := range modules {
		readable := false
		for role := range rolePermissions {
			if !visible[role][section] {
				continue
			}
			for _, permission := range permissions {
				if holds(role, permission) {
					readable = true
				}
			}
		}
		if !readable {
			t.Fatalf("模块 %s 没有任何持有者能看到，能力无处使用（权限：%v）", section, permissions)
		}
	}
	// ② 每个权限都必须至少对应一个该角色看得到的模块（否则权限白给）。
	// 只读目录权限（*.read）不参与：它们是人员/资质/设备目录的数据源，服务于其它模块的
	// 表单渲染，本身不构成某个模块的入口。
	for role, permissions := range rolePermissions {
		for _, permission := range permissions {
			if strings.HasSuffix(permission, ".read") {
				continue
			}
			entry := false
			for section, sectionPermissions := range modules {
				for _, candidate := range sectionPermissions {
					if candidate == permission && visible[role][section] {
						entry = true
					}
				}
			}
			if !entry && permission != "project.read" {
				t.Fatalf("角色 %s 持有 %s，但对应模块一个都看不到", role, permission)
			}
		}
	}
	// ③ 能看到写模块的角色必须持有该模块写操作的某个权限；只读视图（看板/列表/评估）
	// 除外——实施看板对团队负责人就是只读的，规划表单却不是。
	for role := range rolePermissions {
		for section, permissions := range modules {
			if !visible[role][section] || readOnlyAllowed[section] {
				continue
			}
			allowed := false
			for _, permission := range permissions {
				if holds(role, permission) {
					allowed = true
				}
			}
			if !allowed {
				t.Fatalf("角色 %s 能看到 %s，却不持有其写操作权限 %v（会出现能填不能提交）", role, section, permissions)
			}
		}
		// 规划/准备是录入表单：看不到提交按钮的填充体验即为此类缺陷，专门兜一条硬约束。
		for _, section := range []string{"planning", "preparation"} {
			if visible[role][section] && !holds(role, "project.implementation.plan") {
				t.Fatalf("角色 %s 能看到 %s 却没有 project.implementation.plan", role, section)
			}
		}
	}
}

// 库结构落后于代码时必须判为 not_ready：否则带着缺失的列对外服务，任何写路径都只会
// 返回 500「服务暂不可用」，把"部署时漏跑迁移"伪装成业务故障。
func TestReadyzFailsFastWhenMigrationsPending(t *testing.T) {
	service := &application.Service{Repo: &repo{}}
	id := identity{p: platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-1"}}, AuthorizationRevision: 1}}
	handler := httpapi.NewRouter(service, id, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		PendingMigrations: []string{"000017_split_rule_config_v2.sql"},
	})
	response := perform(handler, http.MethodGet, "/readyz", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("待执行迁移时应 503，实际 %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "migrations_pending") || !strings.Contains(response.Body.String(), "000017_split_rule_config_v2.sql") {
		t.Fatalf("就绪检查应显式说明待执行迁移：%s", response.Body.String())
	}
	// 存活检查保持 200，并把待迁移数量带出来供巡检发现。
	response = perform(handler, http.MethodGet, "/healthz", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "pending_migrations") {
		t.Fatalf("健康检查应 200 并带出待迁移数量：%d %s", response.Code, response.Body.String())
	}
	// 结构就绪时恢复为 ready。
	ready := httpapi.NewRouter(service, id, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response = perform(ready, http.MethodGet, "/readyz", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "\"ready\"") {
		t.Fatalf("结构就绪时应 ready：%d %s", response.Code, response.Body.String())
	}
}

func navigationBodyForRole(t *testing.T, role string) string {
	t.Helper()
	repository := &repo{}
	service := &application.Service{Repo: repository}
	id := identity{p: platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Roles: []string{role}, Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: role, ScopeType: "APPLICATION"}}}}
	response := perform(httpapi.NewRouter(service, id, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), http.MethodGet, "/api/v1/navigation", "")
	if response.Code != http.StatusOK {
		t.Fatalf("role %s status=%d body=%s", role, response.Code, response.Body.String())
	}
	return response.Body.String()
}

// 超级管理员（项目系统管理员 / 平台系统管理员）持有全部应用权限，必须看到所有功能模块；
// 业务角色仍然只看到各自职责范围内的栏目。
func TestAdministratorNavigationCoversEveryWorkspace(t *testing.T) {
	all := []string{
		"dashboard", "monitoring", "projects", "decomposition",
		"allocation", "inbox", "planning", "preparation", "qualifications", "equipment", "assignments", "methods",
		"implementation", "exceptions", "standards", "reports",
		"split-rules", "warning-rules", "automations", "permissions", "sla", "capability-codes",
	}
	for _, role := range []string{"admin", "system_admin"} {
		body := navigationBodyForRole(t, role)
		for _, section := range all {
			if !strings.Contains(body, `"`+section+`"`) {
				t.Fatalf("role %s navigation missing %q: %s", role, section, body)
			}
		}
	}
	// 业务管理员只看项目/拆解/分配，不应看到系统配置模块。
	businessAdmin := navigationBodyForRole(t, "business_admin")
	for _, section := range []string{"split-rules", "permissions", "sla", "capability-codes", "equipment"} {
		if strings.Contains(businessAdmin, `"`+section+`"`) {
			t.Fatalf("business_admin must not see %q: %s", section, businessAdmin)
		}
	}
	// 设备管理员默认进入设备能力页，而不是被兜底到 dashboard/projects。
	deviceAdmin := navigationBodyForRole(t, "device_admin")
	if !strings.Contains(deviceAdmin, `"equipment"`) || !strings.Contains(deviceAdmin, `"projects"`) {
		t.Fatalf("device_admin navigation = %s", deviceAdmin)
	}
}

func TestRoleNavigationMatchesPrototypeWorkspaces(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{"business_admin", "projects"},
		{"team_lead", "assignments"},
		{"technical_director", "standards"},
		{"project_manager", "reports"},
		{"admin", "permissions"},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			repository := &repo{}
			service := &application.Service{Repo: repository}
			id := identity{p: platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Roles: []string{tc.role}, Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: tc.role, ScopeType: "APPLICATION"}}}}
			response := perform(httpapi.NewRouter(service, id, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), http.MethodGet, "/api/v1/navigation", "")
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
func TestServiceItemConfirmationUsesWorkflow(t *testing.T) {
	response := perform(router(t, map[string]bool{"service_item.confirm": true}, nil), http.MethodPost, "/api/v1/service-items/confirm", `{"ids":["SI-1"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "SI-1") {
		t.Fatalf("body=%s", response.Body.String())
	}
}
func TestContractActivationCreatesProjectAndGroupedServiceItems(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	handler := httpapi.NewRouter(service, identity{p: platform.Principal{TenantID: "tenant-1", IdentityID: "contract_management", UserID: "contract_management", Permissions: map[string]bool{"project.contract.import": true}, DataScopes: []platform.DataScope{{RoleCode: "system_integration", ScopeType: "APPLICATION"}}}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := `{"contract_id":"HT-1","contract_version":"v1","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","services":[{"source_id":"S1","name":"等保测评","site":"上海","batch":"B1","category":"等保","system":"核心系统","requirement":"三级","test_mode":"STANDARD"},{"source_id":"S2","name":"渗透测试","site":"上海","batch":"B1","category":"渗透测试","system":"门户","requirement":"黑盒","test_mode":"PENETRATION"},{"source_id":"S3","name":"等保复测","site":"上海","batch":"B1","category":"等保","system":"门户","requirement":"二级","test_mode":"STANDARD"}]}`
	response := perform(handler, http.MethodPost, "/api/v1/contracts/activate", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"services":2`) {
		t.Fatalf("body=%s", response.Body.String())
	}
	if len(repository.projects) != 1 || repository.projects[0].Status != "待拆解确认" {
		t.Fatalf("projects=%+v", repository.projects)
	}
	if len(repository.items) != 2 || repository.items[0].Status != "待确认" || repository.items[1].Status != "待确认" {
		t.Fatalf("items=%+v", repository.items)
	}
	if len(repository.events) != 1 || repository.events[0].Payload["stamped_contract_uploaded"] != false {
		t.Fatalf("events=%+v", repository.events)
	}

	stampedBody := `{"contract_id":"HT-1","contract_version":"v1","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","stamped_contract_uploaded":true,"services":[{"source_id":"S1","site":"上海","batch":"B1","category":"等保","system":"核心系统","test_mode":"STANDARD"}]}`
	response = perform(handler, http.MethodPost, "/api/v1/contracts/activate", stampedBody)
	if response.Code != http.StatusCreated || len(repository.events) != 2 || repository.events[1].Type != application.EventContractStampStatus {
		t.Fatalf("status=%d projects=%+v events=%+v", response.Code, repository.projects, repository.events)
	}
}

func TestContractIntegrationAcceptsInternalRequestWithoutBrowserSession(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	// H4 后：内部投递必须携带可验证的机器令牌（浏览器会话依旧不需要）。
	verifier := &integrationVerifier{}
	handler := httpapi.NewRouter(service, identity{err: platform.ErrUnauthenticated}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: verifier},
	})
	body := `{"contract_id":"HT-2","contract_version":"4","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","services":[{"source_id":"S1","site":"上海","batch":"B1","category":"等保","system":"核心系统","test_mode":"STANDARD"}]}`
	deliveryID, tenantID := ulid.Make().String(), "tenant-contract"
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer verified-machine-token")
	request.Header.Set("X-Contract-Delivery-ID", deliveryID)
	request.Header.Set("X-Contract-Tenant-ID", tenantID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.projects) != 1 || repository.projects[0].TenantID != tenantID {
		t.Fatalf("projects=%+v", repository.projects)
	}
}

func TestContractIntegrationRejectsMissingRoutingHeaders(t *testing.T) {
	// H4 后：先通过机器令牌校验，再按缺失路由头拒绝 400。
	handler := httpapi.NewRouter(&application.Service{Repo: &repo{}}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: &integrationVerifier{}},
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer verified-machine-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestContractIntegrationRequiresVerifiedKeycloakMachineTokenWhenEnabled(t *testing.T) {
	service := &application.Service{Repo: &repo{}}
	verifier := &integrationVerifier{}
	handler := httpapi.NewRouter(service, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: verifier},
	})
	body := `{"contract_id":"HT-2","contract_version":"4","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","services":[{"source_id":"S1","site":"上海","batch":"B1","category":"等保","system":"核心系统","test_mode":"STANDARD"}]}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Contract-Delivery-ID", ulid.Make().String())
	request.Header.Set("X-Contract-Tenant-ID", "tenant-contract")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Contract-Delivery-ID", ulid.Make().String())
	request.Header.Set("X-Contract-Tenant-ID", "tenant-contract")
	request.Header.Set("Authorization", "Bearer verified-machine-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || verifier.token != "verified-machine-token" {
		t.Fatalf("verified bearer status=%d token=%q body=%s", response.Code, verifier.token, response.Body.String())
	}
}

func TestContractIntegrationFailsClosedWhenBearerVerifierIsUnavailable(t *testing.T) {
	handler := httpapi.NewRouter(&application.Service{Repo: &repo{}}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true},
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestContractIntegrationRejectsInvalidKeycloakMachineToken(t *testing.T) {
	verifier := &integrationVerifier{err: errors.New("invalid signature")}
	handler := httpapi.NewRouter(&application.Service{Repo: &repo{}}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: verifier},
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer invalid-machine-token")
	request.Header.Set("X-Contract-Delivery-ID", ulid.Make().String())
	request.Header.Set("X-Contract-Tenant-ID", "tenant-contract")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDashboardIntegrationReturnsTenantScopedProjectMetrics(t *testing.T) {
	repository := &repo{dashboard: domain.Dashboard{
		ProjectCount:     6,
		InFlightProjects: 4,
		RiskProjects:     2,
		ServiceItems:     18,
		StatusCounts:     map[string]int{"实施中": 4, "已完成": 2},
	}}
	handler := httpapi.NewRouter(
		&application.Service{Repo: repository},
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpapi.RouterOptions{DashboardIntegration: &httpapi.DashboardIntegrationOptions{
			Enabled:        true,
			BearerVerifier: &integrationVerifier{identity: platform.ServiceTokenIdentity{TenantID: "tenant-dashboard", ApplicationCode: "data_analysis", EnvironmentCode: "test"}},
		}},
	)

	request := httptest.NewRequest(http.MethodGet, "/internal/v1/dashboard", nil)
	request.Header.Set("Authorization", "Bearer dashboard-machine-token")
	request.Header.Set("X-DA-Tenant-ID", "spoofed-tenant")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.dashboardScope.TenantID != "tenant-dashboard" || !repository.dashboardScope.AllowAll {
		t.Fatalf("dashboard scope=%+v", repository.dashboardScope)
	}
	var payload struct {
		Data domain.Dashboard `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.ProjectCount != 6 || payload.Data.InFlightProjects != 4 || payload.Data.RiskProjects != 2 || payload.Data.ServiceItems != 18 || payload.Data.StatusCounts["实施中"] != 4 {
		t.Fatalf("dashboard=%+v", payload.Data)
	}
	if payload.Data.TenantID != "tenant-dashboard" {
		t.Fatalf("dashboard tenant=%q, want verified tenant", payload.Data.TenantID)
	}
}

func TestDashboardIntegrationUsesVerifiedTenantWithoutRoutingHeader(t *testing.T) {
	handler := httpapi.NewRouter(
		&application.Service{Repo: &repo{}},
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpapi.RouterOptions{DashboardIntegration: &httpapi.DashboardIntegrationOptions{
			Enabled:        true,
			BearerVerifier: &integrationVerifier{identity: platform.ServiceTokenIdentity{TenantID: "tenant-dashboard", ApplicationCode: "data_analysis", EnvironmentCode: "test"}},
		}},
	)
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/dashboard", nil)
	request.Header.Set("Authorization", "Bearer dashboard-machine-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMissingPermissionIsForbidden(t *testing.T) {
	response := perform(router(t, map[string]bool{"project.read": true}, nil), http.MethodPost, "/api/v1/projects", `{"name":"越权","customer":"客户","contract":"HT-1","contract_id":"approved-1"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
}

// 人员、设备与能力码是操作台表单的下拉数据源。这些只读接口以 project.read 为基线：
// 除超级管理员外，项目经理等角色同样需要渲染表单，历史上用 assign/manage 权限把守会
// 让非管理员角色整体加载失败。
func TestDirectoryReadsAreAvailableToProjectReaders(t *testing.T) {
	projectManager := map[string]bool{
		"project.read": true, "project.resource.read": true, "project.implementation.plan": true,
		"project.field.complete": true, "service_item.confirm": true,
	}
	paths := []string{"/api/v1/personnel?page=1&page_size=50", "/api/v1/capabilities", "/api/v1/equipment"}
	handler := router(t, projectManager, nil)
	for _, path := range paths {
		if response := perform(handler, http.MethodGet, path, ""); response.Code == http.StatusForbidden {
			t.Fatalf("project reader denied on %s: %s", path, response.Body.String())
		}
	}
	reader := router(t, map[string]bool{}, nil)
	for _, path := range paths {
		if response := perform(reader, http.MethodGet, path, ""); response.Code != http.StatusForbidden {
			t.Fatalf("principal without project.read reached %s: status=%d", path, response.Code)
		}
	}
}

func TestDeleteEquipmentRequiresDeviceManagePermission(t *testing.T) {
	response := perform(router(t, map[string]bool{"project.read": true}, nil), http.MethodDelete, "/api/v1/equipment/EQ-001", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing device manage permission status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPermissionWithoutDataScopeIsForbidden(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Permissions: map[string]bool{"project.read": true}, AuthorizationRevision: 1}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/api/v1/projects", "")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "PM_SCOPE_FORBIDDEN") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMeReturnsStableIdentityAndDataScopes(t *testing.T) {
	response := perform(router(t, map[string]bool{"project.read": true}, nil), http.MethodGet, "/api/v1/auth/me", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"identity_id", "person_id", "data_scopes", "authorization_revision", "catalog_version"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("response missing %s: %s", expected, response.Body.String())
		}
	}
}
func TestWriteIsReportedToAudit(t *testing.T) {
	reporter := &audit{}
	response := perform(router(t, map[string]bool{"project.create": true}, reporter), http.MethodPost, "/api/v1/projects", `{"name":"审计项目","customer":"客户","contract":"HT-1","contract_id":"approved-1","service_items":[{"site":"杭州机房"}]}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d", response.Code)
	}
	if len(reporter.events) != 1 || reporter.events[0].ActorID != "user-1" {
		t.Fatalf("events=%+v", reporter.events)
	}
}
func TestUnauthenticated(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	handler := httpapi.NewRouter(service, identity{err: platform.ErrUnauthenticated}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/api/v1/projects", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestRequestIDReplacesInvalidClientValue(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "untrusted request id")
	response := httptest.NewRecorder()
	httpapi.NewRouter(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(response, request)

	if _, err := ulid.ParseStrict(response.Header().Get("X-Request-ID")); err != nil {
		t.Fatalf("generated X-Request-ID is not a ULID: %v", err)
	}
}

func TestHealthReportsWhetherPlatformAuditIsEnabled(t *testing.T) {
	for name, reporter := range map[string]platform.AuditReporter{
		"disabled": nil,
		"enabled":  &audit{},
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(httpapi.NewRouter(nil, nil, reporter, slog.New(slog.NewTextHandler(io.Discard, nil))), http.MethodGet, "/healthz", "")
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d", response.Code)
			}
			// data 里除 audit 之外还带 pending_migrations（数字），因此按 any 解码后取值。
			var payload struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if got, _ := payload.Data["audit"].(string); got != name {
				t.Fatalf("audit=%q, want %q", payload.Data["audit"], name)
			}
			// 待执行迁移数量必须在存活检查里可见：部署巡检据此发现"库结构落后于代码"。
			if _, ok := payload.Data["pending_migrations"]; !ok {
				t.Fatalf("healthz 应带出 pending_migrations：%s", response.Body.String())
			}
		})
	}
}

func TestReadinessFailsWhenRequiredAuditIsDisabled(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	response := perform(httpapi.NewRouter(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), http.MethodGet, "/readyz", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	var payload struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data["audit"] != "disabled" || payload.Data["status"] != "not_ready" {
		t.Fatalf("readiness=%v", payload.Data)
	}
}

func TestRequestIDPreservesValidClientULID(t *testing.T) {
	id := ulid.Make().String()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", id)
	response := httptest.NewRecorder()
	httpapi.NewRouter(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(response, request)

	if got := response.Header().Get("X-Request-ID"); got != id {
		t.Fatalf("X-Request-ID = %q, want %q", got, id)
	}
}

// 前置状态不满足时不能用 422「请求参数不合法」打发用户：必须 409 + 具体缺哪一步。
func TestImplementationPlanPreconditionReturnsActionableConflict(t *testing.T) {
	handler := router(t, map[string]bool{"project.read": true, "project.implementation.plan": true}, nil)
	body := `{"planned_start":"2026-09-15T02:00:00Z","planned_end":"2026-09-16T02:00:00Z","site_plan":"现场实施步骤"}`
	response := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/implementation-plan", body)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	payload := response.Body.String()
	if !strings.Contains(payload, "PM_PRECONDITION_FAILED") {
		t.Fatalf("expected PM_PRECONDITION_FAILED, got %s", payload)
	}
	if !strings.Contains(payload, "指派项目经理") {
		t.Fatalf("message must name the missing step, got %s", payload)
	}
	if strings.Contains(payload, "请求参数不合法") {
		t.Fatalf("precondition must not be reported as a field error: %s", payload)
	}
}

// 真正的字段级错误仍为 422，但必须点名具体字段。
func TestImplementationPlanValidationNamesTheInvalidField(t *testing.T) {
	repository := &repo{items: []domain.ServiceItem{{ID: "SI-1", Status: "待分配", ProjectManagerID: "pm-1", ConflictStatus: "PASSED"}}}
	service := &application.Service{Repo: repository}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Roles: []string{"project_manager"}, Permissions: map[string]bool{"project.read": true, "project.implementation.plan": true}, DataScopes: []platform.DataScope{{RoleCode: "project_manager", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	response := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/implementation-plan", `{"planned_start":"","planned_end":"","site_plan":"现场实施步骤"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "计划开始与计划结束必须是有效的日期时间") {
		t.Fatalf("message should name the invalid fields: %s", response.Body.String())
	}

	response = perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/implementation-plan", `{"planned_start":"2026-09-16T02:00:00Z","planned_end":"2026-09-15T02:00:00Z","site_plan":"现场实施步骤"}`)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "计划结束时间必须晚于计划开始时间") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

}

// 人员姓名批量解析：目录未开通时明确返回 503，而不是让界面显示一串 ULID。
func TestPersonnelNamesEndpointReportsUnavailableDirectory(t *testing.T) {
	handler := router(t, map[string]bool{"project.read": true}, nil)
	response := perform(handler, http.MethodGet, "/api/v1/personnel/names?user_ids=u-1,u-2", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "PM_PERSONNEL_UNAVAILABLE") {
		t.Fatalf("body=%s", response.Body.String())
	}
}

// 目录可用时返回 user_id → 显示名 的映射，供界面把团队负责人/项目经理/工程师渲染成姓名。
func TestPersonnelNamesEndpointResolvesNames(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository, Personnel: ownerDirectoryStub{names: map[string]string{"u-1": "张三"}}}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/api/v1/personnel/names?user_ids=u-1,u-404", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "张三") || !strings.Contains(body, `"u-1"`) {
		t.Fatalf("body=%s", body)
	}
	if strings.Contains(body, "u-404") {
		t.Fatalf("unresolvable id must be omitted: %s", body)
	}
}

// 团队负责人/项目经理/工程师下拉按应用角色取人：/personnel 必须把 role_code 原样透传给
// 平台负责人目录，并同时接受重复参数与逗号分隔两种写法。
func TestSlaOverdueEndpointReturnsOverdueItems(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/api/v1/delivery/sla-overdue", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"data":[]`) {
		t.Fatalf("expected empty overdue array, got %s", response.Body.String())
	}
}

func TestPersonnelEndpointForwardsRoleCodes(t *testing.T) {
	directory := &recordingOwnerDirectoryStub{}
	service := &application.Service{Repo: &repo{}, Personnel: directory}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	response := perform(handler, http.MethodGet, "/api/v1/personnel?role_code=team_lead&role_code=project_manager,engineer&role_origin=TEMPLATE&page=1&page_size=50", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := strings.Join(directory.last.RoleCodes, ","); got != "team_lead,project_manager,engineer" {
		t.Fatalf("role codes = %q, want %q", got, "team_lead,project_manager,engineer")
	}
	if got := strings.Join(directory.last.RoleOrigins, ","); got != "TEMPLATE" {
		t.Fatalf("role origins = %q, want %q", got, "TEMPLATE")
	}

	// 不带 role_code 时保持原有语义：返回应用内全部可选人员，而不是过滤空角色。
	response = perform(handler, http.MethodGet, "/api/v1/personnel?page=1&page_size=50", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(directory.last.RoleCodes) != 0 || len(directory.last.RoleOrigins) != 0 {
		t.Fatalf("role codes = %v, role origins = %v, want empty", directory.last.RoleCodes, directory.last.RoleOrigins)
	}
}

func TestQualifiedPersonnelEndpointUsesProjectCapabilityLedger(t *testing.T) {
	repository := &repo{capabilities: []domain.Capability{{
		ResourceType: "PERSON", ResourceID: "P-0001", ResourceName: "张三", UserID: "user-qualified",
		Codes: []string{"CISP"}, Status: "ACTIVE", IdentityStatus: domain.IdentityStatusActive,
	}}}
	service := &application.Service{Repo: repository}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "identity-1", UserID: "admin-1", Roles: []string{"business_admin"}, Permissions: map[string]bool{"project.team.assign": true}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}}}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/api/v1/qualified-personnel?keyword=CISP&page=1&page_size=50", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"user_id":"user-qualified"`, `"resource_id":"P-0001"`, `"display_name":"张三"`, `"CISP"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %s: %s", expected, body)
		}
	}
}

type recordingOwnerDirectoryStub struct{ last platform.OwnerDirectoryQuery }

func (stub *recordingOwnerDirectoryStub) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	stub.last = query
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{}, Page: 1, PageSize: 50}, nil
}

type ownerDirectoryStub struct{ names map[string]string }

func (stub ownerDirectoryStub) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	display, ok := stub.names[query.UserID]
	if !ok {
		return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{}}, nil
	}
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: query.UserID, DisplayName: display}}}, nil
}

// 现场实施计划必须携带人员与设备清单：清单随事件载荷下发给仓储层，
// 快照字段由服务端从能力档案解析，浏览器只提交资源标识与使用时段。
func TestImplementationPlanCarriesResolvedPersonnel(t *testing.T) {
	repository := &repo{
		items: []domain.ServiceItem{{ID: "SI-1", Status: "待分配", ProjectManagerID: "pm-1", ConflictStatus: "PASSED"}},
		capabilities: []domain.Capability{
			{ResourceType: "PERSON", ResourceID: "P-001", ResourceName: "王明", Codes: []string{"CISP-PTE"}, Status: "ACTIVE", ValidUntil: time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)},
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "无线测试套件", Codes: []string{"802.11 a/b/g/n/ac"}, Status: "ACTIVE"},
		},
	}
	service := &application.Service{Repo: repository}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Roles: []string{"project_manager"}, Permissions: map[string]bool{"project.read": true, "project.implementation.plan": true}, DataScopes: []platform.DataScope{{RoleCode: "project_manager", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	body := `{"planned_start":"2026-09-15T02:00:00Z","planned_end":"2026-09-20T02:00:00Z","personnel":[{"resource_type":"PERSON","resource_id":"P-001"}]}`
	response := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/implementation-plan", body)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.events) != 1 {
		t.Fatalf("events=%+v", repository.events)
	}
	personnel, ok := repository.events[0].Payload["personnel"].([]domain.PlanResource)
	if !ok || len(personnel) != 1 {
		t.Fatalf("payload personnel=%#v", repository.events[0].Payload["personnel"])
	}
	if personnel[0].ResourceName != "王明" {
		t.Fatalf("personnel=%+v", personnel)
	}

	// 没有人员行的计划不得发布：现场实施必须有人可派。
	repository.events = nil
	response = perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/implementation-plan", `{"planned_start":"2026-09-15T02:00:00Z","planned_end":"2026-09-20T02:00:00Z","site_plan":"现场实施步骤","personnel":[]}`)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "至少添加一名实施人员") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// 实施准备阶段登记设备清单：占用冲突必须硬拦并点名占用方，未被占用的设备正常落库。
func TestPreparationRecordsEquipmentAndBlocksOverlappingReservation(t *testing.T) {
	repository := &repo{
		items: []domain.ServiceItem{{
			ID: "SI-1", Status: "待实施", ProjectManagerID: "pm-1", ConflictStatus: "PASSED",
			PlannedStart: "2026-09-15T02:00:00Z", PlannedEnd: "2026-09-20T02:00:00Z",
		}},
		capabilities: []domain.Capability{
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", ResourceName: "无线测试套件", Status: "ACTIVE"},
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-002", ResourceName: "BurpSuite 终端", Status: "ACTIVE"},
		},
		reservations: []domain.EquipmentReservation{
			{ServiceItemID: "SI-OTHER", ProjectID: "PJ-2026-002", ResourceID: "EQ-001", ResourceName: "无线测试套件", WindowStart: "2026-09-16", WindowEnd: "2026-09-18"},
		},
	}
	service := &application.Service{Repo: repository}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Roles: []string{"project_manager"}, Permissions: map[string]bool{"project.read": true, "project.implementation.plan": true}, DataScopes: []platform.DataScope{{RoleCode: "project_manager", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	conflicting := `{"travel_request_id":"TRIP-1","equipment":[{"resource_type":"EQUIPMENT","resource_id":"EQ-001","window_start":"2026-09-17","window_end":"2026-09-19"}]}`
	response := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/preparation", conflicting)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "PM_RESOURCE_CONFLICT") || !strings.Contains(response.Body.String(), "PJ-2026-002") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.events) != 0 {
		t.Fatalf("conflicting preparation must not record an event: %+v", repository.events)
	}

	free := `{"travel_request_id":"TRIP-1","equipment":[{"resource_type":"EQUIPMENT","resource_id":"EQ-002","window_start":"2026-09-16","window_end":"2026-09-18"}]}`
	response = perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/preparation", free)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.events) != 1 {
		t.Fatalf("events=%+v", repository.events)
	}
	equipment, ok := repository.events[0].Payload["equipment"].([]domain.PlanResource)
	if !ok || len(equipment) != 1 || equipment[0].ResourceName != "BurpSuite 终端" || equipment[0].WindowStart != "2026-09-16" {
		t.Fatalf("equipment=%#v", repository.events[0].Payload["equipment"])
	}
}

// M2：安全头补齐防内嵌/防嗅探/防缓存配置，且对任意接口生效。
func TestSecurityHeadersAreSetOnEveryResponse(t *testing.T) {
	handler := httpapi.NewRouter(&application.Service{Repo: &repo{}}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/healthz", "")
	for _, header := range []string{"X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy", "Content-Security-Policy", "Cache-Control"} {
		if value := response.Header().Get(header); value == "" {
			t.Fatalf("missing security header %s", header)
		}
	}
	if response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options=%q", response.Header().Get("X-Frame-Options"))
	}
}

// M2：内部机器接口（合同激活投递）同样写入平台审计，不再出现无审计的写通道。
func TestInternalContractActivationIsAudited(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	auditLog := &audit{}
	handler := httpapi.NewRouter(service, identity{err: platform.ErrUnauthenticated}, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: &integrationVerifier{}},
	})
	body := `{"contract_id":"HT-AUDIT","contract_version":"1","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","services":[{"source_id":"S1","site":"上海","batch":"B1","category":"等保","test_mode":"STANDARD"}]}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer verified-machine-token")
	request.Header.Set("X-Contract-Delivery-ID", ulid.Make().String())
	request.Header.Set("X-Contract-Tenant-ID", "tenant-audit")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(auditLog.events) != 1 {
		t.Fatalf("internal activation must be audited, got %+v", auditLog.events)
	}
	event := auditLog.events[0]
	if event.ActorID != "contract_management" || !strings.Contains(event.Action, "internal.v1.contracts.activate") {
		t.Fatalf("audit event=%+v", event)
	}
}

// 列表接口统一返回分页 envelope：显式分页时给出当前页与筛选后的总数，
// 未指定 page_size 时返回全部（下拉数据源需要完整集合）。
func TestProjectListPaginationEnvelope(t *testing.T) {
	repository := &repo{projects: []domain.Project{
		{ID: "PJ-1", TenantID: "tenant-1"}, {ID: "PJ-2", TenantID: "tenant-1"}, {ID: "PJ-3", TenantID: "tenant-1"},
	}}
	service := &application.Service{Repo: repository}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	response := perform(handler, http.MethodGet, "/api/v1/projects?page=2&page_size=1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Data struct {
			Items    []domain.Project `json:"items"`
			Total    int              `json:"total"`
			Page     int              `json:"page"`
			PageSize int              `json:"page_size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v body=%s", err, response.Body.String())
	}
	if page.Data.Total != 3 || page.Data.Page != 2 || page.Data.PageSize != 1 || len(page.Data.Items) != 1 {
		t.Fatalf("unexpected page: %+v", page.Data)
	}
	if page.Data.Items[0].ID != "PJ-2" {
		t.Fatalf("second page must carry the second row, got %s", page.Data.Items[0].ID)
	}

	// 未指定 page_size 时不截断，保持"完整集合"语义。
	response = perform(handler, http.MethodGet, "/api/v1/projects", "")
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Data.Total != 3 || len(page.Data.Items) != 3 {
		t.Fatalf("unpaginated list must return everything: %+v", page.Data)
	}

	// 分页参数非法时明确报错，而不是静默返回第一页。
	response = perform(handler, http.MethodGet, "/api/v1/projects?page=0", "")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid page must be rejected, status=%d body=%s", response.Code, response.Body.String())
	}
}

// 超大 page_size 被收敛到上限，避免客户端拉爆响应。
func TestVeryLargePageSizeIsCapped(t *testing.T) {
	handler := router(t, map[string]bool{"project.read": true}, nil)
	response := perform(handler, http.MethodGet, "/api/v1/projects?page=1&page_size=100000", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Data struct {
			PageSize int `json:"page_size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Data.PageSize != 200 {
		t.Fatalf("page_size must be capped at 200, got %d", page.Data.PageSize)
	}
}
