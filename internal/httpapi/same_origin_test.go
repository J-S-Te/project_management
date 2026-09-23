package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// SEC-D11：/api/v1 写请求必须携带与 OIDC_REDIRECT_URI 一致的 Origin；
// 缺失、跨源、Sec-Fetch-Site=cross-site、配置缺失（失败关闭）一律被拒。
func TestPMSameOriginWriteRejectsMissingAndCrossOrigin(t *testing.T) {
	tests := []struct {
		name        string
		redirectURI string
		origin      string
		site        string
	}{
		{name: "missing origin", redirectURI: "http://example.com/project_management/auth/callback"},
		{name: "cross origin", redirectURI: "http://example.com/project_management/auth/callback", origin: "https://evil.example"},
		{name: "unexpected host", redirectURI: "http://example.com/project_management/auth/callback", origin: "http://attacker.example"},
		{name: "cross-site fetch metadata", redirectURI: "http://example.com/project_management/auth/callback", origin: "http://example.com", site: "cross-site"},
		{name: "empty redirect uri fails closed", redirectURI: "", origin: "http://example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OIDC_REDIRECT_URI", test.redirectURI)
			t.Setenv("OIDC_POST_LOGOUT_REDIRECT_URI", "")
			t.Setenv("APP_CORS_ALLOWED_ORIGINS", "")
			gin.SetMode(gin.TestMode)
			handler := NewRouter(nil, pmOriginTestIdentity{}, nil, discardLogger())
			request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.site != "" {
				request.Header.Set("Sec-Fetch-Site", test.site)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "PM_ORIGIN_REJECTED") {
				t.Fatalf("status=%d body=%s, want 403 PM_ORIGIN_REJECTED", response.Code, response.Body.String())
			}
		})
	}
}

// SEC-D11 正向基线：同源 Origin 放行进入业务链路（403 来自业务权限）；GET 不受影响。
func TestPMSameOriginWriteAllowsMatchingOriginAndLeavesReadsUntouched(t *testing.T) {
	t.Setenv("OIDC_REDIRECT_URI", "http://example.com/project_management/auth/callback")
	t.Setenv("OIDC_POST_LOGOUT_REDIRECT_URI", "")
	t.Setenv("APP_CORS_ALLOWED_ORIGINS", "")
	gin.SetMode(gin.TestMode)
	handler := NewRouter(nil, pmOriginTestIdentity{}, nil, discardLogger())

	write := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader("{}"))
	write.Header.Set("Content-Type", "application/json")
	write.Header.Set("Origin", "http://example.com")
	writeResponse := httptest.NewRecorder()
	handler.ServeHTTP(writeResponse, write)
	if writeResponse.Code != http.StatusForbidden || !strings.Contains(writeResponse.Body.String(), "PM_FORBIDDEN") {
		t.Fatalf("write status=%d body=%s, want business 403 (origin accepted)", writeResponse.Code, writeResponse.Body.String())
	}

	read := httptest.NewRequest(http.MethodGet, "/api/v1/navigation", nil)
	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, read)
	if strings.Contains(readResponse.Body.String(), "PM_ORIGIN_REJECTED") {
		t.Fatalf("reads must be exempt from origin checks, status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
}
