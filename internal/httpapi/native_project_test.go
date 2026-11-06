package httpapi_test

import (
	"context"
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

type nativeHTTPRepository struct{ repo }

// CreateNativeProject 为HTTP边界测试记录真实应用层完成校验后的项目。
// 参数：ctx/filter 为请求范围，project/items 为待保存范围，fingerprint 为幂等摘要。
// 返回值：记录的项目，不访问外部服务。
func (r *nativeHTTPRepository) CreateNativeProject(_ context.Context, _ platform.ScopeFilter, project domain.Project, items []domain.ServiceItem, _ string) (domain.Project, error) {
	r.projects = append(r.projects, project)
	r.items = append(r.items, items...)
	return project, nil
}

// nativeHTTPRouter 创建具备指定原生能力和真实权限校验的HTTP测试入口。
// 参数：enabled 为显式开关，allowed 表示测试用户是否具有创建权限。
// 返回值：带真实应用服务和权限中间件的路由。
func nativeHTTPRouter(enabled, allowed bool) http.Handler {
	service := &application.Service{Repo: &nativeHTTPRepository{}, NativeEnabled: enabled}
	p := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Roles: []string{"business_admin"}, Permissions: map[string]bool{"project.create": allowed}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}}}
	return httpapi.NewRouter(service, identity{p: p}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestNativeProjectHTTPSourceBoundary 验证专用入口不能混用合同来源或绕过真实权限。
// 参数：t 为测试上下文。
// 返回值：无；状态码或受控来源错误时标记失败。
func TestNativeProjectHTTPSourceBoundary(t *testing.T) {
	body := `{"name":"原生项目","customer":"本地单位","customer_id":"UNIT-1","source_kind":"NATIVE","source_id":"operation-1","service_items":[{"category":"等保测评","site":"现场","test_mode":"STANDARD"}]}`
	response := perform(nativeHTTPRouter(true, true), http.MethodPost, "/api/v1/native-projects", body)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"source_kind":"NATIVE"`) || !strings.Contains(response.Body.String(), `"contract_id":""`) {
		t.Fatalf("native=%d %s", response.Code, response.Body.String())
	}
	for _, tc := range []struct {
		name             string
		enabled, allowed bool
		path, body       string
		status           int
	}{
		{"permission", true, false, "/api/v1/native-projects", body, http.StatusForbidden},
		{"disabled", false, true, "/api/v1/native-projects", body, http.StatusConflict},
		{"contract endpoint", true, true, "/api/v1/projects", body, http.StatusUnprocessableEntity},
		{"contract source", true, true, "/api/v1/native-projects", strings.Replace(body, `"NATIVE"`, `"CONTRACT"`, 1), http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := perform(nativeHTTPRouter(tc.enabled, tc.allowed), http.MethodPost, tc.path, tc.body)
			if r.Code != tc.status {
				t.Fatalf("status=%d expected=%d body=%s", r.Code, tc.status, r.Body.String())
			}
		})
	}
	capability := perform(nativeHTTPRouter(false, true), http.MethodGet, "/api/v1/project-creation-capabilities", "")
	if capability.Code != http.StatusOK || !strings.Contains(capability.Body.String(), `"native_enabled":false`) {
		t.Fatalf("capability=%s", capability.Body.String())
	}
}
