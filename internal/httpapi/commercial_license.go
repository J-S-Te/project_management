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

// Exact reviewed read routes: future GET endpoints do not inherit permission
// to compute or advance business merely because they use GET.
var commercialHistoryRoutes = map[string]bool{
	"/internal/v1/contracts/detection-categories": true, "/internal/v1/dashboard": true,
	"/api/v1/navigation": true, "/api/v1/role-catalog": true, "/api/v1/rule-configuration-catalog": true,
	"/api/v1/dashboard": true, "/api/v1/projects": true, "/api/v1/projects-monitoring": true,
	"/api/v1/approved-contracts": true, "/api/v1/approved-contracts/:contractID/service-items": true,
	"/api/v1/projects/:id": true, "/api/v1/delivery-events": true, "/api/v1/delivery/sla-overdue": true,
	"/api/v1/service-items": true, "/api/v1/service-items/:id/penetration-work-package": true,
	"/api/v1/service-items/:id/penetration-work-package/report-revisions": true, "/api/v1/service-items/:id/report-revisions": true,
	"/api/v1/personnel": true, "/api/v1/qualified-personnel": true, "/api/v1/personnel/names": true,
	"/api/v1/capabilities": true, "/api/v1/capabilities/import/template": true, "/api/v1/equipment": true,
	"/api/v1/service-items/:id/equipment-reservations": true, "/api/v1/equipment/import/template": true,
	"/api/v1/sites": true, "/api/v1/rules": true, "/api/v1/split-policy": true,
	"/api/v1/detection-categories": true, "/api/v1/detection-categories/import/template": true, "/api/v1/split-overrides": true,
}

func commercialOperation(method, path string) core.Operation {
	if method == http.MethodOptions {
		return core.ESSENTIAL_SERVICE
	}
	for _, route := range []string{"/healthz", "/readyz", "/auth/login", "/auth/callback", "/auth/logout", "/auth/local-logout", "/auth/backchannel-logout", "/logged-out", "/api/v1/auth/me"} {
		if path == route {
			return core.ESSENTIAL_SERVICE
		}
	}
	if method == http.MethodGet && path == "/api/v1/capabilities/export" {
		return core.EXPORT_HISTORY
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
