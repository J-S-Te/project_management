package mysql

import (
	"context"
	"errors"
	"github.com/j-s-te/project-management/internal/application"
	"strings"
	"time"
)

func (r *Repository) CountActiveBackgroundLeases(ctx context.Context, tenant string, now time.Time) (int64, error) {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(tenant) != tenant || len(tenant) > 64 || now.IsZero() {
		return -1, errors.New("invalid background lease query")
	}
	var n int64
	// A single statement gives both outboxes one database statement snapshot.
	e := r.db.WithContext(ctx).Raw(`SELECT (SELECT COUNT(*) FROM pm_delivery_effects_outbox WHERE tenant_id = ? AND status = 'PROCESSING' AND locked_until > ?) + (SELECT COUNT(*) FROM pm_notification_outbox WHERE tenant_id = ? AND status = 'PROCESSING' AND locked_until > ?) AS active_leases`, tenant, now.UTC(), tenant, now.UTC()).Scan(&n).Error
	if e != nil {
		return -1, e
	}
	return n, nil
}

func (r *Repository) ReadBackgroundQueueStats(ctx context.Context, tenant string, now time.Time) ([]application.BackgroundQueueStats, error) {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(tenant) != tenant || len(tenant) > 64 || now.IsZero() {
		return nil, errors.New("invalid background queue query")
	}
	out := make([]application.BackgroundQueueStats, 0, 2)
	for _, item := range []struct{ kind, table string }{{"effects", "pm_delivery_effects_outbox"}, {"notifications", "pm_notification_outbox"}} {
		var row struct {
			Pending, RetryWait, Processing, Dead int64
			OldestDueAt                          *time.Time
		}
		// The table names are compile-time constants, never supplied by the caller.
		query := `SELECT COALESCE(SUM(CASE WHEN status = 'PENDING' THEN 1 ELSE 0 END),0) AS pending, COALESCE(SUM(CASE WHEN status = 'RETRY_WAIT' THEN 1 ELSE 0 END),0) AS retry_wait, COALESCE(SUM(CASE WHEN status = 'PROCESSING' THEN 1 ELSE 0 END),0) AS processing, COALESCE(SUM(CASE WHEN status = 'DEAD' THEN 1 ELSE 0 END),0) AS dead FROM ` + item.table + ` WHERE tenant_id = ?`
		if e := r.db.WithContext(ctx).Raw(query, tenant).Scan(&row).Error; e != nil {
			return nil, e
		}
		var due []struct{ CreatedAt time.Time }
		if e := r.db.WithContext(ctx).Table(item.table).Select("created_at").Where("tenant_id = ? AND status IN ? AND (next_retry_at IS NULL OR next_retry_at <= ?) AND (locked_until IS NULL OR locked_until <= ?)", tenant, []string{"PENDING", "RETRY_WAIT", "PROCESSING"}, now.UTC(), now.UTC()).Order("created_at").Limit(1).Scan(&due).Error; e != nil {
			return nil, e
		}
		if len(due) == 1 {
			row.OldestDueAt = &due[0].CreatedAt
		}
		out = append(out, application.BackgroundQueueStats{Kind: item.kind, Pending: row.Pending, RetryWait: row.RetryWait, Processing: row.Processing, Dead: row.Dead, OldestDueAt: row.OldestDueAt})
	}
	return out, nil
}
