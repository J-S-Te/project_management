package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/domain"
)

// metricUsageCandidates 读取服务项现场事件推算的用量候选。
func (h *Handler) metricUsageCandidates(c *gin.Context) {
	out, err := h.service.MetricUsageCandidates(c.Request.Context(), principal(c), c.Param("id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}

// metricUsageCandidateApply 服务端重算候选后登记，完整事件链按策略自动确认。
func (h *Handler) metricUsageCandidateApply(c *gin.Context) {
	var in domain.MetricUsageCandidateApplyInput
	if !decodeNativeScope(c, &in) {
		return
	}
	out, err := h.service.ApplyMetricUsageCandidate(c.Request.Context(), principal(c), c.Param("id"), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, out)
}
