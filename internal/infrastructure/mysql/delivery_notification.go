package mysql

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func createNotificationOutbox(tx *gorm.DB, event domain.DeliveryEvent) error {
	if event.Notification == nil || len(event.Notification.Recipients) == 0 {
		return nil
	}
	payload, err := json.Marshal(event.Notification)
	if err != nil {
		return err
	}
	idempotencyKey := strings.TrimSpace(event.Notification.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = event.ID
	}
	return tx.Create(&notificationOutboxRecord{
		EventID: event.ID, TenantID: event.TenantID, EventType: event.Type,
		IdempotencyKey: idempotencyKey,
		AggregateType:  "service_item", AggregateID: event.ServiceItemID,
		Payload: payload, Status: "PENDING", CreatedAt: event.CreatedAt,
	}).Error
}

func (r *Repository) EnqueueNotification(ctx context.Context, tenantID string, message domain.NotificationMessage) (bool, error) {
	payload, err := json.Marshal(message)
	if err != nil {
		return false, err
	}
	idempotencyKey := strings.TrimSpace(message.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = message.EventID
	}
	record := notificationOutboxRecord{EventID: message.EventID, TenantID: tenantID, IdempotencyKey: idempotencyKey, EventType: message.EventType, AggregateType: message.ReferenceType, AggregateID: message.ReferenceID, Payload: payload, Status: "PENDING", CreatedAt: message.OccurredAt}
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	return result.RowsAffected == 1, result.Error
}

func (r *Repository) ClaimNotificationOutbox(ctx context.Context, tenantID, workerID string, limit int, now time.Time) ([]domain.NotificationOutboxItem, error) {
	if err := application.ValidateOutboxIdentity(tenantID, workerID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var claimed []notificationOutboxRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("tenant_id = ? AND status IN ? AND (next_retry_at IS NULL OR next_retry_at <= ?) AND (locked_until IS NULL OR locked_until <= ?)", tenantID, []string{"PENDING", "RETRY_WAIT", "PROCESSING"}, now, now).
			Order("created_at ASC").Limit(limit).Find(&claimed).Error; err != nil {
			return err
		}
		if len(claimed) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(claimed))
		for _, row := range claimed {
			ids = append(ids, row.ID)
		}
		return tx.Model(&notificationOutboxRecord{}).Where("tenant_id = ? AND id IN ?", tenantID, ids).Updates(map[string]any{
			"status": "PROCESSING", "locked_by": workerID, "locked_until": now.Add(2 * time.Minute),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	items := make([]domain.NotificationOutboxItem, 0, len(claimed))
	for _, row := range claimed {
		var message domain.NotificationMessage
		if err := json.Unmarshal(row.Payload, &message); err != nil {
			return nil, err
		}
		// The persisted key includes the producer fallback when the JSON key was empty.
		message.IdempotencyKey = row.IdempotencyKey
		items = append(items, domain.NotificationOutboxItem{ID: row.ID, Message: message, RetryCount: row.RetryCount})
	}
	return items, nil
}

func (r *Repository) MarkNotificationOutboxSent(ctx context.Context, tenantID string, id uint64, workerID string, now time.Time) error {
	if err := application.ValidateOutboxIdentity(tenantID, workerID); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&notificationOutboxRecord{}).
		Where("tenant_id=? AND id=? AND status='PROCESSING' AND locked_by=? AND locked_until > ?", tenantID, id, workerID, now).
		Updates(map[string]any{"status": "SENT", "sent_at": now, "locked_by": "", "locked_until": nil, "last_error_summary": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

func (r *Repository) MarkNotificationOutboxRetry(ctx context.Context, tenantID string, id uint64, workerID string, now, next time.Time, dead bool) error {
	if err := application.ValidateOutboxIdentity(tenantID, workerID); err != nil {
		return err
	}
	status := "RETRY_WAIT"
	if dead {
		status = "DEAD_LETTER"
	}
	result := r.db.WithContext(ctx).Model(&notificationOutboxRecord{}).
		Where("tenant_id=? AND id=? AND status='PROCESSING' AND locked_by=? AND locked_until > ?", tenantID, id, workerID, now).
		Updates(map[string]any{"status": status, "retry_count": gorm.Expr("retry_count + 1"), "next_retry_at": next, "locked_by": "", "locked_until": nil})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}
