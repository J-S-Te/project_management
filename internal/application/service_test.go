package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type scopeRepository struct {
	lastFilter platform.ScopeFilter
	created    domain.Project
}

type serviceProjectRepository struct {
	scopeRepository
	items []domain.ServiceItem
}

func (r *serviceProjectRepository) CreateProjectWithServiceItems(_ context.Context, project domain.Project, items []domain.ServiceItem) error {
	r.created = project
	r.items = items
	return nil
}

func (r *scopeRepository) ListProjects(_ context.Context, filter platform.ScopeFilter, _, _ string) ([]domain.Project, error) {
	r.lastFilter = filter
	return nil, nil
}
func (r *scopeRepository) GetProject(_ context.Context, filter platform.ScopeFilter, id string) (domain.Project, error) {
	r.lastFilter = filter
	return domain.Project{ID: id, TenantID: filter.TenantID}, nil
}
func (r *scopeRepository) CreateProject(_ context.Context, item domain.Project) error {
	r.created = item
	return nil
}
func (r *scopeRepository) ListServiceItems(_ context.Context, filter platform.ScopeFilter, _ string) ([]domain.ServiceItem, error) {
	r.lastFilter = filter
	return nil, nil
}
func (r *scopeRepository) GetServiceItem(_ context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	r.lastFilter = filter
	return domain.ServiceItem{ID: id, ProjectID: "PJ-1"}, nil
}
func (r *scopeRepository) ConfirmServiceItems(context.Context, platform.ScopeFilter, []string, string) ([]domain.ServiceItem, error) {
	return nil, nil
}
func (r *scopeRepository) ListRules(context.Context, string, string) ([]domain.Rule, error) {
	return nil, nil
}
func (r *scopeRepository) CreateRule(_ context.Context, item domain.Rule) (domain.Rule, error) {
	return item, nil
}
func (r *scopeRepository) UpdateRule(_ context.Context, _ string, _ string, _ int64, item domain.Rule) (domain.Rule, error) {
	return item, nil
}
func (r *scopeRepository) SetRuleEnabled(_ context.Context, _ string, _ string, id int64, enabled bool, _ string) (domain.Rule, error) {
	return domain.Rule{ID: id, Enabled: enabled}, nil
}
func (r *scopeRepository) Dashboard(_ context.Context, filter platform.ScopeFilter) (domain.Dashboard, error) {
	r.lastFilter = filter
	return domain.Dashboard{}, nil
}

func principalWith(permission string, scopes ...platform.DataScope) platform.Principal {
	return platform.Principal{TenantID: "tenant-1", IdentityID: "identity-1", UserID: "identity-1", Permissions: map[string]bool{permission: true}, DataScopes: scopes}
}

func TestApplicationScopeAllowsAllProjectQueries(t *testing.T) {
	repository := &scopeRepository{}
	service := &Service{Repo: repository}
	principal := principalWith("project.read", platform.DataScope{RoleCode: "project_manager", ScopeType: "APPLICATION"})
	if _, err := service.ListProjects(context.Background(), principal, "", ""); err != nil {
		t.Fatal(err)
	}
	if !repository.lastFilter.AllowAll || repository.lastFilter.TenantID != "tenant-1" {
		t.Fatalf("filter=%+v", repository.lastFilter)
	}
}

func TestProjectPermissionWithoutScopeIsForbidden(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}}
	if _, err := service.ListProjects(context.Background(), principalWith("project.read"), "", ""); err != ErrForbidden {
		t.Fatalf("error=%v", err)
	}
}

func TestSelfCreateStoresStableOwnerIdentity(t *testing.T) {
	repository := &scopeRepository{}
	service := &Service{Repo: repository}
	principal := principalWith("project.create", platform.DataScope{RoleCode: "project_manager", ScopeType: "SELF", ScopeID: "identity-1"})
	created, err := service.CreateProject(context.Background(), principal, domain.Project{Name: "项目", Customer: "客户", Contract: "HT-1"})
	if err != nil {
		t.Fatal(err)
	}
	if created.OwnerIdentityID != "identity-1" || repository.created.OwnerIdentityID != "identity-1" {
		t.Fatalf("created=%+v stored=%+v", created, repository.created)
	}
}

