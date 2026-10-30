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

// TestImportProjectBindingPolicyValidation 验证NONE、历史政策及非法命令不能进入持久层。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingPolicyValidation(t *testing.T) {
	input := domain.ContractImportBindingCommand{SchemaVersion: 1, CommandID: strings.Repeat("a", 32), TenantID: "tenant", ContractID: "C1", ContractVersion: "1", OriginImportItemID: "item", SourceRevision: 1, PolicyDigest: strings.Repeat("b", 64), ReleaseVersion: 1, ImportMode: "CONTINUE_LIVE", ProjectMode: "CREATE_NEW", Relationship: "PRIMARY", TargetOwnerUserID: "owner", Contract: domain.ContractActivation{ContractID: "C1", ContractVersion: "1", ContractNumber: "HT1", Customer: "客户", CustomerID: "customer", EffectiveAt: time.Now(), Services: []domain.ContractService{{SourceID: "S1", Site: "站点", Batch: "一期", Category: "检测", TestMode: "STANDARD"}}}}
	if err := validateImportBinding(input); err != nil {
		t.Fatalf("valid=%v", err)
	}
	for _, scenario := range []string{"none", "archive", "version", "contract", "services", "owner", "relationship", "source", "long-number", "duplicate-service", "date"} {
		changed := input
		switch scenario {
		case "none":
			changed.ProjectMode = "NONE"
		case "archive":
			changed.ImportMode = "ARCHIVE_ONLY"
		case "version":
			changed.SchemaVersion = 2
		case "contract":
			changed.Contract.ContractID = "other"
		case "services":
			changed.Contract.Services = nil
		case "owner":
			changed.TargetOwnerUserID = ""
		case "relationship":
			changed.Relationship = "SUPPLEMENT"
		case "source":
			changed.SourceRevision = 0
		case "long-number":
			changed.Contract.ContractNumber = strings.Repeat("x", 65)
		case "duplicate-service":
			changed.Contract.Services = append(append([]domain.ContractService{}, input.Contract.Services...), input.Contract.Services[0])
		case "date":
			changed.Contract.EffectiveAt = time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
		}
		if err := validateImportBinding(changed); err == nil {
			t.Fatalf("scenario=%s accepted", scenario)
		}
	}
	service := Service{}
	p := platform.Principal{TenantID: "other", Permissions: map[string]bool{"project.contract.import": true}}
	if _, err := service.BindImportedContract(context.Background(), p, input, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross tenant=%v", err)
	}
	for _, activation := range []domain.ContractActivation{{OriginKind: "IMPORTED"}, {ImportMode: "ARCHIVE_ONLY"}, {OriginImportItemID: "item"}} {
		if _, err := service.ActivateContract(context.Background(), p, activation); !errors.Is(err, ErrImportPolicy) {
			t.Fatalf("legacy activation=%v", err)
		}
	}
}

type importedReferenceDirectory struct{ approvedContractVerifierStub }

// ListReferences 提供普通与历史来源混合目录以验证项目补建计数边界。
// 参数：ctx 为请求，after 为游标，limit 为分页大小。
// 返回值：两个引用及结束游标。
func (importedReferenceDirectory) ListReferences(ctx context.Context, after string, limit int) ([]platform.ApprovedContract, string, error) {
	return []platform.ApprovedContract{{ID: "normal"}, {ID: "normal-batch", OriginKind: "NORMAL", ImportMode: "NORMAL", OriginImportItemID: "batch-item"}, {ID: "archive", OriginKind: "IMPORTED", ImportMode: "ARCHIVE_ONLY", OriginImportItemID: "item"}}, "", nil
}

// TestImportedContractReferencesNeverBecomePendingProjects 验证补建提示不把历史来源当作普通合同。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportedContractReferencesNeverBecomePendingProjects(t *testing.T) {
	service := Service{Repo: &scopeRepository{}, Contracts: importedReferenceDirectory{}}
	var result domain.Dashboard
	service.populatePendingProjectCreation(context.Background(), "tenant", &result)
	if !result.PendingProjectCreationAvailable || result.PendingProjectCreation != 2 {
		t.Fatalf("pending=%+v", result)
	}
}
