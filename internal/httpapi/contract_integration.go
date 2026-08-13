package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/platform"
)

type ContractIntegrationOptions struct {
	Enabled bool
}

func (h *Handler) authenticateContractIntegration(options ContractIntegrationOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !options.Enabled {
			writeError(c, http.StatusNotFound, "PM_NOT_FOUND", "接口未启用")
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
