package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
)

type crmProjectsRepo struct {
	application.Repository
	tenant, customer string
	page, pageSize   int
}

// CRMCustomerProjects 捕获可信租户和精确分页条件。
// 参数：上下文、租户、客户主键、页码和页大小。
// 返回值：一条客户项目摘要及总数。
func (r *crmProjectsRepo) CRMCustomerProjects(
	_ context.Context, tenant, customer string, page, pageSize int,
) ([]application.CRMProjectSummary, int64, error) {
	r.tenant, r.customer, r.page, r.pageSize = tenant, customer, page, pageSize
	return []application.CRMProjectSummary{{
		ProjectID: "P-1", ProjectName: "测试项目", Status: "实施中",
		Progress: 40, SourceUpdatedAt: time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC),
	}}, 1, nil
}

// TestCRMProjectHistoryBindsCustomerAndMachineTenant 验证接口范围和最小响应。
// 参数：t 为测试上下文。
// 返回值：无。
func TestCRMProjectHistoryBindsCustomerAndMachineTenant(t *testing.T) {
	const path = "/internal/v1/crm/customers/42/projects"
	for _, tc := range []struct {
		name, caller, token, query string
		status                     int
	}{
		{"success", "customer_and_opportunity", "machine", "?page=1&page_size=5", 200},
		{"no bearer", "customer_and_opportunity", "", "", 401},
		{"different app", "settlement", "machine", "", 401},
		{"tenant query", "customer_and_opportunity", "machine", "?tenant_id=other", 422},
		{"duplicate page", "customer_and_opportunity", "machine", "?page=1&page=2", 422},
		{"oversized page", "customer_and_opportunity", "machine", "?page_size=101", 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &crmProjectsRepo{}
			router := NewRouter(&application.Service{Repo: repo}, nil, nil,
				slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{
					CRMProjectHistory: handoverVerifierStub{identity: platform.ServiceTokenIdentity{
						TenantID: "signed-tenant", ApplicationCode: tc.caller,
					}},
				})
			req := httptest.NewRequest("GET", path+tc.query, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			req.Header.Set("X-Tenant-ID", "unsafe-header")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want %d: %s", w.Code, tc.status, w.Body)
			}
			if w.Code != 200 {
				if repo.tenant != "" {
					t.Fatal("rejected request reached repository")
				}
				return
			}
			if repo.tenant != "signed-tenant" || repo.customer != "42" || repo.page != 1 || repo.pageSize != 5 {
				t.Fatalf("query scope: %+v", repo)
			}
			var body struct {
				Data struct {
					Items []map[string]any
					Total int
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Data.Total != 1 ||
				len(body.Data.Items) != 1 || body.Data.Items[0]["project_id"] != "P-1" {
				t.Fatalf("unexpected response: %s (%v)", w.Body, err)
			}
			if _, present := body.Data.Items[0]["tenant_id"]; present {
				t.Fatal("tenant leaked into project summary")
			}
		})
	}
}
