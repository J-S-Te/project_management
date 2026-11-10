package httpapi

import (
	"context"
	core "github.com/J-S-Te/license-core"
	"github.com/gin-gonic/gin"
	"net/http"
)

type CommercialLicenseGate interface {
	Check(context.Context, core.Operation) error
}

// 精确审查的读取路由：未来 GET 端点不能仅因使用 GET
// 就继承计算或推进业务的权限。
var commercialHistoryRoutes = map[string]bool{
	"/api/v1/projects/:id/reimbursement-spend":                              true,
	"/api/v1/projects/:id/expense-participants":                             true,
	"/internal/v1/reimbursement/spend-snapshots/reconciliation":             true,
	"/api/v1/projects/:id/native-scope":                                     true,
	"/internal/v1/contract-imports/capabilities":                            true,
	"/internal/v1/contract-imports/project-bindings/:origin_import_item_id": true,
	"/internal/v1/settlement/project-references/:projectID":                 true,
	"/internal/v1/dashboard/projects/snapshot":                              true,
	"/internal/v1/contracts/detection-categories":                           true, "/internal/v1/dashboard": true,
	"/internal/v1/handover/responsibilities": true, "/internal/v1/handover/transfers/:item_id": true,
	"/api/v1/navigation": true, "/api/v1/role-catalog": true, "/api/v1/rule-configuration-catalog": true,
	"/api/v1/project-creation-capabilities": true,
	"/api/v1/dashboard":                     true, "/api/v1/projects": true, "/api/v1/projects-monitoring": true,
	"/api/v1/approved-contracts": true, "/api/v1/approved-contracts/:contractID/service-items": true,
	// BIZ-XAPI-09：选择器搜索是只读目录查询，与上面的合同目录同属历史读取。
	"/api/v1/approved-contract-options":         true,
	"/api/v1/projects/:id/supplement-contracts": true,
	"/api/v1/projects/:id":                      true, "/api/v1/delivery-events": true, "/api/v1/delivery/sla-overdue": true,
	"/api/v1/service-items": true, "/api/v1/service-items/:id/penetration-work-package": true,
	// PRJ-01：现场证据/缺项判定已下推服务端（BIZ-PROJECT-06），该接口只回读事件流推算
	// 缺项，不写业务状态。漏分类会在许可到期只读期把它判成 MUTATE_BUSINESS，
	// 前端 fail-closed 因而无法渲染"现场完成"前置条件。
	"/api/v1/service-items/:id/field-evidence":                            true,
	"/api/v1/service-items/:id/penetration-work-package/report-revisions": true, "/api/v1/service-items/:id/report-revisions": true,
	"/api/v1/personnel": true, "/api/v1/qualified-personnel": true, "/api/v1/personnel/names": true,
	"/api/v1/capabilities": true, "/api/v1/capabilities/import/template": true, "/api/v1/equipment": true,
	"/api/v1/service-items/:id/equipment-reservations": true, "/api/v1/equipment/import/template": true,
	"/api/v1/sites": true, "/api/v1/rules": true, "/api/v1/split-policy": true,
	"/api/v1/detection-categories": true, "/api/v1/detection-categories/import/template": true, "/api/v1/split-overrides": true,
}

// commercialOperation 保持会话技术入口的原有许可分类边界。
// 参数：method为原始HTTP操作方法。
//
//	path为原始技术或业务路由。
//
// 返回值：与当前路由匹配的许可操作类别。
func commercialOperation(method, path string) core.Operation {
	if method == http.MethodGet && path == "/auth/session" || method == http.MethodPost && path == "/auth/activity" {
		return core.ESSENTIAL_SERVICE
	}
	if method == http.MethodOptions {
		return core.ESSENTIAL_SERVICE
	}
	// 审计恢复仅检查失败投递或重试不可变审计
	// 事件。将此精确方法和路径策略与业务重放分离。
	if (method == http.MethodGet && path == "/api/v1/audit-outbox/failed") ||
		(method == http.MethodPost && path == "/api/v1/audit-outbox/failed/:id/retry") {
		return core.ESSENTIAL_SERVICE
	}
	// 会话状态查询与显式活动续期和 /auth/me 同级：都属于基础会话服务，不因
	// 商业授权到期而阻断，避免用户无法登录/续期却看到"授权不可用"。
	for _, route := range []string{"/healthz", "/readyz", "/auth/login", "/auth/callback", "/auth/logout", "/auth/local-logout", "/auth/backchannel-logout", "/logged-out", "/api/v1/auth/me", "/api/v1/auth/activity"} {
		if path == route {
			return core.ESSENTIAL_SERVICE
		}
	}
	if method == http.MethodGet && path == "/api/v1/capabilities/export" {
		return core.EXPORT_HISTORY
	}
	if method == http.MethodPost && (path == "/internal/v1/reimbursement/project-references/search" || path == "/internal/v1/reimbursement/project-references/batch-validate") {
		return core.READ_HISTORY
	}
	if method == http.MethodGet && commercialHistoryRoutes[path] {
		return core.READ_HISTORY
	}
	return core.MUTATE_BUSINESS
}
func commercialLicenseMiddleware(gate CommercialLicenseGate) gin.HandlerFunc {
	return func(c *gin.Context) {
		if gate != nil && gate.Check(c.Request.Context(), commercialOperation(c.Request.Method, c.FullPath())) != nil {
			writeError(c, http.StatusForbidden, "COMMERCIAL_LICENSE_DENIED", "商业授权不可用或已到期；历史查询和导出仍受原权限控制")
			c.Abort()
			return
		}
		c.Next()
	}
}
