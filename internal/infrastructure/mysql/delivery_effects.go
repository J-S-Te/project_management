package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type deliveryEffectsRecord struct {
	ID               uint64 `gorm:"primaryKey;autoIncrement"`
	TenantID         string
	SourceEventID    string
	Payload          []byte `gorm:"type:json"`
	Status           string
	RetryCount       uint
	NextRetryAt      *time.Time
	LockedBy         string
	LockedUntil      *time.Time
	LastErrorSummary string
	CreatedAt        time.Time
	CompletedAt      *time.Time
}

func (deliveryEffectsRecord) TableName() string { return "pm_delivery_effects_outbox" }

func createDeliveryEffects(tx *gorm.DB, event domain.DeliveryEvent) error {
	if event.Type == application.EventAutomationTriggered || event.Type == application.EventWarningTriggered {
		return nil
	}
	var automationRows []ruleRow
	if err := tx.Table("pm_automation").Where("tenant_id = ? AND enabled = ? AND TRIM(`trigger`) = ?", event.TenantID, true, event.Type).Order("id").Scan(&automationRows).Error; err != nil {
		return err
	}
	automationRules := make([]domain.Rule, 0, len(automationRows))
	for _, row := range automationRows {
		automationRules = append(automationRules, ruleFromRow(row))
	}
	warnings := []domain.Rule{}
	if event.Type == application.EventExecutionTeamAssigned {
		var rows []ruleRow
		if err := tx.Table("pm_warning_rule").Where("tenant_id = ? AND enabled = ?", event.TenantID, true).Order("id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			warnings = append(warnings, ruleFromRow(row))
		}
	}
	effects := application.SnapshotDeliveryEffects(event, automationRules, warnings)
	if len(effects.Targets) == 0 && effects.WarningPayload == nil && effects.RoleNotification == nil {
		return nil
	}
	payload, err := json.Marshal(effects)
	if err != nil {
		return err
	}
	return tx.Create(&deliveryEffectsRecord{TenantID: event.TenantID, SourceEventID: event.ID, Payload: payload, Status: "PENDING", CreatedAt: event.CreatedAt}).Error
}

func (r *Repository) ClaimDeliveryEffects(ctx context.Context, tenantID, workerID string, limit int, now time.Time) ([]domain.DeliveryEffectsItem, error) {
	if err := application.ValidateOutboxIdentity(tenantID, workerID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var claimed []deliveryEffectsRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("tenant_id = ? AND status IN ? AND (next_retry_at IS NULL OR next_retry_at <= ?) AND (locked_until IS NULL OR locked_until <= ?)", tenantID, []string{"PENDING", "RETRY_WAIT", "PROCESSING"}, now, now).Order("id").Limit(limit).Find(&claimed).Error; err != nil {
			return err
		}
		ids := make([]uint64, 0, len(claimed))
		for _, row := range claimed {
			ids = append(ids, row.ID)
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Model(&deliveryEffectsRecord{}).Where("tenant_id = ? AND id IN ?", tenantID, ids).Updates(map[string]any{"status": "PROCESSING", "locked_by": workerID, "locked_until": now.Add(2 * time.Minute)}).Error
	})
	if err != nil {
		return nil, err
	}
	items := make([]domain.DeliveryEffectsItem, 0, len(claimed))
	for _, row := range claimed {
		var effects domain.DeliveryEffects
		invalidPayload := json.Unmarshal(row.Payload, &effects) != nil
		if invalidPayload {
			// Return the claimed identity without consuming a business attempt.
			// Only the application can recheck the license after the claim.
			effects = domain.DeliveryEffects{}
		} else {
			effects.Event.TenantID = row.TenantID
		}
		items = append(items, domain.DeliveryEffectsItem{ID: row.ID, TenantID: row.TenantID, Effects: effects, RetryCount: row.RetryCount, InvalidPayload: invalidPayload})
	}
	return items, nil
}

func (r *Repository) CompleteDeliveryEffects(ctx context.Context, tenantID string, id uint64, workerID string, events []domain.DeliveryEvent, messages []domain.NotificationMessage, now time.Time) error {
	if err := application.ValidateOutboxIdentity(tenantID, workerID); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job deliveryEffectsRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ? AND status = 'PROCESSING' AND locked_by = ? AND locked_until > ?", tenantID, id, workerID, now).Take(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return application.ErrConflict
			}
			return err
		}
		for _, event := range events {
			if event.TenantID != job.TenantID || (event.Type != application.EventAutomationTriggered && event.Type != application.EventWarningTriggered) {
				return application.ErrValidation
			}
			// 派生记录仅含审计数据，重放绝不能执行业务状态变更。
			if err := createEvent(tx, event); err != nil {
				return err
			}
		}
		for _, message := range messages {
			payload, err := json.Marshal(message)
			if err != nil {
				return err
			}
			record := notificationOutboxRecord{EventID: message.EventID, TenantID: job.TenantID, IdempotencyKey: message.IdempotencyKey, EventType: message.EventType, AggregateType: message.ReferenceType, AggregateID: message.ReferenceID, Payload: payload, Status: "PENDING", CreatedAt: message.OccurredAt}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error; err != nil {
				return err
			}
		}
		return tx.Model(&job).Where("tenant_id = ? AND status = 'PROCESSING' AND locked_by = ? AND locked_until > ?", tenantID, workerID, now).Updates(map[string]any{"status": "COMPLETED", "completed_at": now, "locked_by": "", "locked_until": nil, "last_error_summary": ""}).Error
	})
}

func (r *Repository) RetryDeliveryEffects(ctx context.Context, tenantID string, id uint64, workerID string, now, next time.Time, failureCode string) error {
	if err := application.ValidateOutboxIdentity(tenantID, workerID); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&deliveryEffectsRecord{}).Where("tenant_id = ? AND id = ? AND status = 'PROCESSING' AND locked_by = ? AND locked_until > ?", tenantID, id, workerID, now).Updates(map[string]any{"status": "RETRY_WAIT", "retry_count": gorm.Expr("retry_count + 1"), "next_retry_at": next, "locked_by": "", "locked_until": nil, "last_error_summary": failureCode})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}
