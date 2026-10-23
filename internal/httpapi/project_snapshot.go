package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

func (h *Handler) projectSnapshot(limits application.ProjectSnapshotLimits) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), limits.Timeout)
		defer cancel()
		// net/http enforces this deadline even while a slow peer blocks Write. Gin's
		// wrapper supports Unwrap; retain its status accounting for actual writes.
		raw := http.ResponseWriter(c.Writer)
		if unwrapper, ok := raw.(interface{ Unwrap() http.ResponseWriter }); ok {
			raw = unwrapper.Unwrap()
		}
		controller := http.NewResponseController(raw)
		deadline, _ := ctx.Deadline()
		if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			writeError(c, http.StatusServiceUnavailable, "PM_SNAPSHOT_UNAVAILABLE", "项目快照暂不可用")
			return
		}
		defer controller.SetWriteDeadline(time.Time{})
		sink := &projectSnapshotHTTPSink{ctx: ctx, c: c, controller: controller}
		stream := application.NewProjectSnapshotStream(ctx, principal(c).TenantID, limits, sink)
		summary, err := h.service.ExportProjectSnapshot(ctx, principal(c), limits, stream)
		if err == nil {
			var observed domain.ProjectSnapshotSummary
			observed, err = stream.Summary()
			if err == nil && summary != observed {
				err = application.ErrProjectSnapshotProtocol
			}
		}
		if err == nil {
			err = sink.write(summary)
		}
		if err != nil {
			// Once NDJSON started, any JSON error body would corrupt its record grammar.
			// Closing without end is the explicit failure signal for the receiver.
			if !c.Writer.Written() {
				snapshotHTTPError(c, err)
			}
			c.Abort()
		}
	}
}

type projectSnapshotHTTPSink struct {
	ctx        context.Context
	c          *gin.Context
	controller *http.ResponseController
}

func (s *projectSnapshotHTTPSink) Begin(header domain.ProjectSnapshotHeader) error {
	s.c.Header("Content-Type", "application/x-ndjson")
	s.c.Header("Cache-Control", "no-store")
	return s.write(header)
}
func (s *projectSnapshotHTTPSink) Project(project domain.ProjectProjection) error {
	return s.write(project)
}
func (s *projectSnapshotHTTPSink) write(record any) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	line, err := domain.ProjectSnapshotLine(record)
	if err != nil {
		return err
	}
	n, err := s.c.Writer.Write(line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	if err = s.controller.Flush(); err != nil {
		return err
	}
	return s.ctx.Err()
}
func snapshotHTTPError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, application.ErrForbidden):
		writeError(c, http.StatusForbidden, "PM_FORBIDDEN", "无权读取项目快照")
	case errors.Is(err, application.ErrProjectSnapshotUnavailable):
		writeError(c, http.StatusServiceUnavailable, "PM_SNAPSHOT_UNAVAILABLE", "项目快照暂不可用")
	case errors.Is(err, application.ErrProjectSnapshotLimit):
		writeError(c, http.StatusRequestEntityTooLarge, "PM_SNAPSHOT_LIMIT", "项目快照超出受保护的读取范围")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "PM_SNAPSHOT_TIMEOUT", "项目快照读取超时或已取消")
	default:
		writeError(c, http.StatusInternalServerError, "PM_SNAPSHOT_FAILED", "项目快照读取失败")
	}
}
