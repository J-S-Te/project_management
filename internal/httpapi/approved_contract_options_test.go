package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
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

// optionSearchVerifier 模拟合同侧 approved-contract-references：
// 只回已审批合同、按 id 升序游标、keyword 匹配编号/标题/客户名，并记录收到的参数。
type optionSearchVerifier struct {
	items    []platform.ApprovedContract
	keyword  string
	afterID  string
	limit    int
	calls    int
	failWith error
}

func (v *optionSearchVerifier) List(context.Context, int) ([]platform.ApprovedContract, error) {
	return nil, nil
}

func (v *optionSearchVerifier) ListReferences(_ context.Context, keyword, afterID string, limit int) ([]platform.ApprovedContract, string, error) {
	v.calls++
	v.keyword, v.afterID, v.limit = keyword, afterID, limit
	if v.failWith != nil {
		return nil, "", v.failWith
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	keyword = strings.TrimSpace(keyword)
	result := make([]platform.ApprovedContract, 0, limit)
	next := ""
	for _, item := range v.items {
		if afterID != "" && item.ID <= afterID {
			continue
		}
		if keyword != "" && !strings.Contains(item.Number, keyword) && !strings.Contains(item.Title, keyword) && !strings.Contains(item.CustomerName, keyword) {
			continue
		}
		result = append(result, item)
		next = item.ID
		if len(result) >= limit {
			break
		}
	}
	if len(result) < limit {
		next = ""
	}
	return result, next, nil
}

func (v *optionSearchVerifier) Get(context.Context, string) (platform.ApprovedContract, error) {
	return platform.ApprovedContract{}, nil
}

func (v *optionSearchVerifier) GetServiceItems(context.Context, string) (platform.ApprovedContractServiceCatalog, error) {
	return platform.ApprovedContractServiceCatalog{}, nil
}

func optionSearchDirectory(total int) []platform.ApprovedContract {
	items := make([]platform.ApprovedContract, 0, total)
	for index := 1; index <= total; index++ {
		items = append(items, platform.ApprovedContract{
			ID: fmt.Sprintf("C-%04d", index), Number: fmt.Sprintf("HT-%04d", index),
			Title: fmt.Sprintf("测评服务 %03d", index), CustomerName: fmt.Sprintf("客户 %03d", index),
			Version: 1, Status: "active", ApprovalPassed: true, OriginKind: "NORMAL", ImportMode: "NORMAL",
		})
	}
	return items
}

func optionSearchPrincipal() platform.Principal {
	return platform.Principal{
		TenantID: "tenant-1", IdentityID: "admin-1", UserID: "admin-1", Roles: []string{"admin"},
		Permissions: map[string]bool{"project.create": true},
		DataScopes:  []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}},
	}
}

func optionSearchRouter(verifier platform.ApprovedContractVerifier, repository application.Repository) http.Handler {
	service := &application.Service{Repo: repository, Contracts: verifier}
	return httpapi.NewRouter(service, identity{p: optionSearchPrincipal()}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestApprovedContractOptionsSearchForwardsKeyword 固定 BIZ-XAPI-09 的项目侧契约：
// 新端点接收 keyword/after_id/limit 并把关键字透传给合同侧，返回 contracts + next_after_id。
func TestApprovedContractOptionsSearchForwardsKeyword(t *testing.T) {
	verifier := &optionSearchVerifier{items: optionSearchDirectory(250)}
	handler := optionSearchRouter(verifier, &repo{})

	response := perform(handler, http.MethodGet, "/api/v1/approved-contract-options?keyword=%20HT-0250%20&after_id=&limit=50", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if verifier.keyword != "HT-0250" || verifier.limit != 200 {
		t.Fatalf("keyword=%q limit=%d", verifier.keyword, verifier.limit)
	}
	var body struct {
		Data struct {
			Contracts   []platform.ApprovedContract `json:"contracts"`
			NextAfterID string                      `json:"next_after_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// 关键字命中首屏 200 条窗口之外的第 250 条：选择器必须能选中它。
	if len(body.Data.Contracts) != 1 || body.Data.Contracts[0].ID != "C-0250" {
		t.Fatalf("contracts=%+v", body.Data.Contracts)
	}
	if body.Data.NextAfterID != "" {
		t.Fatalf("next_after_id=%q，want 空", body.Data.NextAfterID)
	}
}

// TestApprovedContractOptionsHideContractsAlreadyLinkedToProjects 固定"已建项不可选"：
// 即使合同仍在可搜索目录里，只要租户内已有项目引用它，就不能被再次选择。
func TestApprovedContractOptionsHideContractsAlreadyLinkedToProjects(t *testing.T) {
	verifier := &optionSearchVerifier{items: optionSearchDirectory(5)}
	repository := &repo{projects: []domain.Project{{ID: "PJ-1", TenantID: "tenant-1", ContractID: "C-0001", Contract: "HT-0001"}}}
	handler := optionSearchRouter(verifier, repository)

	response := perform(handler, http.MethodGet, "/api/v1/approved-contract-options?keyword=HT-0001", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"contracts":[]`) {
		t.Fatalf("status=%d body=%s，want 空可用合同列表", response.Code, response.Body.String())
	}
}

// TestApprovedContractOptionsRejectsInvalidParams 固定参数上限：limit>100、keyword>100 均为 422，
// 与旧的 /approved-contracts（limit≤200）互不影响。
func TestApprovedContractOptionsRejectsInvalidParams(t *testing.T) {
	longKeyword := strings.Repeat("字", 101)
	for _, path := range []string{
		"/api/v1/approved-contract-options?limit=101",
		"/api/v1/approved-contract-options?limit=0",
		"/api/v1/approved-contract-options?keyword=" + longKeyword,
	} {
		verifier := &optionSearchVerifier{items: optionSearchDirectory(3)}
		response := perform(optionSearchRouter(verifier, &repo{}), http.MethodGet, path, "")
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "PM_VALIDATION_ERROR") {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if verifier.calls != 0 {
			t.Fatalf("%s 非法参数不应触达合同依赖", path)
		}
	}
}

// TestApprovedContractOptionsRequireCreatePermission 固定权限：只读角色 403。
func TestApprovedContractOptionsRequireCreatePermission(t *testing.T) {
	service := &application.Service{Repo: &repo{}, Contracts: &optionSearchVerifier{items: optionSearchDirectory(3)}}
	principal := optionSearchPrincipal()
	principal.Permissions = map[string]bool{"project.read": true}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/api/v1/approved-contract-options?keyword=HT", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// TestApprovedContractOptionsKeepsContractDependencyFailure 固定 fail-closed：
// 合同依赖失败返回 503 PM_CONTRACT_UNAVAILABLE，不能退化成"没有可用合同"。
func TestApprovedContractOptionsKeepsContractDependencyFailure(t *testing.T) {
	verifier := &optionSearchVerifier{failWith: fmt.Errorf("contract service down")}
	response := perform(optionSearchRouter(verifier, &repo{}), http.MethodGet, "/api/v1/approved-contract-options?keyword=HT", "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "PM_CONTRACT_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
