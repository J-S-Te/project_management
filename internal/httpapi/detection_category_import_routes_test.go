package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectionCategoryImportRoutesRequireRuleManagement(t *testing.T) {
	t.Setenv("OIDC_REDIRECT_URI", "http://example.com/project_management/auth/callback")
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "false")
	t.Setenv("PLATFORM_ENVIRONMENT_CODE", "dev")
	handler := NewRouter(nil, pmOriginTestIdentity{}, nil, discardLogger())
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/detection-categories/import/template"},
		{http.MethodGet, "/api/v1/detection-categories/import/template?example=true"},
		{http.MethodPost, "/api/v1/detection-categories/import/preview"},
		{http.MethodPost, "/api/v1/detection-categories/import"},
	} {
		t.Run(route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			request.Header.Set("Origin", "http://example.com")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "PM_FORBIDDEN") {
				t.Fatalf("expected permission-protected route, status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
