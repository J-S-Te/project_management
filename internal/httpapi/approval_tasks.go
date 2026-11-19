package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// approvalNavigationSections 为实际复核者和提交者补齐原业务入口及统一审批待办。
// 参数：sections 为已有角色栏目，p 为当前权限，nativeEnabled 为原生范围功能状态。
// 返回值：沿用默认栏目顺序的导航清单；业务写权限由原接口校验。
func approvalNavigationSections(sections []string, p platform.Principal, nativeEnabled bool) []string {
	method := p.Has("project.special_method.review")
	methodRequester := p.Has("service_item.confirm")
	deviation := p.Has("project.deviation.review") || p.Has("project.deviation.report")
	nativeApprover := p.Has("project.native_scope.approve") &&
		(slices.Contains(p.Roles, "technical_director") || slices.Contains(p.Roles, "admin"))
	nativeRequester := p.Has("project.decomposition.manage") && slices.Contains(p.Roles, "business_admin")
	native := nativeEnabled && (nativeApprover || nativeRequester)
	if (method || methodRequester || deviation || native) && !slices.Contains(sections, "approvals") {
		sections = append(sections, "approvals")
	}
	if (method || methodRequester) && !slices.Contains(sections, "methods") {
		sections = append(sections, "methods")
	}
	if deviation && !slices.Contains(sections, "exceptions") {
		sections = append(sections, "exceptions")
	}
	return sections
}

// readServiceItemByID 以现有列表格式返回经权限复核的直达服务项。
// 参数：c/query 为请求和分页，ids 为原始服务项编号参数。
// 返回值：无；写入单条结果或统一校验错误。
func (h *Handler) readServiceItemByID(c *gin.Context, query pageQuery, ids []string) {
	if len(ids) != 1 || strings.TrimSpace(ids[0]) == "" {
		writeServiceError(c, application.ValidationError("请指定一个服务项编号"))
		return
	}
	item, err := h.service.ReadApprovalServiceItem(
		c.Request.Context(), principal(c), strings.TrimSpace(ids[0]), c.Query("project_id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writePage(c, []domain.ServiceItem{item}, 1, query)
}

// approvalTasks 返回当前账号受权的审批待办或精确申请详情。
// 参数：c 为已鉴权请求，分页、事项类型和申请编号用于筛选。
// 返回值：无；写入分页结果，输入或权限错误沿用统一错误模型。
func (h *Handler) approvalTasks(c *gin.Context) {
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, sizeErr := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageErr != nil || sizeErr != nil {
		writeServiceError(c, application.ValidationError("审批待办分页参数必须为整数"))
		return
	}
	if len(c.QueryArray("request_id")) > 1 || len(c.QueryArray("task_type")) > 1 {
		writeServiceError(c, application.ValidationError("请指定一个办理事项类型与编号"))
		return
	}
	result, err := h.service.ListApprovalTasks(c.Request.Context(), principal(c), domain.ApprovalQuery{
		Page: page, PageSize: size, View: c.DefaultQuery("view", "assigned"),
		RequestID: strings.TrimSpace(c.Query("request_id")),
		TaskType:  strings.TrimSpace(c.Query("task_type")),
	})
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeData(c, http.StatusOK, result)
}
