package httpapi

import (
	"github.com/gin-gonic/gin"
)

// metricFactsStream 输出受原机器范围与历史许可保护的完整事实流。
// 参数：c 为已验证主体的只读请求。
// 返回值：无；输出中失败不追加end，输出前失败沿用错误模型。
func (h *Handler) metricFactsStream(c *gin.Context) {
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-store")
	if err := h.service.StreamMetricFacts(c.Request.Context(), principal(c), c.Writer); err != nil && !c.Writer.Written() {
		snapshotHTTPError(c, err)
	}
}
