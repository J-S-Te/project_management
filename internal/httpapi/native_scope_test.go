package httpapi_test

import (
	"context"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
	"io"
	"log/slog"
	"net/http"
	"testing"
)

type nativeScopeHTTPRepo struct{ nativeHTTPRepository }

// ReadNativeScope 返回HTTP授权测试的真实格式状态。
// 参数：ctx/filter/id 为服务传入的可信坐标。
// 返回值：不访问外部系统的合成范围。
func (r *nativeScopeHTTPRepo) ReadNativeScope(_ context.Context, _ platform.ScopeFilter, id string) (domain.NativeScopeState, error) {
	return domain.NativeScopeState{ProjectID: id, ScopeVersion: 1, ProjectVersion: 1, Changes: []domain.NativeScopeChange{}}, nil
}

// RequestNativeScope 返回HTTP链已受理的待审批提案。
// 参数：ctx/filter/id/actor 为可信身份，in/items 为验证后范围。
// 返回值：明确待审批回执。
func (r *nativeScopeHTTPRepo) RequestNativeScope(_ context.Context, _ platform.ScopeFilter, id, actor string, in domain.NativeScopeInput, items []domain.ServiceItem) (domain.NativeScopeChange, error) {
	return domain.NativeScopeChange{ID: "change", ProjectID: id, RequestedBy: actor, Status: "PENDING", OperationID: in.OperationID, Version: 1, ServiceItems: items}, nil
}

// DecideNativeScope 返回HTTP链提交的明确决定。
// 参数：ctx/filter/project/id/actor 为坐标，in 为验证后决定。
// 返回值：合成最终回执。
func (r *nativeScopeHTTPRepo) DecideNativeScope(_ context.Context, _ platform.ScopeFilter, project, id, actor string, in domain.NativeScopeDecision) (domain.NativeScopeChange, error) {
	return domain.NativeScopeChange{ID: id, ProjectID: project, DecidedBy: actor, Status: in.Decision, Version: 2}, nil
}

// TestNativeScopeHTTPPermissions 验证真实路由保留权限、精确角色和非法决定拒绝。
// 参数：t 为测试上下文。
// 返回值：无；默认关闭、无权或错误角色可写时失败。
func TestNativeScopeHTTPPermissions(t *testing.T) {
	body := `{"operation_id":"op-1","expected_version":1,"decision":"APPROVE","comment":"核验通过"}`
	for _, tc := range []struct {
		role                string
		enabled, permission bool
		status              int
	}{
		{"technical_director", true, true, 200}, {"admin", true, true, 200}, {"system_admin", true, true, 403}, {"business_admin", true, true, 403}, {"technical_director", false, true, 409}, {"technical_director", true, false, 403},
	} {
		p := platform.Principal{TenantID: "tenant-1", IdentityID: "u-1", UserID: "u-1", Roles: []string{tc.role}, Permissions: map[string]bool{"project.native_scope.approve": tc.permission, "project.read": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
		router := httpapi.NewRouter(&application.Service{NativeEnabled: tc.enabled, Repo: &nativeScopeHTTPRepo{}}, identity{p: p}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		got := perform(router, http.MethodPost, "/api/v1/projects/PJ-1/native-scope-changes/change/decision", body)
		if got.Code != tc.status {
			t.Fatalf("%s enabled=%v status=%d body=%s", tc.role, tc.enabled, got.Code, got.Body.String())
		}
	}
}

// TestNativeScopeApproverReadsModeWithoutCreate 固定审批员可读取模式元数据而不能借此建项。
// 参数：t 为测试上下文。
// 返回值：无；读取被创建权限阻断或模式标记授予写权限时失败。
func TestNativeScopeApproverReadsModeWithoutCreate(t *testing.T) {
	p := platform.Principal{TenantID: "tenant-1", IdentityID: "director", UserID: "director", Roles: []string{"technical_director"}, Permissions: map[string]bool{"project.read": true, "project.native_scope.approve": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	router := httpapi.NewRouter(&application.Service{NativeEnabled: true, Repo: &nativeScopeHTTPRepo{}}, identity{p: p}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := perform(router, http.MethodGet, "/api/v1/project-creation-capabilities", "")
	if got.Code != http.StatusOK {
		t.Fatalf("mode read=%d %s", got.Code, got.Body.String())
	}
	got = perform(router, http.MethodPost, "/api/v1/native-projects", `{"source_kind":"NATIVE"}`)
	if got.Code != http.StatusForbidden {
		t.Fatalf("read flag granted create=%d", got.Code)
	}
}

// TestNativeScopeRejectsForeignSourceFields 验证专用本地入口不接受合同来源或客户端审批声明。
// 参数：t 为测试上下文。
// 返回值：无；未知来源字段被静默接受时失败。
func TestNativeScopeRejectsForeignSourceFields(t *testing.T) {
	p := platform.Principal{TenantID: "tenant-1", IdentityID: "business", UserID: "business", Roles: []string{"business_admin"}, Permissions: map[string]bool{"project.decomposition.manage": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	router := httpapi.NewRouter(&application.Service{NativeEnabled: true, Repo: &nativeScopeHTTPRepo{}}, identity{p: p}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := perform(router, http.MethodPost, "/api/v1/projects/PJ-1/native-scope-changes", `{"operation_id":"op","base_scope_version":1,"expected_project_version":1,"reason":"修订","source_kind":"CONTRACT","contract_id":"fake","approved":true,"service_items":[{"category":"等保测评","site":"现场"}]}`)
	if got.Code != http.StatusBadRequest {
		t.Fatalf("foreign source=%d %s", got.Code, got.Body.String())
	}
}

// ReplayNativeScope 保持HTTP边界替身只测试当前请求且不伪造已提交历史。
// 参数：ctx/filter/id/actor/in 为真实服务传入的坐标与请求。
// 返回值：未找到历史，不冒充持久回执。
func (r *nativeScopeHTTPRepo) ReplayNativeScope(_ context.Context, _ platform.ScopeFilter, _, _ string, _ domain.NativeScopeInput) (domain.NativeScopeChange, bool, error) {
	return domain.NativeScopeChange{}, false, nil
}
