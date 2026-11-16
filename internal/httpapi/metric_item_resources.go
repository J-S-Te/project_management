package httpapi

import (
	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"net/http"
	"strconv"
)

// metricItemResourceList 用途：绑定当前父服务项的分页事实读取，不接受客户端替换可信租户。
// 参数：kind 为路由固定的容量或用量类型。
// 返回值：按原权限与数据范围返回真实台账的处理函数。
func (h *Handler) metricItemResourceList(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
		if err != nil {
			writeServiceError(c, application.ErrValidation)
			return
		}
		out, err := h.service.MetricItemResources(c.Request.Context(), principal(c), c.Param("id"), kind, page)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		writeData(c, http.StatusOK, out)
	}
}
