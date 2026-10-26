package auditoutbox

import (
	"context"
	"net"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GORMStore 用 prj_audit_outbox 表实现领取与回写。
//
// 领取是短事务：先把到期行标成 processing 并写入租约（locked_at），再返回给调用方；
// 进程崩溃后租约到期，其他 worker 可以重新领取（至少一次投递，平台按 event_id 去重）。
type GORMStore struct {
	DB       *gorm.DB
	Lease    time.Duration
	MaxTries uint
}

type outboxRow struct {
	ID             string     `gorm:"column:id;primaryKey"`
	TenantID       string     `gorm:"column:tenant_id"`
	EventID        string     `gorm:"column:event_id"`
	ActorID        *string    `gorm:"column:actor_id"`
	ActorType      string     `gorm:"column:actor_type"`
	Action         string     `gorm:"column:action"`
	ResourceType   string     `gorm:"column:resource_type"`
	ResourceID     string     `gorm:"column:resource_id"`
	RequestID      *string    `gorm:"column:request_id"`
	LoginIP        []byte     `gorm:"column:login_ip"`
	ClientIP       []byte     `gorm:"column:client_ip"`
	IPSource       string     `gorm:"column:ip_source"`
	Result         string     `gorm:"column:result"`
	DeliveryStatus string     `gorm:"column:delivery_status"`
	Attempts       uint       `gorm:"column:attempts"`
	NextAttemptAt  time.Time  `gorm:"column:next_attempt_at"`
	LockedAt       *time.Time `gorm:"column:locked_at"`
	DeliveredAt    *time.Time `gorm:"column:delivered_at"`
	LastError      *string    `gorm:"column:last_error"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
}

func (outboxRow) TableName() string { return "prj_audit_outbox" }

// Claim 领取到期且未被租约占用的行；超时租约会被回收，避免进程崩溃后事件永久卡住。
func (s *GORMStore) Claim(ctx context.Context, workerID string, limit int, now time.Time) ([]Entry, error) {
	if limit < 1 {
		return nil, nil
	}
	lease := s.Lease
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	leaseCutoff := now.Add(-lease)
	var rows []outboxRow
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 崩溃的认领者会留下 processing 状态。租约到期后，
		// 恢复不可变事件，不能静默搁置其审计。
		query := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("next_attempt_at <= ? AND (locked_at IS NULL OR locked_at < ?) AND delivery_status IN ?",
				now, leaseCutoff, []string{"pending", "retry", "processing"}).
			Order("next_attempt_at ASC, id ASC").Limit(limit)
		if s.MaxTries > 0 {
			// 最后一次尝试崩溃时没有投递结果。允许其
			// 过期的 processing 行重试；恢复后的投递确实失败时，
			// MarkFailed 仍强制次数上限。
			query = query.Where("(attempts < ? OR (delivery_status = ? AND locked_at < ?))", s.MaxTries, "processing", leaseCutoff)
		}
		if err := query.Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			if err := tx.Model(&outboxRow{}).Where("id = ?", rows[i].ID).
				Updates(map[string]any{"delivery_status": "processing", "locked_at": now, "attempts": gorm.Expr("attempts + 1")}).Error; err != nil {
				return err
			}
			rows[i].Attempts++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, Entry{
			ID: row.ID, TenantID: row.TenantID, EventID: row.EventID, ActorID: deref(row.ActorID), ActorType: row.ActorType,
			Action: row.Action, ResourceType: row.ResourceType, ResourceID: row.ResourceID, RequestID: deref(row.RequestID),
			LoginIP: ipString(row.LoginIP), ClientIP: ipString(row.ClientIP), IPSource: row.IPSource,
			Result: row.Result, Attempts: row.Attempts,
		})
	}
	return entries, nil
}

// MarkDelivered 标记投递成功并释放租约。
func (s *GORMStore) MarkDelivered(ctx context.Context, id string, now time.Time) error {
	return s.DB.WithContext(ctx).Model(&outboxRow{}).Where("id = ?", id).
		Updates(map[string]any{"delivery_status": "delivered", "delivered_at": now, "locked_at": nil, "last_error": nil}).Error
}

// MarkFailed 记录失败原因并按退避时间重新排队；达到上限后停在 failed 供告警。
func (s *GORMStore) MarkFailed(ctx context.Context, id, code string, retryAt, now time.Time) error {
	status := "retry"
	if s.MaxTries > 0 {
		var row outboxRow
		if err := s.DB.WithContext(ctx).Select("attempts").Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		if row.Attempts >= s.MaxTries {
			status = "failed"
		}
	}
	return s.DB.WithContext(ctx).Model(&outboxRow{}).Where("id = ?", id).
		Updates(map[string]any{"delivery_status": status, "next_attempt_at": retryAt, "locked_at": nil, "last_error": code}).Error
}

type FailedEntry struct {
	ID, EventID, Action, ResourceType, ResourceID, RequestID, LastError string
	Attempts                                                            uint
	NextAttemptAt                                                       time.Time
	CreatedAt                                                           time.Time
}

// ListFailed 为操作员返回终态失败审计事件。绝不
// 返回其他租户行，也不暴露载荷或证据内容。
func (s *GORMStore) ListFailed(ctx context.Context, tenantID string, limit int) ([]FailedEntry, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	var rows []outboxRow
	if err := s.DB.WithContext(ctx).Where("tenant_id = ? AND delivery_status = ?", tenantID, "failed").Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]FailedEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, FailedEntry{ID: row.ID, EventID: row.EventID, Action: row.Action, ResourceType: row.ResourceType, ResourceID: row.ResourceID, RequestID: deref(row.RequestID), LastError: deref(row.LastError), Attempts: row.Attempts, NextAttemptAt: row.NextAttemptAt, CreatedAt: row.CreatedAt})
	}
	return out, nil
}

// RetryFailed 重置单个终态审计事件。event_id 和原审计
// 数据保持不变，以新的尝试预算重试投递。
func (s *GORMStore) RetryFailed(ctx context.Context, tenantID, id string, now time.Time) (bool, error) {
	result := s.DB.WithContext(ctx).Model(&outboxRow{}).
		Where("tenant_id = ? AND id = ? AND delivery_status = ?", tenantID, id, "failed").
		Updates(map[string]any{"delivery_status": "retry", "attempts": 0, "next_attempt_at": now, "locked_at": nil, "last_error": nil})
	return result.RowsAffected == 1, result.Error
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func ipString(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	if ip := net.IP(value); ip != nil {
		return ip.String()
	}
	return strings.TrimSpace(string(value))
}
