package httpapi

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

// reportDashboard 返回可信租户报告快照，保持BusinessSnapshot原始JSON契约。
// 参数：c为已完成专用机器鉴权和READ_HISTORY许可校验的请求。
// 返回值：无，通过HTTP写出快照或既有错误契约。
func (h *Handler) reportDashboard(c *gin.Context) {
	value, err := h.service.ReportDashboard(c.Request.Context(), principal(c))
	if err != nil {
		snapshotHTTPError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, value)
}
