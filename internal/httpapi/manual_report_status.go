package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

// advanceManualReportStatus 接收当前项目经理的主报告手动阶段命令。
// 参数：c 为带可信会话及服务项编号的请求上下文。
// 返回值：无；返回更新后的阶段和手动模式，失败沿用统一业务错误。
func (h *Handler) advanceManualReportStatus(c *gin.Context) {
	var input domain.ReportStatusInput
	if !decode(c, &input) {
		return
	}
	if err := h.service.AdvanceManualReportStatus(
		c.Request.Context(), principal(c), c.Param("id"), input,
	); err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, map[string]string{
		"status":          strings.ToUpper(strings.TrimSpace(input.Phase)),
		"transition_mode": application.ManualProjectManagerReportMode,
	})
}
