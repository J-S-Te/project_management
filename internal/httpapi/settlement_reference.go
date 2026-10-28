package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/platform"
)

type SettlementReferenceOptions struct {
	Enabled        bool
	BearerVerifier platform.ClientCredentialsTokenVerifier
}

// authenticateSettlementReference 验证专用结算只读机器身份并从验签结果提取租户。
// 参数：options 为启动时固定的独立验签器。
// 返回值：认证中间件，未配置或身份不可信时失败关闭。
func (h *Handler) authenticateSettlementReference(options SettlementReferenceOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		if options.BearerVerifier == nil {
			writeError(c, 503, "PM_SETTLEMENT_REFERENCE_UNAVAILABLE", "项目引用机器认证未配置")
			c.Abort()
			return
		}
		value := strings.TrimSpace(c.GetHeader("Authorization"))
		if !strings.HasPrefix(value, "Bearer ") {
			writeError(c, 401, "PM_SETTLEMENT_REFERENCE_UNAUTHORIZED", "需要机器身份")
			c.Abort()
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
		identity, err := options.BearerVerifier.VerifyClientCredentials(c.Request.Context(), token)
		if token == "" || err != nil || identity.TenantID == "" || identity.ApplicationCode != "settlement" {
			writeError(c, 401, "PM_SETTLEMENT_REFERENCE_UNAUTHORIZED", "机器身份无效")
			c.Abort()
			return
		}
		c.Set("principal", platform.Principal{TenantID: identity.TenantID, UserID: "settlement", IdentityID: "settlement", Permissions: map[string]bool{"settlement.project_reference.read": true}})
		c.Next()
	}
}

// settlementProjectReference 输出同租户项目的权威引用并禁止缓存。
// 参数：c 为通过独立机器认证的请求。
// 返回值：无；结果通过HTTP输出。
func (h *Handler) settlementProjectReference(c *gin.Context) {
	ref, err := h.service.ResolveSettlementProjectReference(c.Request.Context(), principal(c), c.Param("projectID"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeData(c, http.StatusOK, ref)
}
