package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// 从 recheck/project_counterexample_test.go 纳入原生套件。
func TestReviewLegacyOriginalContractMustNotBecomeItsOwnSupplement(t *testing.T) {
	project := domain.Project{Contract: "HT-REVIEW-1", CustomerID: "review-customer", Customer: "Review Customer"}
	contract := platform.ApprovedContract{ID: "review-contract-id", Number: "HT-REVIEW-1", CustomerID: "review-customer", CustomerName: "Review Customer", Status: "active", ApprovalPassed: true}
	service := &Service{Contracts: supplementContractVerifier{contract: contract}}
	if err := service.verifyApprovedSupplementContract(context.Background(), project, contract.ID); err == nil {
		t.Fatal("the original contract was accepted as a supplement via its canonical id")
	}
}

type reviewAdjustmentRepository struct{ adjustmentRepository }

func (r *reviewAdjustmentRepository) ListDetectionCategories(context.Context, string) ([]domain.DetectionCategory, error) {
	return []domain.DetectionCategory{{Category: "等保测评", RequiredCodes: " core，EXTRA ; core ", SpecialMethod: domain.SpecialMethodNo, Enabled: true}}, nil
}

func TestReviewAdjustmentCarriesCategoryBaseline(t *testing.T) {
	repo := &reviewAdjustmentRepository{adjustmentRepository{project: domain.Project{ID: "PJ-1", ContractID: "C-1", CustomerID: "customer"}}}
	s := Service{Repo: repo, Contracts: supplementContractVerifier{contract: platform.ApprovedContract{ID: "SUP", CustomerID: "customer", Status: "active", ApprovalPassed: true}}}
	p := principalWith("project.decomposition.manage", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION"})
	err := s.AdjustDecomposition(context.Background(), p, "PJ-1", domain.DecompositionAdjustmentInput{Reason: "新增范围", SupplementContractID: "SUP", Items: []domain.ContractService{{SourceID: "S1", Site: "杭州机房", Batch: "第一批", Category: "等保测评", TestMode: "STANDARD"}}})
	if err != nil {
		t.Fatal(err)
	}
	items := repo.events[0].Payload["service_items"].([]domain.ServiceItem)
	if len(items) != 1 || !reflect.DeepEqual(items[0].RequiredCodes, []string{"CORE", "EXTRA"}) {
		t.Fatalf("adjusted baseline lost: %+v", items)
	}
	// 模拟指派边界已确认持久化的条目，不使用数据库。
	item := items[0]
	item.ID, item.Status = "SI-1", "待分配"
	assignmentRepo := &assignmentValidationRepository{assignmentRevokeRepository: assignmentRevokeRepository{item: item}}
	assignmentRepo.foundCapabilities = []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-MANAGER", UserID: "PM-1", IdentityStatus: domain.IdentityStatusActive, Status: "ACTIVE", Codes: []string{"CORE"}},
		{ResourceType: "PERSON", ResourceID: "P-ENGINEER", UserID: "ENG-1", IdentityStatus: domain.IdentityStatusActive, Status: "ACTIVE", Codes: []string{"CORE"}},
	}
	assignment := Service{Repo: assignmentRepo, Personnel: roleDirectoryStub{byRole: map[string][]string{assignmentRoleProjectManager: {"PM-1"}, assignmentRoleEngineer: {"ENG-1"}}}}
	p = principalWith("project.execution.assign", platform.DataScope{RoleCode: "team_lead", ScopeType: "APPLICATION"})
	result, err := assignment.AssignExecutionTeam(context.Background(), p, item.ID, domain.ExecutionAssignmentInput{ProjectManagerID: "P-MANAGER", EngineerIDs: []string{"P-ENGINEER"}})
	if err != nil || result.Passed || !contains(result.Conflicts, "缺少能力：EXTRA") {
		t.Fatalf("missing baseline must block assignment: %+v, %v", result, err)
	}
}

func reviewActivation() domain.ContractActivation {
	return domain.ContractActivation{ContractID: "canonical-id", ContractVersion: "2", Customer: "客户", EffectiveAt: time.Now(), Services: []domain.ContractService{{SourceID: "S1", Site: "杭州机房", Batch: "第一批", Category: "等保测评"}}}
}

func TestReviewActivationWithoutNumberFailsClosed(t *testing.T) {
	repo := &duplicateContractRepository{}
	s := Service{Repo: repo}
	p := principalWith("project.contract.import", platform.DataScope{RoleCode: "system_integration", ScopeType: "APPLICATION"})
	if _, err := s.ActivateContract(context.Background(), p, reviewActivation()); !errors.Is(err, ErrContractUnavailable) {
		t.Fatalf("missing authoritative identity accepted: %v", err)
	}
	if len(repo.activated) != 0 {
		t.Fatal("unresolved identity created a project")
	}
}

func TestReviewCompatibilityActivationMustNotGuessLegacyIdentityFromID(t *testing.T) {
	// 旧编号可能与稳定 ID 占用相同字符串命名空间。
	// 仅 ID 查询命中不能作为两者相同的权威证据。
	repo := &duplicateContractRepository{existing: domain.Project{ID: "PJ-OTHER", Contract: "canonical-id"}}
	s := Service{Repo: repo}
	p := principalWith("project.contract.import", platform.DataScope{RoleCode: "system_integration", ScopeType: "APPLICATION"})
	if _, err := s.ActivateContract(context.Background(), p, reviewActivation()); !errors.Is(err, ErrContractUnavailable) {
		t.Fatalf("guessed a legacy number from stable ID without authority: %v", err)
	}
	if repo.synced {
		t.Fatal("modified unrelated legacy project's stamp status")
	}
}