func TestProjectCreationCanPersistInitialServiceItemsAtomically(t *testing.T) {
	repository := &serviceProjectRepository{}
	service := &Service{Repo: repository}
	created, err := service.CreateProjectWithServiceItems(context.Background(), principalWith("project.create", platform.DataScope{RoleCode: "project_manager", ScopeType: "SELF", ScopeID: "identity-1"}), domain.Project{Name: "项目", Customer: "客户", Contract: "HT-1"}, []domain.ContractService{{Site: "杭州机房", Batch: "第一批", Category: "信息安全检测", Requirement: "按标准执行"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Services != 1 || len(repository.items) != 1 || repository.items[0].ProjectID != created.ID || repository.items[0].Status != "待确认" {
		t.Fatalf("created=%+v items=%+v", created, repository.items)
	}
}

func TestOrganizationCreateRequiresAndStoresAuthorizedOwnerOrg(t *testing.T) {
	repository := &scopeRepository{}
	service := &Service{Repo: repository}
	principal := principalWith("project.create", platform.DataScope{RoleCode: "project_manager", ScopeType: "ORG", ScopeID: "org-1"})
	created, err := service.CreateProject(context.Background(), principal, domain.Project{Name: "项目", Customer: "客户", Contract: "HT-1"})
	if err != nil || created.OwnerOrgID != "org-1" {
		t.Fatalf("created=%+v error=%v", created, err)
	}
	_, err = service.CreateProject(context.Background(), principal, domain.Project{Name: "项目", Customer: "客户", Contract: "HT-2", OwnerOrgID: "org-2"})
	if err != ErrForbidden {
		t.Fatalf("cross-org create error=%v", err)
	}
}

func TestProjectOnlyScopeCannotCreateUnassignedProject(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}}
	principal := principalWith("project.create", platform.DataScope{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-existing"})
	if _, err := service.CreateProject(context.Background(), principal, domain.Project{Name: "项目", Customer: "客户", Contract: "HT-1"}); err != ErrForbidden {
		t.Fatalf("error=%v", err)
	}
}

func TestNarrowScopeCannotManageTenantWideRulesOrCapabilities(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-1"})
	if _, err := service.CreateRule(context.Background(), principal, domain.Rule{Name: "越权规则", Scope: "tenant"}); err != ErrForbidden {
		t.Fatalf("CreateRule error=%v", err)
	}
	principal = principalWith("project.read", platform.DataScope{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-1"})
	if _, err := service.ListRules(context.Background(), principal, "split-rules"); err != ErrForbidden {
		t.Fatalf("ListRules error=%v", err)
	}
	principal = principalWith("project.resource.manage", platform.DataScope{RoleCode: "project_manager", ScopeType: "ORG", ScopeID: "org-1"})
	if _, err := service.UpsertCapability(context.Background(), principal, domain.Capability{ResourceType: "PERSON", ResourceID: "person-1", ResourceName: "人员", Codes: []string{"TEST"}}); err != ErrForbidden {
		t.Fatalf("UpsertCapability error=%v", err)
	}
	principal = principalWith("project.resource.read", platform.DataScope{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-1"})
	if _, err := service.ListCapabilities(context.Background(), principal, "PERSON"); err != ErrForbidden {
		t.Fatalf("ListCapabilities error=%v", err)
	}
}

func TestFullDataScopeCanManageTenantWideRules(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-1"})
	if _, err := service.CreateRule(context.Background(), principal, domain.Rule{Name: "规则", Scope: "tenant"}); err != nil {
		t.Fatal(err)
	}
}

type personnelStub struct {
	page      platform.OwnerDirectoryPage
	err       error
	lastQuery platform.OwnerDirectoryQuery
}

func (stub *personnelStub) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	stub.lastQuery = query
	return stub.page, stub.err
}

// 人员目录是操作台表单的下拉数据源：project.read 即可读取，assign 权限继续保留兼容；
// 未配置目录时返回“目录不可用”，完全没有目录读权限时才是越权。
func TestListPersonnelRequiresDirectoryReadPermissionAndConfiguredDirectory(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}}
	if _, err := service.ListPersonnel(context.Background(), principalWith("project.create"), "", "", nil, nil, 0, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListPersonnel() without directory read permission error = %v, want %v", err, ErrForbidden)
	}
	for _, permission := range []string{"project.read", "project.team.assign", "project.execution.assign"} {
		if _, err := service.ListPersonnel(context.Background(), principalWith(permission), "", "", nil, nil, 0, 0); !errors.Is(err, ErrPersonnelUnavailable) {
			t.Fatalf("ListPersonnel() with %s and no directory error = %v, want %v", permission, err, ErrPersonnelUnavailable)
		}
	}
	stub := &personnelStub{page: platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: "user-1", DisplayName: "张三"}}, Page: 1, PageSize: 50, Total: 1}}
	service.Personnel = stub
	page, err := service.ListPersonnel(context.Background(), principalWith("project.read"), "张", "", nil, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].UserID != "user-1" {
		t.Fatalf("unexpected page: %#v", page)
	}
}

// 团队负责人/项目经理/工程师的下拉按应用角色取人：角色码必须原样透传给平台目录，
// 空白和重复值在进入查询前就要被剔除，否则会变成对空角色的过滤。
func TestListPersonnelForwardsNormalizedRoleCodes(t *testing.T) {
	stub := &personnelStub{page: platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{}, Page: 1, PageSize: 50}}
	service := &Service{Repo: &scopeRepository{}, Personnel: stub}
	if _, err := service.ListPersonnel(context.Background(), principalWith("project.team.assign"), "", "", []string{" team_lead ", "", "team_lead", "project_manager"}, []string{"TEMPLATE", "TEMPLATE"}, 1, 50); err != nil {
		t.Fatal(err)
	}
	if strings.Join(stub.lastQuery.RoleCodes, ",") != "team_lead,project_manager" {
		t.Fatalf("role codes = %v, want [team_lead project_manager]", stub.lastQuery.RoleCodes)
	}
	// 来源过滤同样去重后透传：它决定下拉里是否会出现管理员直接开通的例外绑定。
	if strings.Join(stub.lastQuery.RoleOrigins, ",") != "TEMPLATE" {
		t.Fatalf("role origins = %v, want [TEMPLATE]", stub.lastQuery.RoleOrigins)
	}
	if _, err := service.ListPersonnel(context.Background(), principalWith("project.read"), "", "", []string{"  ", ""}, []string{}, 1, 50); err != nil {
		t.Fatal(err)
	}
	if len(stub.lastQuery.RoleCodes) != 0 || len(stub.lastQuery.RoleOrigins) != 0 {
		t.Fatalf("empty filters must not become a filter: %v / %v", stub.lastQuery.RoleCodes, stub.lastQuery.RoleOrigins)
	}
}

