package httpapi

import (
	"context"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/metricstream"
	"github.com/j-s-te/project-management/internal/platform"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// StreamMetricFacts 为机器鉴权回归提供可观察租户的空流替身。
// 参数：ctx/tenant 为验签请求，output 为响应。
// 返回值：完整空流或输出错误。
func (r *metricFactsRepositoryStub) StreamMetricFacts(ctx context.Context, tenant string, output io.Writer) error {
	meta, _ := r.ReadMetricFacts(ctx, tenant)
	w, err := metricstream.New(output, meta)
	if err != nil {
		return err
	}
	return w.End("", "")
}

// TestProjectMetricStreamMachineBoundary 验证新流沿用机器范围且过期商业许可仍允许历史读取。
// 参数：t 为隔离HTTP测试上下文。
// 返回值：无；匿名放行、租户覆盖或错误许可分类时失败。
func TestProjectMetricStreamMachineBoundary(t *testing.T) {
	repo := &metricFactsRepositoryStub{}
	router := NewRouter(&application.Service{Repo: repo}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{CommercialLicense: expiredCommercialGate{}, DashboardIntegration: &DashboardIntegrationOptions{Enabled: true, BearerVerifier: handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "signed-tenant"}}}})
	for _, authorized := range []bool{false, true} {
		req := httptest.NewRequest("GET", "/internal/v1/dashboard/metrics/stream?tenant_id=forged", nil)
		req.Header.Set("X-DA-Tenant-ID", "forged")
		if authorized {
			req.Header.Set("Authorization", "Bearer synthetic")
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if !authorized {
			if out.Code != 401 {
				t.Fatalf("anonymous %d", out.Code)
			}
			continue
		}
		if out.Code != 200 || repo.tenant != "signed-tenant" || !strings.Contains(out.Body.String(), `"type":"end"`) {
			t.Fatalf("stream %d %s", out.Code, out.Body.String())
		}
	}
}
