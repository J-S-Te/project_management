package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

// StreamProjectSnapshot keeps every source read on one read-only RR transaction.
// It never publishes an end record; only a successful commit returns a summary.
func (r *Repository) StreamProjectSnapshot(ctx context.Context, tenant string, limits application.ProjectSnapshotLimits, sink application.ProjectSnapshotSink) (domain.ProjectSnapshotSummary, error) {
	empty := domain.ProjectSnapshotSummary{}
	if err := limits.Validate(); err != nil {
		return empty, err
	}
	if tenant == "" || strings.TrimSpace(tenant) != tenant || sink == nil {
		return empty, application.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	tx := r.db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		return empty, tx.Error
	}
	defer tx.Rollback()
	// All helpers belong to this tx-bound repository, never to the original DB.
	reader := NewRepository(tx)
	stream := application.NewProjectSnapshotStream(ctx, tenant, limits, sink)
	header := domain.ProjectSnapshotHeader{Type: "header", SchemaVersion: 1, TenantID: tenant, SnapshotID: ulid.Make().String(), SourceSnapshotAt: time.Now().UTC()}
	if err := stream.Begin(header); err != nil {
		return empty, err
	}
	cursor := ""
	total, serviceItems := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		// One extra project detects overflow without an unbounded COUNT or full scan.
		batchLimit := min(limits.BatchSize, limits.MaxProjects-total+1)
		var projects []projectRecord
		if err := snapshotProjectsQuery(reader.db.WithContext(ctx), tenant, cursor, batchLimit).Find(&projects).Error; err != nil {
			return empty, err
		}
		if len(projects) == 0 {
			break
		}
		if len(projects) > limits.MaxProjects-total {
			return empty, application.ErrProjectSnapshotLimit
		}
		ids := projectIDsOf(projects)
		inputs, count, err := reader.snapshotActiveItems(ctx, tenant, ids, limits.MaxServiceItems-serviceItems)
		if err != nil {
			return empty, err
		}
		serviceItems += count
		modified, err := reader.snapshotSourceUpdatedAt(ctx, tenant, ids)
		if err != nil {
			return empty, err
		}
		for _, p := range projects {
			projection := domain.NewProjectProjection(projectFromRecord(p), header.SnapshotID, inputs[p.ID], modified[p.ID])
			if err := stream.Project(projection); err != nil {
				return empty, err
			}
			total++
			cursor = p.ID
		}
	}
	summary, err := stream.Summary()
	if err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := tx.Commit().Error; err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return summary, nil
}

func (r *Repository) snapshotActiveItems(ctx context.Context, tenant string, ids []string, remaining int) (map[string][]domain.ProjectSnapshotItem, int, error) {
	type state struct{ ProjectID, Status, ReportStatus string }
	var rows []state
	// Model supplies archived_at IS NULL. The extra item detects a giant project
	// before the complete collection can consume unbounded application memory.
	err := r.db.WithContext(ctx).Model(&serviceItemRecord{}).Select("project_id, status, report_status").Where("tenant_id = ? AND project_id IN ?", tenant, ids).Order("id ASC").Limit(remaining + 1).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	if len(rows) > remaining {
		return nil, 0, application.ErrProjectSnapshotLimit
	}
	result := make(map[string][]domain.ProjectSnapshotItem, len(ids))
	for _, row := range rows {
		result[row.ProjectID] = append(result[row.ProjectID], domain.ProjectSnapshotItem{Status: row.Status, ReportStatus: row.ReportStatus})
	}
	return result, len(rows), nil
}

func (r *Repository) snapshotSourceUpdatedAt(ctx context.Context, tenant string, ids []string) (map[string]time.Time, error) {
	// Include archived rows: their removal changes the active projection even when
	// soft delete did not touch updated_at. Aggregate returns at most one row/project.
	rows, err := r.db.WithContext(ctx).Unscoped().Model(&serviceItemRecord{}).Select("project_id, MAX(updated_at), MAX(archived_at)").Where("tenant_id = ? AND project_id IN ?", tenant, ids).Group("project_id").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]time.Time, len(ids))
	for rows.Next() {
		var id string
		var updated, archived any
		if err := rows.Scan(&id, &updated, &archived); err != nil {
			return nil, err
		}
		latest := time.Time{}
		for _, value := range []any{updated, archived} {
			t, err := snapshotSQLTime(value)
			if err != nil {
				return nil, err
			}
			if t.After(latest) {
				latest = t
			}
		}
		result[id] = latest
	}
	return result, rows.Err()
}

// MySQL parseTime yields time.Time; aggregate expressions and SQLite fixtures may
// yield SQL datetime bytes/text instead. No timezone inference from local process.
func snapshotSQLTime(value any) (time.Time, error) {
	switch v := value.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return v.UTC(), nil
	case []byte:
		return snapshotSQLTime(string(v))
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999 -0700 MST"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC(), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("invalid project snapshot source timestamp")
}

func snapshotProjectsQuery(db *gorm.DB, tenant, cursor string, limit int) *gorm.DB {
	if db.Dialector.Name() == "mysql" {
		// MySQL schema defaults to unicode_ci; use byte comparison for protocol IDs.
		return db.Where("tenant_id = ? AND BINARY id > BINARY ?", tenant, cursor).Order("BINARY id ASC").Limit(limit)
	}
	return db.Where("tenant_id = ? AND id > ?", tenant, cursor).Order("id ASC").Limit(limit)
}