// personnelDirectoryStub 按 user_id 精确应答，模拟平台负责人目录的查询语义。
type personnelDirectoryStub struct {
	names map[string]string
	err   error
}

func (stub personnelDirectoryStub) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if stub.err != nil {
		return platform.OwnerDirectoryPage{}, stub.err
	}
	display, ok := stub.names[query.UserID]
	if !ok {
		return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{}, Page: 1, PageSize: 1}, nil
	}
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: query.UserID, DisplayName: display}}, Page: 1, PageSize: 1}, nil
}

// 界面必须显示姓名而不是 ULID：批量解析要能一次拿到多名人员，并跳过已离职的 ID。
func TestResolvePersonnelNamesBatchesAndSkipsUnknown(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}, Personnel: personnelDirectoryStub{names: map[string]string{"u-1": "张三", "u-2": "李四"}}}
	names, err := service.ResolvePersonnelNames(context.Background(), principalWith("project.read"), []string{"u-1", " u-2 ", "u-1", "", "u-404"})
	if err != nil {
		t.Fatal(err)
	}
	if names["u-1"] != "张三" || names["u-2"] != "李四" {
		t.Fatalf("unexpected names: %v", names)
	}
	if _, exists := names["u-404"]; exists {
		t.Fatalf("unknown id must not be resolved: %v", names)
	}
	if len(names) != 2 {
		t.Fatalf("duplicate ids must be collapsed: %v", names)
	}
}

func TestResolvePersonnelNamesFailsClosedWhenDirectoryIsDown(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}, Personnel: personnelDirectoryStub{err: errors.New("directory unavailable")}}
	if _, err := service.ResolvePersonnelNames(context.Background(), principalWith("project.read"), []string{"u-1", "u-2"}); !errors.Is(err, ErrPersonnelUnavailable) {
		t.Fatalf("err = %v, want ErrPersonnelUnavailable", err)
	}
}

