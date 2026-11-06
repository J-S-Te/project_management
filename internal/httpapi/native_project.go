package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/domain"
)

// projectCreationCapabilities 返回当前环境显式启用的建项来源能力。
// 参数：c 为持有 project.read 或 project.create 的已鉴权模式读取请求。
// 返回值：无；只输出显式模式标记，不授予任何建项或范围审批写权限。
func (h *Handler) projectCreationCapabilities(c *gin.Context) {
	writeData(c, http.StatusOK, gin.H{"native_enabled": h.service.NativeEnabled})
}

// createNativeProject 接收本地委托范围并复用真实用户授权及组织边界。
// 参数：c 为经过 project.create 权限检查的用户请求。
// 返回值：无；输出持久化项目或明确业务错误。
func (h *Handler) createNativeProject(c *gin.Context) {
	var request struct {
		domain.Project
		ServiceItems []domain.ContractService `json:"service_items"`
	}
	if !decode(c, &request) {
		return
	}
	item, err := h.service.CreateNativeProject(c.Request.Context(), principal(c), request.Project, request.ServiceItems)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, item)
}
