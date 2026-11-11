package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

// metricFacts 返回专用机器身份已绑定租户的完整指标包络。
// 参数：c 为已完成机器鉴权和READ_HISTORY许可的请求。
// 返回值：无；查询失败和超限使用既有失败契约。
func (h *Handler) metricFacts(c *gin.Context) {
	out, err := h.service.MetricFacts(c.Request.Context(), principal(c))
	if err != nil {
		snapshotHTTPError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"code": "OK", "data": out})
}

// metricReviewList 返回父服务项下分页审核轮次。
// 参数：c 为真实项目读取会话。
// 返回值：无；写出最多50条轮次。
func (h *Handler) metricReviewList(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	out, err := h.service.MetricReportReviews(c.Request.Context(), principal(c), c.Param("id"), page)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}

// metricReviewCommand 严格区分报告提交和独立决定路由。
// 参数：decision 为是否审核决定。
// 返回值：处理真实审核命令的HTTP函数。
func (h *Handler) metricReviewCommand(decision bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in domain.ReportReviewInput
		if !decodeNativeScope(c, &in) {
			return
		}
		if decision && in.Decision == "" || !decision && in.Decision != "" {
			writeServiceError(c, application.ErrValidation)
			return
		}
		out, err := h.service.ReviewMetricReport(c.Request.Context(), principal(c), c.Param("id"), in)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		writeData(c, http.StatusOK, out)
	}
}

// metricCapacityPublish 接收明确资源容量发布命令。
// 参数：c 为真实容量管理员会话。
// 返回值：无；发布成功返回冻结版本。
func (h *Handler) metricCapacityPublish(c *gin.Context) {
	var in domain.ResourceCapacityInput
	if !decodeNativeScope(c, &in) {
		return
	}
	out, err := h.service.PublishMetricCapacity(c.Request.Context(), principal(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, out)
}

// metricUsageCreate 接收带真实时段和证据的用量草稿。
// 参数：c 为具有明确登记动作权限的会话。
// 返回值：无；返回草稿或既有业务错误。
func (h *Handler) metricUsageCreate(c *gin.Context) {
	var in domain.ResourceUsageInput
	if !decodeNativeScope(c, &in) {
		return
	}
	out, err := h.service.CreateMetricUsage(c.Request.Context(), principal(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, out)
}

// metricUsageCommand 绑定实际用量的明确提交、决定或更正动作。
// 参数：action 为服务器路由固定的业务动作。
// 返回值：有版本、幂等和服务端独立复核的处理函数。
func (h *Handler) metricUsageCommand(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in domain.ResourceUsageDecision
		if !decodeNativeScope(c, &in) {
			return
		}
		out, err := h.service.MutateMetricUsage(c.Request.Context(), principal(c), c.Param("usageID"), action, in)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		writeData(c, http.StatusOK, out)
	}
}

// metricResourceList 绑定有界分页资源事实查询。
// 参数：kind 为服务器固定的容量或用量集合名。
// 返回值：按真实授权范围读取事实的处理函数。
func (h *Handler) metricResourceList(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
		if err != nil {
			writeServiceError(c, application.ErrValidation)
			return
		}
		out, err := h.service.MetricResources(c.Request.Context(), principal(c), kind, page)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		writeData(c, http.StatusOK, out)
	}
}

// metricReviewCurrent 返回与分页历史独立的两个报告主体当前轮次。
// 参数：c 为拥有父项读取范围的真实请求。
// 返回值：无；明确输出当前事实或既有服务错误。
func (h *Handler) metricReviewCurrent(c *gin.Context) {
	out, err := h.service.CurrentMetricReportReviews(c.Request.Context(), principal(c), c.Param("id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}
