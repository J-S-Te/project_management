package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/platform"
)

// TestMain 固定 CSRF 同源校验的允许源环境（t.Setenv 不适用于包级初始化）：
// 本包测试的写请求统一携带 Origin: http://example.com，与 OIDC_REDIRECT_URI 解析结果一致。
func TestMain(m *testing.M) {
	_ = os.Setenv("OIDC_REDIRECT_URI", "http://example.com/project_management/auth/callback")
	_ = os.Setenv("OIDC_POST_LOGOUT_REDIRECT_URI", "")
	_ = os.Setenv("APP_CORS_ALLOWED_ORIGINS", "")
	os.Exit(m.Run())
}

// stubAuditReporterPM 模拟平台审计 ingest 失败（凭据失效、网络故障等）。
type stubAuditReporterPM struct {
	err   error
	calls int
}

func (s *stubAuditReporterPM) Report(_ context.Context, _ platform.AuditEvent) error {
	s.calls++
	return s.err
}

// stubIdentityPM 返回不带任何权限的主体：路由层 require("project.create")
// 会写出 403 业务响应，与强制审计模式的 503 拒绝形成可区分的断言，
// 且全程不需要触碰业务 service。
type stubIdentityPM struct{}

func (stubIdentityPM) Authenticate(context.Context, *http.Request) (platform.Principal, error) {
	return platform.Principal{TenantID: "tenant-1", UserID: "user-1", DisplayName: "测试用户"}, nil
}

func newPMAuditTestRouter(audit platform.AuditReporter, logs *bytes.Buffer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	return NewRouter(nil, stubIdentityPM{}, audit, logger)
}

// SEC-D4b：强制审计模式下 reporter 缺失时必须在执行业务 handler 之前拒绝写入。
func TestPMAuditWriteRejectedWhenReporterMissingUnderRequiredAudit(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	var logs bytes.Buffer
	router := newPMAuditTestRouter(nil, &logs)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "PM_AUDIT_UNAVAILABLE") {
		t.Fatalf("body = %s, want PM_AUDIT_UNAVAILABLE", response.Body.String())
	}
	logged := logs.String()
	if !strings.Contains(logged, "audit reporter unavailable, rejecting request") {
		t.Fatalf("missing reject log: %s", logged)
	}
	if !strings.Contains(logged, "POST /api/v1/projects") || !strings.Contains(logged, "request_id") {
		t.Fatalf("reject log must contain route and request id: %s", logged)
	}
}

// SEC-D4b：强制审计模式下 Report 失败必须拒绝请求（503），业务 403 响应被丢弃。
func TestPMAuditWriteRejectedWhenReportFailsUnderRequiredAudit(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	var logs bytes.Buffer
	reporter := &stubAuditReporterPM{err: errors.New("platform audit ingest unavailable")}
	router := newPMAuditTestRouter(reporter, &logs)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "PM_AUDIT_WRITE_REJECTED") {
		t.Fatalf("body = %s, want PM_AUDIT_WRITE_REJECTED", response.Body.String())
	}
	if reporter.calls != 1 {
		t.Fatalf("Report calls = %d, want 1", reporter.calls)
	}
	logged := logs.String()
	if !strings.Contains(logged, "report platform audit failed") {
		t.Fatalf("missing report failure log: %s", logged)
	}
	if !strings.Contains(logged, "POST /api/v1/projects") || !strings.Contains(logged, "request_id") {
		t.Fatalf("failure log must contain route and request id: %s", logged)
	}
}

// SEC-D4b：非强制模式下 Report 失败不改变业务结果，但必须留下含路由与请求 id
// 的 error 日志（原来只有一行缺上下文的日志）。
func TestPMAuditWriteFailureLoggedWhenAuditNotRequired(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "false")
	var logs bytes.Buffer
	reporter := &stubAuditReporterPM{err: errors.New("platform audit ingest unavailable")}
	router := newPMAuditTestRouter(reporter, &logs)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (business outcome unchanged); body = %s", response.Code, http.StatusForbidden, response.Body.String())
	}
	logged := logs.String()
	if !strings.Contains(logged, "report platform audit failed") {
		t.Fatalf("missing report failure log: %s", logged)
	}
	if !strings.Contains(logged, "POST /api/v1/projects") || !strings.Contains(logged, "request_id") {
		t.Fatalf("failure log must contain route and request id: %s", logged)
	}
}

// SEC-D4b：强制审计模式下上报成功时，缓冲的业务响应必须原样提交。
func TestPMAuditWriteSuccessFlushesBusinessResponseUnderRequiredAudit(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	var logs bytes.Buffer
	reporter := &stubAuditReporterPM{}
	router := newPMAuditTestRouter(reporter, &logs)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusForbidden, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "PM_FORBIDDEN") {
		t.Fatalf("business body not flushed: %s", response.Body.String())
	}
	if got := response.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("pre-swap security headers lost after flush: X-Frame-Options = %q", got)
	}
	if reporter.calls != 1 {
		t.Fatalf("Report calls = %d, want 1", reporter.calls)
	}
	if strings.Contains(logs.String(), "report platform audit failed") {
		t.Fatalf("unexpected failure log: %s", logs.String())
	}
}