func TestReviewCompatibilityAuthorityNumberMismatchDoesNotSyncLegacyProject(t *testing.T) {
	repo := &duplicateContractRepository{existing: domain.Project{ID: "PJ-OTHER", Contract: "canonical-id"}}
	s := Service{Repo: repo, Contracts: supplementContractVerifier{contract: platform.ApprovedContract{ID: "canonical-id", Number: "HT-REAL", ApprovalPassed: true}}}
	p := principalWith("project.contract.import", platform.DataScope{RoleCode: "system_integration", ScopeType: "APPLICATION"})
	if _, err := s.ActivateContract(context.Background(), p, reviewActivation()); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("authority number mismatch accepted as legacy identity: %v", err)
	}
	if repo.synced || len(repo.activated) != 0 {
		t.Fatal("ambiguous legacy identity changed data")
	}
}

func TestReviewCompatibilityAuthorityMismatchAfterConcurrentInsert(t *testing.T) {
	repo := &duplicateContractRepository{existing: domain.Project{ID: "PJ-OTHER", Contract: "canonical-id"}, missOnce: true, duplicate: true}
	s := Service{Repo: repo, Contracts: supplementContractVerifier{contract: platform.ApprovedContract{ID: "canonical-id", Number: "HT-REAL", ApprovalPassed: true}}}
	p := principalWith("project.contract.import", platform.DataScope{RoleCode: "system_integration", ScopeType: "APPLICATION"})
	if _, err := s.ActivateContract(context.Background(), p, reviewActivation()); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("ambiguous concurrent identity accepted: %v", err)
	}
	if repo.synced {
		t.Fatal("ambiguous identity synced on duplicate recovery")
	}
}

func TestReviewActivationWithoutNumberResolvesAuthority(t *testing.T) {
	repo := &duplicateContractRepository{}
	s := Service{Repo: repo, Contracts: supplementContractVerifier{contract: platform.ApprovedContract{ID: "canonical-id", Number: "HT-LEGACY", ApprovalPassed: true}}}
	p := principalWith("project.contract.import", platform.DataScope{RoleCode: "system_integration", ScopeType: "APPLICATION"})
	project, err := s.ActivateContract(context.Background(), p, reviewActivation())
	if err != nil {
		t.Fatal(err)
	}
	if project.Contract != "HT-LEGACY" || len(repo.findContractNumbers) == 0 || repo.findContractNumbers[len(repo.findContractNumbers)-1] != "HT-LEGACY" {
		t.Fatalf("authority was not used before identity lookup: %+v %v", project, repo.findContractNumbers)
	}
}

type reviewLegacyIdentityRepository struct{ duplicateContractRepository }

func (r *reviewLegacyIdentityRepository) FindProjectByContract(_ context.Context, _ platform.ScopeFilter, id, number string) (domain.Project, error) {
	if number == "HT-LEGACY" {
		return r.existing, nil
	}
	return domain.Project{}, ErrNotFound
}

func TestReviewCompatibilityActivationReusesLegacyProjectAfterAuthorityLookup(t *testing.T) {
	for _, legacyID := range []string{"", "HT-LEGACY"} {
		t.Run("legacy ID="+legacyID, func(t *testing.T) {
			repo := &reviewLegacyIdentityRepository{duplicateContractRepository{existing: domain.Project{ID: "PJ-LEGACY", TenantID: "tenant-1", ContractID: legacyID, Contract: "HT-LEGACY", ContractVersion: "1"}}}
			s := Service{Repo: repo, Contracts: supplementContractVerifier{contract: platform.ApprovedContract{ID: "canonical-id", Number: "HT-LEGACY", ApprovalPassed: true}}}
			p := principalWith("project.contract.import", platform.DataScope{RoleCode: "system_integration", ScopeType: "APPLICATION"})
			project, err := s.ActivateContract(context.Background(), p, reviewActivation())
			if err != nil || project.ID != "PJ-LEGACY" || !repo.synced || len(repo.activated) != 0 {
				t.Fatalf("legacy identity split: %+v, %v, creates=%v", project, err, repo.activated)
			}
		})
	}
}

func TestReviewCompatibilityActivationRejectsUnresolvableAuthority(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contract platform.ApprovedContract
		err      error
	}{
		{"wrong identity", platform.ApprovedContract{ID: "different-id", Number: "HT-LEGACY", ApprovalPassed: true}, nil},
		{"missing number", platform.ApprovedContract{ID: "canonical-id", ApprovalPassed: true}, nil},
		{"not approved", platform.ApprovedContract{ID: "canonical-id", Number: "HT-LEGACY"}, nil},
		{"dependency failure", platform.ApprovedContract{}, errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &duplicateContractRepository{}
			s := Service{Repo: repo, Contracts: supplementContractVerifier{contract: tc.contract, err: tc.err}}
			p := principalWith("project.contract.import", platform.DataScope{RoleCode: "system_integration", ScopeType: "APPLICATION"})
			if _, err := s.ActivateContract(context.Background(), p, reviewActivation()); !errors.Is(err, ErrContractUnavailable) {
				t.Fatalf("unsafe authority accepted: %v", err)
			}
			if len(repo.activated) > 0 {
				t.Fatal("created project from unresolved identity")
			}
		})
	}
}
