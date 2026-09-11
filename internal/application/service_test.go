package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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
func (r *scopeRepository) ConfirmServiceItems(context.Context, string, []string, string) ([]domain.ServiceItem, error) {
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
	principal = principalWith("project.resource.read", platform.DataScope{RoleCode: "project_manager", ScopeType: "ORG", ScopeID: "org-1"})
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
