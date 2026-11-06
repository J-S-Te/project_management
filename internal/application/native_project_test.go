package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// TestNativeProjectWithoutContract 验证原生委托可以独立于合同目录建立待确认范围。
// 参数：t 为测试上下文。
// 返回值：无；建项失败时标记测试失败。
func TestNativeProjectWithoutContract(t *testing.T) {
	var project domain.Project
	if err := json.Unmarshal([]byte(`{"name":"原生项目","customer":"委托单位","customer_id":"party-1","source_kind":"NATIVE","source_id":"request-1"}`), &project); err != nil {
		t.Fatal(err)
	}
	repo := &serviceProjectRepository{}
	service := &Service{Repo: repo, NativeEnabled: true}
	principal := principalWith("project.create", platform.DataScope{RoleCode: "business_admin", ScopeType: "SELF", ScopeID: "identity-1"})
	_, err := service.CreateNativeProject(context.Background(), principal, project, []domain.ContractService{{Site: "现场", Category: "等保测评", TestMode: "STANDARD"}})
	if err != nil {
		t.Fatalf("native project without contract should be creatable: %v", err)
	}
}

// CreateNativeProject 使用轻量仓储记录受控原生建项结果。
// 参数：ctx/filter 为请求范围，project/items 为项目范围，fingerprint 为幂等摘要。
// 返回值：记录后的项目，不访问外部系统。
func (r *serviceProjectRepository) CreateNativeProject(_ context.Context, _ platform.ScopeFilter, project domain.Project, items []domain.ServiceItem, _ string) (domain.Project, error) {
	r.created = project
	r.items = items
	return project, nil
}

// TestNativeProjectRejectsUnsafeInputs 验证原生开关、真实权限、组织和本地来源边界。
// 参数：t 为测试上下文。
// 返回值：无；任何非法输入落库时标记失败。
func TestNativeProjectRejectsUnsafeInputs(t *testing.T) {
	principal := principalWith("project.create", platform.DataScope{RoleCode: "business_admin", ScopeType: "SELF", ScopeID: "identity-1"})
	base := domain.Project{Name: "项目", Customer: "单位", CustomerID: "party-1", SourceKind: "NATIVE", SourceID: "request-1"}
	items := []domain.ContractService{{Site: "现场", Category: "等保测评", TestMode: "STANDARD"}}
	cases := []struct {
		name string
		edit func(*Service, *platform.Principal, *domain.Project, *[]domain.ContractService)
	}{
		{"disabled", func(s *Service, _ *platform.Principal, _ *domain.Project, _ *[]domain.ContractService) {
			s.NativeEnabled = false
		}},
		{"no permission", func(_ *Service, p *platform.Principal, _ *domain.Project, _ *[]domain.ContractService) {
			p.Permissions = map[string]bool{}
		}},
		{"wrong role", func(_ *Service, p *platform.Principal, _ *domain.Project, _ *[]domain.ContractService) {
			p.Roles = []string{"engineer"}
		}},
		{"organization", func(_ *Service, _ *platform.Principal, p *domain.Project, _ *[]domain.ContractService) {
			p.OwnerOrgID = "unavailable-org"
		}},
		{"contract source", func(_ *Service, _ *platform.Principal, p *domain.Project, _ *[]domain.ContractService) {
			p.SourceKind = "CONTRACT"
		}},
		{"contract id", func(_ *Service, _ *platform.Principal, p *domain.Project, _ *[]domain.ContractService) {
			p.ContractID = "fake-contract"
		}},
		{"contract approval", func(_ *Service, _ *platform.Principal, p *domain.Project, _ *[]domain.ContractService) {
			p.ContractVersion = "approved"
		}},
		{"party id", func(_ *Service, _ *platform.Principal, p *domain.Project, _ *[]domain.ContractService) {
			p.CustomerID = ""
		}},
		{"source id", func(_ *Service, _ *platform.Principal, p *domain.Project, _ *[]domain.ContractService) {
			p.SourceID = ""
		}},
		{"unknown category", func(_ *Service, _ *platform.Principal, _ *domain.Project, i *[]domain.ContractService) {
			(*i)[0].Category = "未知类别"
		}},
		{"invalid mode", func(_ *Service, _ *platform.Principal, _ *domain.Project, i *[]domain.ContractService) {
			(*i)[0].TestMode = "APPROVED"
		}},
		{"missing scope", func(_ *Service, _ *platform.Principal, _ *domain.Project, i *[]domain.ContractService) { *i = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &serviceProjectRepository{}
			service := &Service{Repo: repo, NativeEnabled: true}
			p := principal
			project := base
			selected := append([]domain.ContractService(nil), items...)
			tc.edit(service, &p, &project, &selected)
			if _, err := service.CreateNativeProject(context.Background(), p, project, selected); err == nil {
				t.Fatal("unsafe input accepted")
			}
			if repo.created.ID != "" {
				t.Fatal("invalid native project persisted")
			}
		})
	}
}

