package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/platform"
)

type ContractIntegrationOptions struct {
	Enabled        bool
	RequireBearer  bool
	BearerVerifier platform.ClientCredentialsTokenVerifier
}

func (h *Handler) authenticateContractIntegration(options ContractIntegrationOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !options.Enabled {
			writeError(c, http.StatusNotFound, "PM_NOT_FOUND", "接口未启用")
			c.Abort()
			return
		}
		// H4 修复：来源校验不可关闭——内部投递必须携带可验证的机器访问令牌，
		// 租户/投递编号等可伪造头不再单独构成信任边界。
		if options.BearerVerifier == nil {
			writeError(c, http.StatusServiceUnavailable, "PM_INTEGRATION_AUTH_UNAVAILABLE", "合同系统机器身份校验未配置")
			c.Abort()
			return
		}
		const bearerPrefix = "Bearer "
		authorization := strings.TrimSpace(c.GetHeader("Authorization"))
		if !strings.HasPrefix(authorization, bearerPrefix) || strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix)) == "" {
			writeError(c, http.StatusUnauthorized, "PM_INTEGRATION_BEARER_REQUIRED", "合同系统投递必须携带 Keycloak 机器访问令牌")
			c.Abort()
			return
		}
		if err := options.BearerVerifier.VerifyClientCredentials(c.Request.Context(), strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix))); err != nil {
			writeError(c, http.StatusUnauthorized, "PM_INTEGRATION_BEARER_INVALID", "合同系统机器访问令牌无效")
			c.Abort()
			return
		}
		tenantID := strings.TrimSpace(c.GetHeader("X-Contract-Tenant-ID"))
		deliveryID := strings.TrimSpace(c.GetHeader("X-Contract-Delivery-ID"))
		if tenantID == "" || deliveryID == "" {
			writeError(c, http.StatusBadRequest, "PM_INTEGRATION_HEADERS_REQUIRED", "缺少合同系统租户或投递编号")
			c.Abort()
			return
		}
		c.Set("principal", platform.Principal{TenantID: tenantID, IdentityID: "contract_management", UserID: "contract_management", DisplayName: "合同管理系统", Permissions: map[string]bool{"project.contract.import": true}, DataScopes: []platform.DataScope{{RoleCode: "system_integration", ScopeType: "APPLICATION"}}})
		c.Next()
	}
}
