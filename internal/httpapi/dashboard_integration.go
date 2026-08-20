package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/platform"
)

// DashboardIntegrationOptions 数据看板系统（服务器到服务器）机器接入配置。
type DashboardIntegrationOptions struct {
	Enabled        bool
	BearerVerifier platform.ClientCredentialsTokenVerifier
}

// authenticateDashboardIntegration 数据看板系统机器接入：Bearer Keycloak 机器令牌
// + 全量项目读范围（看板为只读消费方）。
func (h *Handler) authenticateDashboardIntegration(options DashboardIntegrationOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !options.Enabled {
			writeError(c, http.StatusNotFound, "PM_DASHBOARD_INTEGRATION_DISABLED", "接口未启用")
			c.Abort()
			return
		}
		if options.BearerVerifier == nil {
			writeError(c, http.StatusServiceUnavailable, "PM_DASHBOARD_AUTH_UNAVAILABLE", "看板系统机器身份校验未配置")
			c.Abort()
			return
		}
		const bearerPrefix = "Bearer "
		authorization := strings.TrimSpace(c.GetHeader("Authorization"))
		if !strings.HasPrefix(authorization, bearerPrefix) || strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix)) == "" {
			writeError(c, http.StatusUnauthorized, "PM_DASHBOARD_BEARER_REQUIRED", "看板系统调用必须携带机器访问令牌")
			c.Abort()
			return
		}
		identity, err := options.BearerVerifier.VerifyClientCredentials(c.Request.Context(), strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix)))
		if err != nil {
			writeError(c, http.StatusUnauthorized, "PM_DASHBOARD_BEARER_INVALID", "看板系统机器访问令牌无效")
			c.Abort()
			return
		}
		tenantID := identity.TenantID
		c.Set("principal", platform.Principal{
			Subject:     "data_analysis",
			TenantID:    tenantID,
			UserID:      "data_analysis",
			IdentityID:  "data_analysis",
			DisplayName: "数据看板与统计分析",
			Permissions: map[string]bool{"project.read": true},
			DataScopes: []platform.DataScope{{
				RoleCode:  "system_integration",
				ScopeType: "APPLICATION",
			}},
		})
		c.Next()
	}
}
