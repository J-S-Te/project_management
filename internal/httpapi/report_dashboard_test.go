package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type reportDashboardRepositoryStub struct {
	application.Repository
	tenant string
	err    error
}

// ReadReportDashboard 提供来源边界，HTTP仍使用真实应用权限校验。
// 参数：ctx为测试上下文，tenant为应用传入的可信租户。
// 返回值：合成空快照或指定源错误。
func (r *reportDashboardRepositoryStub) ReadReportDashboard(ctx context.Context, tenant string) (domain.ReportDashboard, error) {
	r.tenant = tenant
	return domain.ReportDashboard{Available: true, TenantID: tenant, Metrics: domain.ReportDashboardMetrics{StatusCounts: map[string]int64{}}}, r.err
}

// TestReportDashboardHTTPKeepsSignedTenantAndPlainSnapshot 验证机器租户与响应契约。
// 参数：t为测试上下文。
// 返回值：无，匿名放行、客户端改租户或失败伪成功时失败。
func TestReportDashboardHTTPKeepsSignedTenantAndPlainSnapshot(t *testing.T) {
	if commercialOperation("GET", "/internal/v1/dashboard/reports") != core.READ_HISTORY {
		t.Fatal("not READ_HISTORY")
	}
	for _, tc := range []struct {
		name, token string
		verifier    platform.ClientCredentialsTokenVerifier
		err         error
		want        int
	}{
		{"valid", "synthetic", handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "trusted"}}, nil, 200},
		{"anonymous", "", handoverVerifierStub{}, nil, 401},
		{"invalid", "synthetic", handoverVerifierStub{err: errors.New("invalid")}, nil, 401},
		{"unconfigured", "synthetic", nil, nil, 503},
		{"source-failure", "synthetic", handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "trusted"}}, errors.New("source failure"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &reportDashboardRepositoryStub{err: tc.err}
			router := NewRouter(&application.Service{Repo: repo}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{DashboardIntegration: &DashboardIntegrationOptions{Enabled: true, BearerVerifier: tc.verifier}})
			r := httptest.NewRequest(http.MethodGet, "/internal/v1/dashboard/reports?tenant_id=attacker", nil)
			r.Header.Set("X-Tenant-ID", "attacker")
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
			}
			if tc.want == 200 {
				var got domain.ReportDashboard
				if json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.Available || got.TenantID != "trusted" || repo.tenant != "trusted" {
					t.Fatalf("unexpected body=%s tenant=%s", w.Body.String(), repo.tenant)
				}
			}
		})
	}
}

// TestReportDashboardExpiredLicenseStillRequiresMachine 验证只读期不绕过机器认证。
// 参数：t为测试上下文。
// 返回值：无，只读期阻断历史读取或匿名放行时失败。
func TestReportDashboardExpiredLicenseStillRequiresMachine(t *testing.T) {
	repo := &reportDashboardRepositoryStub{}
	router := NewRouter(&application.Service{Repo: repo}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{CommercialLicense: expiredCommercialGate{}, DashboardIntegration: &DashboardIntegrationOptions{Enabled: true, BearerVerifier: handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "trusted"}}}})
	for _, bearer := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/internal/v1/dashboard/reports", nil)
		want := 401
		if bearer {
			r.Header.Set("Authorization", "Bearer synthetic")
			want = 200
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("bearer=%v HTTP=%d body=%s", bearer, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/internal/v1/dashboard/reports/other", "/internal/v1/dashboard/reports/"} {
		if commercialOperation("GET", path) != core.MUTATE_BUSINESS {
			t.Fatalf("unreviewed %s inherited history", path)
		}
	}
}
