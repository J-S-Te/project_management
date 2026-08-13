package application

import (
	"context"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type scopeRepository struct {
	lastFilter platform.ScopeFilter
	created    domain.Project
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
func (r *scopeRepository) SetRuleEnabled(_ context.Context, _ string, id int64, enabled bool, _ string) (domain.Rule, error) {
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
