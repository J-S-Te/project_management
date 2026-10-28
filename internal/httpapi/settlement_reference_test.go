package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

type settlementRefRepo struct {
	application.Repository
	observed platform.ScopeFilter
}

// GetProject 返回权威项目并观察可信租户。
// 参数：ctx 为请求，filter 为服务范围，id 为项目标识。
// 返回值：权威项目和错误。
func (r *settlementRefRepo) GetProject(ctx context.Context, filter platform.ScopeFilter, id string) (domain.Project, error) {
	r.observed = filter
	return domain.Project{ID: id, TenantID: filter.TenantID, ContractID: "C1", CustomerID: "42", Name: "项目", Version: 3, Status: "实施中"}, nil
}

// TestSettlementReferenceUsesVerifiedTenant 验证引用接口只接受独立机器租户并输出最小投影。
// 参数：t 为测试上下文。
// 返回值：无。
func TestSettlementReferenceUsesVerifiedTenant(t *testing.T) {
	repo := &settlementRefRepo{}
	verifier := &integrationVerifier{identity: platform.ServiceTokenIdentity{TenantID: "tenant", ApplicationCode: "settlement"}}
	router := httpapi.NewRouter(&application.Service{Repo: repo}, identity{}, nil, nil, httpapi.RouterOptions{SettlementReference: &httpapi.SettlementReferenceOptions{Enabled: true, BearerVerifier: verifier}})
	for _, token := range []string{"", "machine"} {
		req := httptest.NewRequest(http.MethodGet, "/internal/v1/settlement/project-references/P1?tenant_id=attacker", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if token == "" {
			if w.Code != 401 {
				t.Fatalf("无令牌=%d", w.Code)
			}
			continue
		}
		var response struct {
			Data struct {
				TenantID   string `json:"tenant_id"`
				ProjectID  string `json:"project_id"`
				ContractID string `json:"contract_id"`
				CustomerID string `json:"customer_id"`
				Version    uint64 `json:"version"`
				Active     bool   `json:"active"`
			}
		}
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		if w.Code != 200 || repo.observed.TenantID != "tenant" || response.Data.TenantID != "tenant" || response.Data.ProjectID != "P1" || response.Data.ContractID != "C1" || response.Data.CustomerID != "42" || response.Data.Version != 3 || !response.Data.Active {
			t.Fatalf("引用响应=%d %s 范围=%+v", w.Code, w.Body, repo.observed)
		}
	}
}
