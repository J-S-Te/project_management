package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type ContractImportOptions struct {
	Enabled        bool
	BearerVerifier platform.ClientCredentialsTokenVerifier
}

// authenticateContractImport 校验独立机器身份，关闭新命令时仍允许可信恢复请求。
// 参数：options 为固定验签器及新增命令开关。
// 返回值：认证中间件；失败时不使用请求头租户或浏览器身份。
func (h *Handler) authenticateContractImport(options ContractImportOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		if options.BearerVerifier == nil {
			writeError(c, 503, "PM_INTEGRATION_AUTH_UNAVAILABLE", "导入机器认证未配置")
			c.Abort()
			return
		}
		auth := strings.TrimSpace(c.GetHeader("Authorization"))
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(c, 401, "PM_INTEGRATION_BEARER_REQUIRED", "需要机器身份")
			c.Abort()
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		identity, err := options.BearerVerifier.VerifyClientCredentials(c.Request.Context(), token)
		if token == "" || err != nil || identity.TenantID == "" || identity.ApplicationCode != "contract_management" {
			writeError(c, 401, "PM_INTEGRATION_BEARER_INVALID", "导入机器身份无效")
			c.Abort()
			return
		}
		c.Set("principal", platform.Principal{
			TenantID: identity.TenantID, UserID: "contract_management", IdentityID: "contract_management",
			Permissions: map[string]bool{"project.contract.import": true},
			DataScopes:  []platform.DataScope{{RoleCode: "system_integration", ScopeType: "APPLICATION"}},
		})
		c.Next()
	}
}

// contractImportCapabilities 返回此接收方实际支持的版本和模式。
// 参数：c 为通过机器认证的请求，enabled 为新命令开关。
// 返回值：无；输出版本化能力信封。
func (h *Handler) contractImportCapabilities(c *gin.Context, enabled bool) {
	c.Header("Cache-Control", "no-store")
	writeData(c, 200, gin.H{
		"schema_version": 1, "tenant_id": principal(c).TenantID,
		"modes": []string{"CONTINUE_LIVE"}, "project_modes": []string{"CREATE_NEW", "LINK_EXISTING"},
		"enabled": enabled,
	})
}

// bindContractImport 严格解码机器命令并返回持久完成证据。
// 参数：c 为通过机器认证的请求，enabled 控制新增命令。
// 返回值：无；成功仅在事务完成后返回PROJECT_BOUND。
func (h *Handler) bindContractImport(c *gin.Context, enabled bool) {
	var input domain.ContractImportBindingCommand
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(c, 422, "IMPORT_VALIDATION_FAILED", "导入命令格式无效")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(c, 422, "IMPORT_VALIDATION_FAILED", "导入命令必须为单一对象")
		return
	}
	receipt, err := h.service.BindImportedContract(c.Request.Context(), principal(c), input, enabled)
	if err != nil {
		writeContractImportError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeData(c, 200, receipt)
}

// getContractImportBinding 输出可信租户来源项的真实绑定。
// 参数：c 为通过机器认证的请求。
// 返回值：无；不存在时返回现有错误信封。
func (h *Handler) getContractImportBinding(c *gin.Context) {
	receipt, err := h.service.GetImportedContractBinding(c.Request.Context(), principal(c), c.Param("origin_import_item_id"))
	if err != nil {
		writeContractImportError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeData(c, 200, receipt)
}

// writeContractImportError 将接续政策及身份冲突映射到稳定错误类别。
// 参数：c 为响应上下文，err 为业务错误。
// 返回值：无。
func writeContractImportError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, application.ErrImportConflict), errors.Is(err, application.ErrConflict):
		writeError(c, 409, "IMPORT_IDEMPOTENCY_CONFLICT", "导入身份、关系或项目版本冲突")
	case errors.Is(err, application.ErrImportPolicy):
		writeError(c, 422, "IMPORT_POLICY_UNSUPPORTED", "项目导入模式不支持")
	case errors.Is(err, application.ErrImportDisabled):
		writeError(c, 409, "IMPORT_TARGET_NOT_READY", "项目导入新增已关闭")
	case errors.Is(err, application.ErrValidation):
		writeError(c, 422, "IMPORT_VALIDATION_FAILED", "项目导入参数或负责人无效")
	default:
		writeServiceError(c, err)
	}
}