// TestNativeProjectKeepsPendingSpecialMethod 验证本地类别规定的方法和能力基线不能被客户端削弱。
// 参数：t 为测试上下文。
// 返回值：无；特殊方法未进入待确认时标记失败。
func TestNativeProjectKeepsPendingSpecialMethod(t *testing.T) {
	repo := &serviceProjectRepository{}
	service := &Service{Repo: repo, NativeEnabled: true}
	principal := principalWith("project.create", platform.DataScope{RoleCode: "business_admin", ScopeType: "SELF", ScopeID: "identity-1"})
	input := domain.Project{Name: "项目", Customer: "单位", CustomerID: "party-1", SourceKind: "NATIVE", SourceID: "request-1", Status: "已完成", Progress: 100, Contract: ""}
	created, err := service.CreateNativeProject(context.Background(), principal, input, []domain.ContractService{{Site: "现场", Category: "渗透测试", TestMode: "STANDARD"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != domain.ProjectStatusPendingDecomposition || created.Progress != 0 || created.ScopeVersion != 1 || created.ContractID != "" || repo.items[0].Status != domain.ServiceItemStatusPendingConfirm || repo.items[0].TestMode != "PENETRATION" {
		t.Fatalf("project=%+v items=%+v", created, repo.items)
	}
}

type nativeReadRepository struct {
	serviceProjectRepository
	project domain.Project
}

// GetProject 为来源边界测试返回指定项目而不访问业务库。
// 参数：ctx/filter 为读取上下文，id 为目标项目。
// 返回值：测试指定的原生项目。
func (r *nativeReadRepository) GetProject(_ context.Context, _ platform.ScopeFilter, _ string) (domain.Project, error) {
	return r.project, nil
}

// TestNativeProjectCannotUseContractAmendmentOrSettlement 验证原生来源不能冒用合同补充和结算引用。
// 参数：t 为测试上下文。
// 返回值：无；原生项目进入合同审批语义时标记失败。
func TestNativeProjectCannotUseContractAmendmentOrSettlement(t *testing.T) {
	repo := &nativeReadRepository{project: domain.Project{ID: "PJ-NATIVE", TenantID: "tenant-1", SourceKind: "NATIVE", CustomerID: "local-party", Version: 1}}
	service := &Service{Repo: repo, NativeEnabled: true}
	user := principalWith("project.decomposition.manage", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION"})
	if err := service.AdjustDecomposition(context.Background(), user, repo.project.ID, domain.DecompositionAdjustmentInput{Reason: "变更", SupplementContractID: "C-SUP"}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("native amendment=%v", err)
	}
	if _, err := service.ListSupplementContracts(context.Background(), user, repo.project.ID, 20); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("native supplement directory=%v", err)
	}
	machine := platform.Principal{TenantID: "tenant-1", Permissions: map[string]bool{"settlement.project_reference.read": true}}
	if _, err := service.ResolveSettlementProjectReference(context.Background(), machine, repo.project.ID); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("native settlement=%v", err)
	}
	if _, err := service.CreateProjectWithServiceItems(context.Background(), user, repo.project, nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("native through contract entry=%v", err)
	}
}
