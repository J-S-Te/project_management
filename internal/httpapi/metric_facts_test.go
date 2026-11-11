package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type metricFactsRepositoryStub struct {
	application.Repository
	application.MetricFactsRepository
	tenant string
}

// ReadMetricFacts 为HTTP授权边界提供明确的最小来源，未替代数据库验收。
// 参数：ctx 为请求，tenant 为服务端可信机器租户。
// 返回值：完整空数组的合成快照，不含他租户数据。
func (r *metricFactsRepositoryStub) ReadMetricFacts(ctx context.Context, tenant string) (domain.MetricFactsSnapshot, error) {
	r.tenant = tenant
	return domain.MetricFactsSnapshot{SchemaVersion: 1, TenantID: tenant, Complete: true, Rows: []domain.MetricFactRow{}}, nil
}

// TestMetricFactsHTTPMachineTenantAndEnvelope 验证专用机器身份、READ_HISTORY和兼容包络。
// 参数：t 为测试上下文。
// 返回值：无；匿名放行、客户端覆盖租户或返回非约定包络时失败。
func TestMetricFactsHTTPMachineTenantAndEnvelope(t *testing.T) {
	repo := &metricFactsRepositoryStub{}
	router := NewRouter(&application.Service{Repo: repo}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{CommercialLicense: expiredCommercialGate{}, DashboardIntegration: &DashboardIntegrationOptions{Enabled: true, BearerVerifier: handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "signed-tenant"}}}})
	for _, token := range []string{"", "synthetic"} {
		request := httptest.NewRequest("GET", "/internal/v1/dashboard/metrics?tenant_id=attacker", nil)
		request.Header.Set("X-Tenant-ID", "attacker")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if token == "" {
			if response.Code != 401 {
				t.Fatalf("anonymous status %d", response.Code)
			}
			continue
		}
		var envelope struct {
			Code string                     `json:"code"`
			Data domain.MetricFactsSnapshot `json:"data"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Code != "OK" || envelope.Data.TenantID != "signed-tenant" || repo.tenant != "signed-tenant" || !envelope.Data.Complete || envelope.Data.Rows == nil {
			t.Fatalf("bad signed response %d %s", response.Code, response.Body.String())
		}
	}
}
