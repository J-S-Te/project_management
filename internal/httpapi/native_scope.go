package httpapi

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"io"
	"net/http"
)

// nativeScope 返回真实当前范围、完整版本及本地审批能力。
// 参数：c 为经过项目读取权限检查的请求。
// 返回值：无；错误经原服务错误契约输出。
func (h *Handler) nativeScope(c *gin.Context) {
	state, err := h.service.ReadNativeScope(c.Request.Context(), principal(c), c.Param("id"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, state)
}

// requestNativeScope 接收待审批本地范围，不直接改变生效版本。
// 参数：c 为真实业务管理员的受保护请求。
// 返回值：无；返回持久提案及原操作号。
func (h *Handler) requestNativeScope(c *gin.Context) {
	var in domain.NativeScopeInput
	if !decodeNativeScope(c, &in) {
		return
	}
	change, err := h.service.RequestNativeScope(c.Request.Context(), principal(c), c.Param("id"), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusCreated, change)
}

// decideNativeScope 接收异人单级批准或驳回并保留失败关闭。
// 参数：c 为具备精确审批权限的请求。
// 返回值：无；未知决定及撤回伪装均拒绝。
func (h *Handler) decideNativeScope(c *gin.Context) {
	var in domain.NativeScopeDecision
	if !decodeNativeScope(c, &in) {
		return
	}
	if in.Decision != "APPROVE" && in.Decision != "REJECT" {
		writeServiceError(c, application.ValidationError("该路由不接受此决定"))
		return
	}
	h.writeNativeScopeDecision(c, in)
}

// withdrawNativeScope 仅将专用申请人撤回路由解释为撤回决定。
// 参数：c 为业务管理员的受保护请求。
// 返回值：无；具体申请人所有权继续由事务核验。
func (h *Handler) withdrawNativeScope(c *gin.Context) {
	var in domain.NativeScopeDecision
	if !decodeNativeScope(c, &in) {
		return
	}
	if in.Decision != "" && in.Decision != "WITHDRAW" {
		writeServiceError(c, application.ValidationError("该路由不接受此决定"))
		return
	}
	in.Decision = "WITHDRAW"
	h.writeNativeScopeDecision(c, in)
}

// writeNativeScopeDecision 调用真实审批服务并返回持久最终结果。
// 参数：c 为请求，in 为专用路由已校验的决定。
// 返回值：无；不将HTTP受理伪装为生效。
func (h *Handler) writeNativeScopeDecision(c *gin.Context, in domain.NativeScopeDecision) {
	change, err := h.service.DecideNativeScope(c.Request.Context(), principal(c), c.Param("id"), c.Param("changeID"), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeData(c, http.StatusOK, change)
}

// decodeNativeScope 严格校验本地范围白名单，拒绝合同和客户端审批字段混入。
// 参数：c 为请求，target 为本地范围专用输入。
// 返回值：单个有界JSON对象通过校验时为真，否则返回原错误格式。
func decodeNativeScope(c *gin.Context, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(c, http.StatusBadRequest, "PM_INVALID_JSON", "请求内容不合法")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(c, http.StatusBadRequest, "PM_INVALID_JSON", "请求只能包含一个JSON对象")
		return false
	}
	return true
}
