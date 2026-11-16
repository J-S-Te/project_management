package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/j-s-te/project-management/internal/auditoutbox"
	"github.com/j-s-te/project-management/internal/platform"
)

// reportAuditOutboxHealth 周期上报本地审计 outbox 快照；失败只记日志，不影响主流程。
func reportAuditOutboxHealth(ctx context.Context, store *auditoutbox.GORMStore, reporter platform.AuditReporter, logger *slog.Logger) {
	if store == nil || reporter == nil {
		return
	}
	health, ok := reporter.(platform.OutboxHealthReporter)
	if !ok {
		return
	}
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		counts, err := store.CountBacklog(ctx)
		if err == nil {
			reportCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if reportErr := health.ReportOutboxHealth(reportCtx, counts.Pending, counts.Dead, time.Time{}); reportErr != nil {
				logger.Warn("report audit outbox health failed", "error", reportErr)
			}
			cancel()
		} else {
			logger.Warn("read audit outbox health failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
