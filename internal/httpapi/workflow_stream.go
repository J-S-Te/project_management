package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/platform"
)

const (
	workflowPollInterval = time.Second
	workflowHeartbeat    = 10 * time.Second
	workflowReadTimeout  = 3 * time.Second
	workflowWriteTimeout = 3 * time.Second
)

// workflowStream 建立当前会话的只读项目流程 SSE，响应只含快照签名。
// 参数：c 为认证路由上下文，project_id 可限定当前详情。
// 返回值：无；读取失效、会话变化、写失败或停机时结束连接。
func (h *Handler) workflowStream(c *gin.Context) {
	request, initial, projectID := c.Request, principal(c), c.Query("project_id")
	initialVersion, err := h.readWorkflowVersion(request.Context(), initial, projectID)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	header := c.Writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store, private")
	header.Set("X-Accel-Buffering", "no")
	if err := writeWorkflowFrame(c.Writer, "event: ready\ndata: "+initialVersion+"\n\n"); err != nil {
		return
	}
	h.forwardWorkflowChanges(c.Writer, request, initial, projectID, initialVersion)
}

// readWorkflowVersion 给单次数据库读取设定明确上限。
// 参数：ctx 为连接上下文，identity 为已认证主体，projectID 为读取范围。
// 返回值：受权快照签名或读取失败。
func (h *Handler) readWorkflowVersion(ctx context.Context, identity platform.Principal, projectID string) (string, error) {
	query, cancel := context.WithTimeout(ctx, workflowReadTimeout)
	defer cancel()
	return h.service.WorkflowVersion(query, identity, projectID)
}

// forwardWorkflowChanges 合并该连接可见范围内的一次或多次已提交变更。
// 参数：writer/request 为当前连接，initial、projectID、version 是首次读取时的会话和快照。
// 返回值：无；连接失效或发布中止时退出。
func (h *Handler) forwardWorkflowChanges(writer http.ResponseWriter, request *http.Request,
	initial platform.Principal, projectID, version string) {
	poll := time.NewTicker(workflowPollInterval)
	heartbeat := time.NewTicker(workflowHeartbeat)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-h.workflowShutdown:
			return
		case <-heartbeat.C:
			if !h.workflowSessionCurrent(request, initial) || writeWorkflowFrame(writer, ": heartbeat\n\n") != nil {
				return
			}
		case <-poll.C:
			current, err := h.readWorkflowVersion(request.Context(), initial, projectID)
			if err != nil {
				h.logger.Debug("read project workflow version", "error", err)
				return
			}
			if current == version {
				continue
			}
			if !h.workflowSessionCurrent(request, initial) ||
				writeWorkflowFrame(writer, "event: workflow-changed\ndata: "+current+"\n\n") != nil {
				return
			}
			version = current
		}
	}
}

// workflowSessionCurrent 用原会话重新核验身份与项目数据范围。
// 参数：request 为原请求，initial 为连接打开时的主体。
// 返回值：会话仍持有同样的授权时返回真；变化时由新连接重新校准。
func (h *Handler) workflowSessionCurrent(request *http.Request, initial platform.Principal) bool {
	ctx, cancel := context.WithTimeout(request.Context(), workflowReadTimeout)
	defer cancel()
	current, err := h.identity.Authenticate(ctx, request)
	if err != nil || !current.Has("project.read") || current.Subject != initial.Subject ||
		current.AuthorizationRevision != initial.AuthorizationRevision ||
		current.CatalogVersion != initial.CatalogVersion {
		return false
	}
	oldScope, oldErr := initial.ProjectScopeFilter()
	newScope, newErr := current.ProjectScopeFilter()
	if oldErr != nil || newErr != nil || !reflect.DeepEqual(oldScope, newScope) {
		return false
	}
	return h.workflowLicense == nil || h.workflowLicense.Check(ctx,
		commercialOperation(http.MethodGet, "/api/v1/workflow/stream")) == nil
}

// writeWorkflowFrame 刷新连接写期限并立即冲刷一条 SSE 帧。
// 参数：writer 为当前流响应，frame 为已编码帧。
// 返回值：连接写入失败或响应不支持流时返回错误。
func writeWorkflowFrame(writer http.ResponseWriter, frame string) error {
	controller := http.NewResponseController(writer)
	if err := controller.SetWriteDeadline(time.Now().Add(workflowWriteTimeout)); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return err
	}
	defer controller.SetWriteDeadline(time.Time{})
	if _, err := io.WriteString(writer, frame); err != nil {
		return err
	}
	return controller.Flush()
}
