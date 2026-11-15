package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

// metricCalendarList 读取组织工作日历。
func (h *Handler) metricCalendarList(c *gin.Context) {
	out, err := h.service.MetricCalendar(c.Request.Context(), principal(c), c.Query("organization_id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}

// metricCalendarSave 保存组织某个星期的工作时段。
func (h *Handler) metricCalendarSave(c *gin.Context) {
	var in domain.MetricCalendarRuleInput
	if !decodeNativeScope(c, &in) {
		return
	}
	out, err := h.service.SaveMetricCalendarRule(c.Request.Context(), principal(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}

// metricCalendarExceptionList 读取组织日期范围内的资源例外。
func (h *Handler) metricCalendarExceptionList(c *gin.Context) {
	out, err := h.service.MetricCalendarExceptions(c.Request.Context(), principal(c), c.Query("organization_id"), c.Query("from"), c.Query("to"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}

// metricCalendarExceptionSave 保存资源容量例外。
func (h *Handler) metricCalendarExceptionSave(c *gin.Context) {
	var in domain.MetricCalendarExceptionInput
	if !decodeNativeScope(c, &in) {
		return
	}
	out, err := h.service.SaveMetricCalendarException(c.Request.Context(), principal(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}

// metricCalendarExceptionDelete 以版本围栏删除资源例外。
func (h *Handler) metricCalendarExceptionDelete(c *gin.Context) {
	version, err := strconv.ParseUint(c.Query("expected_version"), 10, 64)
	if err != nil {
		writeServiceError(c, application.ErrValidation)
		return
	}
	if err = h.service.DeleteMetricCalendarException(c.Request.Context(), principal(c), c.Param("id"), version, c.Query("idempotency_key")); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// metricCapacityGenerate 按组织日历逐日生成容量。
func (h *Handler) metricCapacityGenerate(c *gin.Context) {
	var in domain.MetricCapacityGenerateInput
	if !decodeNativeScope(c, &in) {
		return
	}
	out, err := h.service.GenerateMetricCapacities(c.Request.Context(), principal(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}

// metricAutoConfirmGet 读取强证据自动确认策略。
func (h *Handler) metricAutoConfirmGet(c *gin.Context) {
	out, err := h.service.MetricAutoConfirmSetting(c.Request.Context(), principal(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}

// metricAutoConfirmPut 保存强证据自动确认策略。
func (h *Handler) metricAutoConfirmPut(c *gin.Context) {
	var in domain.MetricAutoConfirmSettingInput
	if !decodeNativeScope(c, &in) {
		return
	}
	out, err := h.service.SetMetricAutoConfirmSetting(c.Request.Context(), principal(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, out)
}
