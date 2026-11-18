package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

// approvalTasks 返回当前账号受权的审批待办或精确申请详情。
// 参数：c 为已鉴权请求，分页和申请编号仅影响筛选而不决定用户或租户。
// 返回值：无；写入分页结果，输入或权限错误沿用统一错误模型。
func (h *Handler) approvalTasks(c *gin.Context) {
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, sizeErr := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageErr != nil || sizeErr != nil {
		writeServiceError(c, application.ValidationError("审批待办分页参数必须为整数"))
		return
	}
	result, err := h.service.ListApprovalTasks(c.Request.Context(), principal(c), domain.ApprovalQuery{
		Page: page, PageSize: size, View: c.DefaultQuery("view", "assigned"),
		RequestID: strings.TrimSpace(c.Query("request_id")),
	})
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeData(c, http.StatusOK, result)
}
