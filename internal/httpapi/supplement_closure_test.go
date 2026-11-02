package httpapi_test

// BIZ-PROJECT-04 的接口级回归：补充协议分支的退出只能走显式关账接口，
// 且必须同时具备拆解管理权限、同客户已审批的补充协议和原因。

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

func supplementClosureRouter(repository *repo, permissions map[string]bool) http.Handler {
	service := &application.Service{Repo: repository, Contracts: approvedContractVerifier{}}
	principal := platform.Principal{
		TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", DisplayName: "业务管理员",
		Permissions: permissions,
		DataScopes:  []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}},
	}
	return httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestSupplementClosureAPIRequiresPermissionContractAndReason(t *testing.T) {
	newRepository := func() *repo {
		return &repo{projects: []domain.Project{{
			ID: "PJ-CLOSE-1", TenantID: "tenant-1", Name: "关账用例", Customer: "示例客户",
			Contract: "HT-ORIG", ContractID: "HT-ORIG", CustomerID: "",
			SupplementStatus: "REQUIRED", Status: domain.ProjectStatusSupplementRequired,
		}}}
	}
	path := "/api/v1/projects/PJ-CLOSE-1/supplement-closure"

	denied := perform(supplementClosureRouter(newRepository(), map[string]bool{"project.read": true}), http.MethodPost, path,
		`{"supplement_contract_id":"HT-SUP","reason":"补充协议已回写"}`)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺少拆解管理权限 status=%d body=%s", denied.Code, denied.Body.String())
	}

	// 关账成功后接口回读项目状态，因此调用方还需要 project.read（与拆解调整接口一致）。
	handler := supplementClosureRouter(newRepository(), map[string]bool{"project.decomposition.manage": true, "project.read": true})
	missingReason := perform(handler, http.MethodPost, path, `{"supplement_contract_id":"HT-SUP"}`)
	if missingReason.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺少原因 status=%d body=%s", missingReason.Code, missingReason.Body.String())
	}
	missingContract := perform(handler, http.MethodPost, path, `{"reason":"补充协议已回写"}`)
	if missingContract.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺少补充协议 status=%d body=%s", missingContract.Code, missingContract.Body.String())
	}

	closedRepository := newRepository()
	closed := perform(supplementClosureRouter(closedRepository, map[string]bool{"project.decomposition.manage": true, "project.read": true}), http.MethodPost, path,
		`{"supplement_contract_id":"HT-SUP","reason":"补充协议已回写并确认范围"}`)
	if closed.Code != http.StatusOK {
		t.Fatalf("受控关账 status=%d body=%s", closed.Code, closed.Body.String())
	}
	if len(closedRepository.events) != 1 || closedRepository.events[0].Type != application.EventSupplementClosed {
		t.Fatalf("关账应写入 %s 事件: %+v", application.EventSupplementClosed, closedRepository.events)
	}
	if payload := closedRepository.events[0].Payload; payload["supplement_contract_id"] != "HT-SUP" || !strings.Contains(payload["reason"].(string), "补充协议已回写") {
		t.Fatalf("关账事件载荷 = %+v", payload)
	}
}

// 不在补充协议分支的项目不能重复关账：避免把正常项目误标成补充协议已处理。
func TestSupplementClosureAPIRejectsProjectOutsideSupplementBranch(t *testing.T) {
	repository := &repo{projects: []domain.Project{{
		ID: "PJ-CLOSE-2", TenantID: "tenant-1", Customer: "示例客户",
		Contract: "HT-ORIG", ContractID: "HT-ORIG", SupplementStatus: "NONE", Status: domain.ProjectStatusPendingDecomposition,
	}}}
	response := perform(supplementClosureRouter(repository, map[string]bool{"project.decomposition.manage": true, "project.read": true}), http.MethodPost,
		"/api/v1/projects/PJ-CLOSE-2/supplement-closure", `{"supplement_contract_id":"HT-SUP","reason":"重复关账"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "无需关账") {
		t.Fatalf("非补充协议分支 status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.events) != 0 {
		t.Fatalf("被拒绝的关账不得写事件: %+v", repository.events)
	}
}
