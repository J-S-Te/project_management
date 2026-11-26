package httpapi_test

import (
	"bytes"
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

const fieldReturnPath = "/api/v1/service-items/SI-1/field-return"
const fieldReturnBody = `{"reason":" 补核现场条件 ","expected_version":7}`

// fieldReturnHandler 使用真实路由和业务服务来验证直退请求。
// 参数：status 为服务项状态，p 为访问身份，gate 为可选的商业许可检查。
// 返回值：隔离的 HTTP 路由和记录交付事件的测试仓储。
func fieldReturnHandler(status string, p platform.Principal, gate httpapi.CommercialLicenseGate) (http.Handler, *repo) {
	repository := &repo{items: []domain.ServiceItem{{
		ID: "SI-1", TenantID: "tenant-1", ProjectID: "PJ-1",
		Status: status, Version: 7, ProjectManagerID: p.UserID,
	}}}
	router := httpapi.NewRouter(&application.Service{Repo: repository}, identity{p: p}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{CommercialLicense: gate})
	return router, repository
}

// fieldReturnPrincipal 提供具有当前操作所需会话范围的项目经理。
// 参数：无。
// 返回值：仅拥有回退操作权的项目经理身份。
func fieldReturnPrincipal() platform.Principal {
	p := fieldEvidencePrincipal("manager-1", map[string]bool{"project.rollback.request": true},
		[]platform.DataScope{{RoleCode: "project_manager", ScopeType: "APPLICATION"}})
	p.Roles = []string{"project_manager"}
	return p
}

// TestFieldReturnWritesDirectTransition 验证现场直退产生独立业务事件而非待审批请求。
// 参数：t 为 HTTP 回归测试上下文。
// 返回值：无；错误状态或审计身份丢失时失败。
func TestFieldReturnWritesDirectTransition(t *testing.T) {
	principal := fieldReturnPrincipal()
	router, repository := fieldReturnHandler("实施中", principal, nil)
	response := perform(router, http.MethodPost, fieldReturnPath, fieldReturnBody)
	if response.Code != http.StatusOK ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"status":"实施准备中"`)) {
		t.Fatalf("现场直退响应=%d %s", response.Code, response.Body)
	}
	if len(repository.events) != 1 {
		t.Fatalf("交付事件数=%d，期望 1", len(repository.events))
	}
	event := repository.events[0]
	if event.Type != "FIELD_RETURNED_TO_PREPARATION" || event.ActorUserID != principal.UserID ||
		event.TenantID != principal.TenantID || event.ServiceItemID != "SI-1" ||
		event.Payload["reason"] != "补核现场条件" || event.Payload["expected_version"] != uint64(7) {
		t.Fatalf("直退事件未保留命令、原因或真实操作人：%+v", event)
	}
}

// TestFieldReturnRejectsInvalidInputs 验证直退要求当前版本、原因及实施中状态。
// 参数：t 为 HTTP 回归测试上下文。
// 返回值：无；不合法请求写入事件时失败。
func TestFieldReturnRejectsInvalidInputs(t *testing.T) {
	for _, entry := range []struct {
		name, status, body, code string
		want                     int
	}{
		{"无版本", "实施中", `{"reason":"补核条件"}`, "PM_VALIDATION_ERROR", http.StatusUnprocessableEntity},
		{"原因空白", "实施中", `{"reason":" ","expected_version":7}`, "PM_VALIDATION_ERROR", http.StatusUnprocessableEntity},
		{"旧版本", "实施中", `{"reason":"补核条件","expected_version":6}`, "PM_STATE_CONFLICT", http.StatusConflict},
		{"已现场完成", "现场实施完成", fieldReturnBody, "PM_PRECONDITION_FAILED", http.StatusConflict},
	} {
		t.Run(entry.name, func(t *testing.T) {
			router, repository := fieldReturnHandler(entry.status, fieldReturnPrincipal(), nil)
			response := perform(router, http.MethodPost, fieldReturnPath, entry.body)
			if response.Code != entry.want || !strings.Contains(response.Body.String(), entry.code) ||
				len(repository.events) != 0 {
				t.Fatalf("无效直退响应=%d %s，交付事件数=%d", response.Code, response.Body, len(repository.events))
			}
		})
	}
}

// TestFieldReturnEnforcesPermissionScopeAndLicense 验证直退沿用授权、数据范围与许可门禁。
// 参数：t 为 HTTP 回归测试上下文。
// 返回值：无；受限请求进入写入时失败。
func TestFieldReturnEnforcesPermissionScopeAndLicense(t *testing.T) {
	noPermission, noScope := fieldReturnPrincipal(), fieldReturnPrincipal()
	noPermission.Permissions = map[string]bool{"project.field.execute": true}
	noScope.Roles, noScope.DataScopes = nil, nil
	for _, entry := range []struct {
		name string
		p    platform.Principal
		gate httpapi.CommercialLicenseGate
	}{
		{"仅现场执行", noPermission, nil},
		{"无数据范围", noScope, nil},
		{"商业许可只读", fieldReturnPrincipal(), expiredLicenseGate{}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			router, repository := fieldReturnHandler("实施中", entry.p, entry.gate)
			response := perform(router, http.MethodPost, fieldReturnPath, fieldReturnBody)
			if response.Code != http.StatusForbidden || len(repository.events) != 0 {
				t.Fatalf("受限直退响应=%d %s，交付事件数=%d", response.Code, response.Body, len(repository.events))
			}
			if entry.gate != nil && !strings.Contains(response.Body.String(), "COMMERCIAL_LICENSE_DENIED") {
				t.Fatalf("直退未进入商业许可写入门禁：%s", response.Body)
			}
		})
	}
}
