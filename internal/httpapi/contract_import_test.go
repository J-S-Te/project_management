package httpapi_test

import (
	"github.com/j-s-te/project-management/internal/platform"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/httpapi"
)

// TestImportProjectBindingScopeConflict 验证新增机器入口缺验签器时失败关闭。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingScopeConflict(t *testing.T) {
	router := httpapi.NewRouter(&application.Service{}, identity{}, nil, nil)
	for _, path := range []string{"/internal/v1/contract-imports/capabilities", "/internal/v1/contract-imports/project-bindings/item"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("path=%s status=%d want=503", path, w.Code)
		}
	}
}

// TestImportProjectBindingMachineBoundary 核对机器caller、缺令牌和查询可信租户。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingMachineBoundary(t *testing.T) {
	verifier := &integrationVerifier{identity: platform.ServiceTokenIdentity{TenantID: "tenant", ApplicationCode: "contract_management"}}
	router := httpapi.NewRouter(&application.Service{}, identity{}, nil, nil, httpapi.RouterOptions{ContractImport: &httpapi.ContractImportOptions{Enabled: true, BearerVerifier: verifier}})
	for _, scenario := range []string{"no-token", "wrong-caller", "capability"} {
		verifier.identity.ApplicationCode = "contract_management"
		req := httptest.NewRequest(http.MethodGet, "/internal/v1/contract-imports/capabilities?tenant_id=attacker", nil)
		if scenario != "no-token" {
			req.Header.Set("Authorization", "Bearer machine")
		}
		if scenario == "wrong-caller" {
			verifier.identity.ApplicationCode = "settlement"
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if scenario == "capability" {
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"tenant_id":"tenant"`) || !strings.Contains(w.Body.String(), `"project_modes":["CREATE_NEW","LINK_EXISTING"]`) {
				t.Fatalf("capability=%d %s", w.Code, w.Body)
			}
		} else if w.Code != 401 {
			t.Fatalf("%s=%d", scenario, w.Code)
		}
	}
	verifier.identity.ApplicationCode = "contract_management"
	for _, body := range []string{`{"tenant_id":"attacker"}`, `{"unknown_field":true}`, `{} {}`} {
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/contract-imports/project-bindings", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer machine")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 403 && w.Code != 422 {
			t.Fatalf("invalid body=%d %s", w.Code, w.Body)
		}
	}
}
