package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
)

const crmProjectHistoryPath = "/internal/v1/crm/customers/:customerID/projects"

// authenticateCRMProjectHistory 验证 CRM 专用机器身份，并绑定签名租户。
// 参数：verifier 为独立验签器，绑定应用、环境、签名租户及专用 scope。
// 返回值：认证中间件，身份无效时拒绝请求。
func authenticateCRMProjectHistory(verifier platform.ClientCredentialsTokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		identity, err := verifier.VerifyClientCredentials(c.Request.Context(), strings.TrimSpace(token))
		if err != nil || identity.TenantID == "" || identity.ApplicationCode != "customer_and_opportunity" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("principal", platform.Principal{
			TenantID: identity.TenantID, IdentityID: identity.ApplicationCode, UserID: identity.ApplicationCode,
			Permissions: map[string]bool{application.CRMProjectHistoryScope: true},
		})
		c.Next()
	}
}

// crmProjectHistory 将精确客户和可信租户内的项目摘要返回给 CRM。
// 参数：c 为已通过机器认证的请求上下文。
// 返回值：无，分页结果或错误通过 HTTP 返回。
func (h *Handler) crmProjectHistory(c *gin.Context) {
	query, err := parseCRMHistoryPage(c.Request.URL.RawQuery)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	result, err := h.service.ListCRMCustomerProjects(
		c.Request.Context(), principal(c), c.Param("customerID"), query.Page, query.PageSize,
	)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeData(c, http.StatusOK, result)
}

// parseCRMHistoryPage 接受显式分页并拒绝额外过滤条件。
// 参数：raw 为请求原始查询串。
// 返回值：页码和页大小，非法键或数值返回参数错误。
func parseCRMHistoryPage(raw string) (pageQuery, error) {
	params, err := url.ParseQuery(raw)
	if err != nil {
		return pageQuery{}, application.ErrValidation
	}
	result := pageQuery{Page: 1, PageSize: 20}
	for key, values := range params {
		if len(values) != 1 {
			return pageQuery{}, application.ErrValidation
		}
		value, err := strconv.Atoi(values[0])
		if err != nil || value < 1 {
			return pageQuery{}, application.ErrValidation
		}
		switch key {
		case "page":
			result.Page = value
		case "page_size":
			result.PageSize = value
		default:
			return pageQuery{}, application.ErrValidation
		}
	}
	return result, nil
}