func TestResolvePersonnelNamesRequiresReadPermission(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}, Personnel: personnelDirectoryStub{}}
	if _, err := service.ResolvePersonnelNames(context.Background(), principalWith("project.create"), []string{"u-1"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// 目录只支持单个 user_id 查询，必须给扇出设上限，避免把目录接口当成自由查询入口。
func TestResolvePersonnelNamesCapsLookupCount(t *testing.T) {
	names := map[string]string{}
	ids := make([]string, 0, maximumPersonnelNameLookups+20)
	for index := 0; index < maximumPersonnelNameLookups+20; index++ {
		id := fmt.Sprintf("u-%03d", index)
		ids = append(ids, id)
		names[id] = id
	}
	service := &Service{Repo: &scopeRepository{}, Personnel: personnelDirectoryStub{names: names}}
	resolved, err := service.ResolvePersonnelNames(context.Background(), principalWith("project.read"), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != maximumPersonnelNameLookups {
		t.Fatalf("resolved %d names, want the cap %d", len(resolved), maximumPersonnelNameLookups)
	}
}

// capabilityRepository 组合 scopeRepository 并补齐 DeliveryRepository，用于验证
// 目录读写的数据范围策略（读：组织级；写：全量范围）。
type capabilityRepository struct {
	scopeRepository
	capabilities []domain.Capability
	reservations []domain.EquipmentReservation
	saved        []domain.Capability
	// identityStatuses 记录身份复核回写的结果，供人员资质同步用例断言。
	identityStatuses map[string]string
}

func (r *capabilityRepository) UpdateCapabilityIdentities(_ context.Context, _ string, statuses map[string]string, _ time.Time) error {
	if r.identityStatuses == nil {
		r.identityStatuses = map[string]string{}
	}
	for userID, status := range statuses {
		r.identityStatuses[userID] = status
	}
	return nil
}

func (r *capabilityRepository) FindProjectByContractVersion(context.Context, platform.ScopeFilter, string, string) (domain.Project, error) {
	return domain.Project{}, ErrNotFound
}
func (r *capabilityRepository) ActivateContract(context.Context, domain.Project, []domain.ServiceItem, domain.DeliveryEvent) error {
	return nil
}
func (r *capabilityRepository) SyncContractStampStatus(context.Context, domain.Project, bool, domain.DeliveryEvent) error {
	return nil
}
func (r *capabilityRepository) ApplyDeliveryEvent(context.Context, domain.DeliveryEvent) error {
	return nil
}
func (r *capabilityRepository) ListSlaOverdue(context.Context, platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	return nil, nil
}
func (r *capabilityRepository) ListDeliveryEvents(context.Context, platform.ScopeFilter, string) ([]domain.DeliveryEvent, error) {
	return nil, nil
}
func (r *capabilityRepository) FindProjectForDeviation(context.Context, platform.ScopeFilter, string) (string, string, error) {
	return "", "", ErrNotFound
}
func (r *capabilityRepository) UpsertCapability(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	r.saved = append(r.saved, item)
	return item, nil
}
func (r *capabilityRepository) ListCapabilities(_ context.Context, _ string, typ string) ([]domain.Capability, error) {
	// 与真实仓储一致地按资源类型过滤：人员资质复核只应看到 PERSON 档案。
	if typ == "" {
		return r.capabilities, nil
	}
	filtered := make([]domain.Capability, 0, len(r.capabilities))
	for _, item := range r.capabilities {
		if item.ResourceType == typ {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}
func (r *capabilityRepository) FindCapabilities(context.Context, string, string, []string) ([]domain.Capability, error) {
	return nil, nil
}
func (r *capabilityRepository) ListEquipmentReservations(context.Context, string, string) ([]domain.EquipmentReservation, error) {
	return r.reservations, nil
}

// 组织级范围（ORG）允许读租户级能力目录：quality_manager 在"资质与能力"栏目
// 需要看到目录数据来渲染表单，写权限仍由 resource.manage 全量范围把守。
func TestOrganizationalScopeCanReadCapabilityDirectory(t *testing.T) {
	repository := &capabilityRepository{capabilities: []domain.Capability{{ResourceType: "PERSON", ResourceID: "P-0001"}}}
	service := &Service{Repo: repository}
	principal := principalWith("project.resource.read", platform.DataScope{RoleCode: "quality_manager", ScopeType: "ORG", ScopeID: "org-1"})
	items, err := service.ListCapabilities(context.Background(), principal, "PERSON")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ResourceID != "P-0001" {
		t.Fatalf("items=%+v", items)
	}
}

// 按项目或个人范围的角色不能读租户级能力目录，避免跨项目泄露人员资质/设备主数据。
func TestProjectScopeStillCannotReadCapabilityDirectory(t *testing.T) {
	service := &Service{Repo: &capabilityRepository{}}
	principal := principalWith("project.resource.read", platform.DataScope{RoleCode: "engineer", ScopeType: "PROJECT", ScopeID: "PJ-1"})
	if _, err := service.ListCapabilities(context.Background(), principal, "PERSON"); err != ErrForbidden {
		t.Fatalf("error=%v", err)
	}
}

// splitRuleRepository 提供可注入的拆解规则，验证拆解时自动确认语义。
type splitRuleRepository struct {
	serviceProjectRepository
	rules []domain.Rule
}

func (r *splitRuleRepository) ListRules(context.Context, string, string) ([]domain.Rule, error) {
	return r.rules, nil
}

// 存在启用拆解规则时：未命中适用范围的常规批次自动确认（待分配），命中的保留待确认。
func TestSplitRulesAutoConfirmNonMatchingItems(t *testing.T) {
	repository := &splitRuleRepository{rules: []domain.Rule{{Enabled: true, Name: "大额批次", Scope: "金额超过 50 万元"}}}
	service := &Service{Repo: repository}
	principal := principalWith("project.create", platform.DataScope{RoleCode: "project_manager", ScopeType: "SELF", ScopeID: "identity-1"})
	_, err := service.CreateProjectWithServiceItems(context.Background(), principal,
		domain.Project{Name: "项目", Customer: "客户", Contract: "HT-1"},
		[]domain.ContractService{
			{Site: "杭州机房", Batch: "第一批", TestMode: "STANDARD"},
			{Site: "上海机房", Batch: "ZH-金额超过 50 万元-001", TestMode: "STANDARD"},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.items) != 2 {
		t.Fatalf("items=%+v", repository.items)
	}
	if repository.items[0].Status != "待分配" {
		t.Fatalf("non-matching item should auto-confirm, got %q", repository.items[0].Status)
	}
	if repository.items[1].Status != "待确认" {
		t.Fatalf("matching item should remain manual-confirm, got %q", repository.items[1].Status)
	}
}

// 未配置任何拆解规则时行为与历史一致：全部服务项待人工确认，不自动放行。
func TestSplitRulesWithoutRulesKeepManualConfirm(t *testing.T) {
	repository := &serviceProjectRepository{}
	service := &Service{Repo: repository}
	_, err := service.CreateProjectWithServiceItems(context.Background(),
		principalWith("project.create", platform.DataScope{RoleCode: "project_manager", ScopeType: "SELF", ScopeID: "identity-1"}),
		domain.Project{Name: "项目", Customer: "客户", Contract: "HT-1"},
		[]domain.ContractService{{Site: "杭州机房", Batch: "第一批", TestMode: "STANDARD"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.items) != 1 || repository.items[0].Status != "待确认" {
		t.Fatalf("items=%+v", repository.items)
	}
}

// maskingRepository 提供可注入的字段权限规则与读数据，验证读侧 hidden 脱敏。
type maskingRepository struct {
	scopeRepository
	rules   []domain.Rule
	project domain.Project
	items   []domain.ServiceItem
}

func (r *maskingRepository) ListRules(context.Context, string, string) ([]domain.Rule, error) {
	return r.rules, nil
}
func (r *maskingRepository) GetProject(_ context.Context, _ platform.ScopeFilter, id string) (domain.Project, error) {
	project := r.project
	project.ID = id
	return project, nil
}
func (r *maskingRepository) ListServiceItems(_ context.Context, _ platform.ScopeFilter, _ string) ([]domain.ServiceItem, error) {
	return r.items, nil
}

// 命中角色的 hidden 规则把敏感字段脱敏为 ***，其余字段不受影响。
func TestFieldPermissionHidesFieldsForRole(t *testing.T) {
	repository := &maskingRepository{
		rules: []domain.Rule{
			{Enabled: true, RoleCode: "engineer", FieldName: "customer", AccessLevel: "hidden"},
			{Enabled: true, RoleCode: "engineer", FieldName: "site", AccessLevel: "hidden"},
			{Enabled: true, RoleCode: "engineer", FieldName: "report_revenue", AccessLevel: "hidden"},
		},
		project: domain.Project{ID: "PJ-1", Name: "项目", Customer: "客户", Contract: "HT-1"},
		items:   []domain.ServiceItem{{ID: "SI-1", Site: "杭州机房", Requirement: "按标准执行"}},
	}
	service := &Service{Repo: repository}
	principal := principalWith("project.read", platform.DataScope{RoleCode: "engineer", ScopeType: "ORG", ScopeID: "org-1"})
	principal.Roles = []string{"engineer"}

	project, err := service.GetProject(context.Background(), principal, "PJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if project.Customer != maskedFieldValue {
		t.Fatalf("customer=%q want %q", project.Customer, maskedFieldValue)
	}
	if project.Name != "项目" || project.Contract != "HT-1" {
		t.Fatalf("unrelated project fields must not be masked: %+v", project)
	}

	items, err := service.ListServiceItems(context.Background(), principal, "PJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Site != maskedFieldValue {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Requirement != "按标准执行" {
		t.Fatalf("requirement should not be masked: %+v", items[0])
	}
}

// 白名单外的字段（如其它域的 report_revenue）不参与脱敏，未知字段名安全忽略。
func TestFieldPermissionIgnoresUnknownFields(t *testing.T) {
	repository := &maskingRepository{
		rules:   []domain.Rule{{Enabled: true, RoleCode: "engineer", FieldName: "report_revenue", AccessLevel: "hidden"}},
		project: domain.Project{ID: "PJ-1", Name: "项目", Customer: "客户", Contract: "HT-1"},
	}
	service := &Service{Repo: repository}
	principal := principalWith("project.read", platform.DataScope{RoleCode: "engineer", ScopeType: "ORG", ScopeID: "org-1"})
	principal.Roles = []string{"engineer"}
	project, err := service.GetProject(context.Background(), principal, "PJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if project.Customer != "客户" {
		t.Fatalf("customer=%q want 客户", project.Customer)
	}
}

// 非目标角色与未启用的规则不产生脱敏。
func TestFieldPermissionIgnoredForOtherRolesAndDisabledRules(t *testing.T) {
	repository := &maskingRepository{
		rules: []domain.Rule{
			{Enabled: true, RoleCode: "engineer", FieldName: "customer", AccessLevel: "hidden"},
			{Enabled: false, RoleCode: "team_lead", FieldName: "customer", AccessLevel: "hidden"},
		},
		project: domain.Project{ID: "PJ-1", Name: "项目", Customer: "客户", Contract: "HT-1"},
	}
	service := &Service{Repo: repository}
	principal := principalWith("project.read", platform.DataScope{RoleCode: "team_lead", ScopeType: "ORG", ScopeID: "org-1"})
	principal.Roles = []string{"team_lead"}
	project, err := service.GetProject(context.Background(), principal, "PJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if project.Customer != "客户" {
		t.Fatalf("customer=%q want 客户", project.Customer)
	}
}

// confirmScopeRepository 记录确认拆解是否把数据范围过滤转交仓储层（M4）。
type confirmScopeRepository struct {
	scopeRepository
	filter platform.ScopeFilter
	ids    []string
	err    error
	items  []domain.ServiceItem
}

func (r *confirmScopeRepository) ConfirmServiceItems(_ context.Context, filter platform.ScopeFilter, ids []string, _ string) ([]domain.ServiceItem, error) {
	r.filter = filter
	r.ids = ids
	return r.items, r.err
}

// 确认拆解不再走同步 Temporal：服务层把范围过滤连同 ids 一起交给仓储事务；
// 范围外的服务项由事务锁查询直接拒掉，而不是等读后写再校验。
func TestConfirmServiceItemsForwardsScopeFilterToRepository(t *testing.T) {
	repository := &confirmScopeRepository{items: []domain.ServiceItem{{ID: "SI-1", Status: "待分配"}}}
	service := &Service{Repo: repository}
	principal := principalWith("service_item.confirm", platform.DataScope{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-1"})

	items, err := service.ConfirmServiceItems(context.Background(), principal, []string{"SI-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "SI-1" {
		t.Fatalf("items=%+v", items)
	}
	if len(repository.filter.ProjectIDs) != 1 || repository.filter.ProjectIDs[0] != "PJ-1" {
		t.Fatalf("service must forward the project scope filter, got %+v", repository.filter)
	}
	if len(repository.ids) != 1 || repository.ids[0] != "SI-1" {
		t.Fatalf("ids=%v", repository.ids)
	}
}

// 范围外确认请求由仓储层直接返回 ErrNotFound，服务层原样透传而不是包裹成 500。
func TestConfirmServiceItemsOutOfScopePassthrough(t *testing.T) {
	repository := &confirmScopeRepository{err: ErrNotFound}
	service := &Service{Repo: repository}
	principal := principalWith("service_item.confirm", platform.DataScope{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-OTHER"})
	if _, err := service.ConfirmServiceItems(context.Background(), principal, []string{"SI-1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
}
