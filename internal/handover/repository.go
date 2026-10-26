package handover

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/auditctx"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

// GORMRepository 是交接协议的持久化实现（项目管理）。
// 关键不变量：回执存在 ⇒ 责任变更已在同一事务内生效。
type GORMRepository struct{ db *gorm.DB }

var _ Repository = (*GORMRepository)(nil)

func NewGORMRepository(db *gorm.DB) (*GORMRepository, error) {
	if db == nil {
		return nil, errors.New("handover repository requires a database")
	}
	return &GORMRepository{db: db}, nil
}

type freezeRow struct {
	ID         string     `gorm:"column:id;size:26;primaryKey"`
	TenantID   string     `gorm:"column:tenant_id;size:64;uniqueIndex:uk_pm_handover_freeze_batch_user,priority:1"`
	BatchID    string     `gorm:"column:batch_id;size:26;uniqueIndex:uk_pm_handover_freeze_batch_user,priority:2"`
	RequestID  string     `gorm:"column:request_id;size:26"`
	UserID     string     `gorm:"column:user_id;size:64;uniqueIndex:uk_pm_handover_freeze_batch_user,priority:3"`
	Status     string     `gorm:"column:status;size:16"`
	FrozenAt   time.Time  `gorm:"column:frozen_at"`
	ReleasedAt *time.Time `gorm:"column:released_at"`
	Outcome    *string    `gorm:"column:outcome;size:16"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
}

func (freezeRow) TableName() string { return "pm_handover_freeze" }

type receiptRow struct {
	ID             string    `gorm:"column:id;size:26;primaryKey"`
	TenantID       string    `gorm:"column:tenant_id;size:64;uniqueIndex:uk_pm_handover_receipt_key,priority:1"`
	BatchID        string    `gorm:"column:batch_id;size:26"`
	ItemID         string    `gorm:"column:item_id;size:26"`
	ResourceType   string    `gorm:"column:resource_type;size:64"`
	ResourceID     string    `gorm:"column:resource_id;size:128"`
	FromUserID     string    `gorm:"column:from_user_id;size:64"`
	ToUserID       string    `gorm:"column:to_user_id;size:64"`
	IdempotencyKey string    `gorm:"column:idempotency_key;size:160;uniqueIndex:uk_pm_handover_receipt_key,priority:2"`
	Status         string    `gorm:"column:status;size:24"`
	AppliedVersion uint64    `gorm:"column:applied_version"`
	ReasonCode     *string   `gorm:"column:reason_code;size:64"`
	EvidenceRef    *string   `gorm:"column:evidence_ref;size:255"`
	ReceivedAt     time.Time `gorm:"column:received_at"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (receiptRow) TableName() string { return "pm_handover_receipt" }

// 项目终态：已完成 / 已终止（取自 internal/domain 的项目状态常量）。
const (
	projectStatusCompleted  = "已完成"
	projectStatusTerminated = "已终止"
	projectManagerColumn    = "manager_identity_id"
	versionConflictReason   = "PM_VERSION_CONFLICT"
)

// CreateFreeze 幂等创建冻结。
func (repository *GORMRepository) CreateFreeze(ctx context.Context, freeze Freeze) (Freeze, bool, error) {
	record := freezeRow{ID: freeze.ID, TenantID: freeze.TenantID, BatchID: freeze.BatchID, RequestID: freeze.RequestID,
		UserID: freeze.UserID, Status: freeze.Status, FrozenAt: freeze.FrozenAt.UTC(),
		CreatedAt: freeze.FrozenAt.UTC(), UpdatedAt: freeze.FrozenAt.UTC()}
	if err := repository.db.WithContext(ctx).Create(&record).Error; err != nil {
		if !isDuplicate(err) {
			return Freeze{}, false, err
		}
		existing, found, lookupErr := repository.ActiveFreeze(ctx, freeze.TenantID, freeze.UserID)
		if lookupErr != nil || !found {
			return Freeze{}, false, fmt.Errorf("handover freeze lookup failed: %v", lookupErr)
		}
		return existing, false, nil
	}
	return freezeFromRow(record), true, nil
}

// ReleaseFreeze 解除冻结；重复解除幂等。
func (repository *GORMRepository) ReleaseFreeze(ctx context.Context, tenantID, batchID, outcome string, now time.Time) error {
	update := repository.db.WithContext(ctx).Model(&freezeRow{}).
		Where("tenant_id = ? AND batch_id = ? AND status = ?", tenantID, batchID, FreezeFrozen).
		Updates(map[string]any{"status": FreezeReleased, "released_at": now.UTC(), "outcome": outcome, "updated_at": now.UTC()})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 0 {
		var count int64
		if err := repository.db.WithContext(ctx).Model(&freezeRow{}).Where("tenant_id = ? AND batch_id = ?", tenantID, batchID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
	}
	return nil
}

// ActiveFreeze 判断用户当前是否被交接冻结（写路径据此拒绝新责任）。
func (repository *GORMRepository) ActiveFreeze(ctx context.Context, tenantID, userID string) (Freeze, bool, error) {
	var record freezeRow
	err := repository.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND status = ?", tenantID, userID, FreezeFrozen).
		Order("frozen_at DESC").Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Freeze{}, false, nil
	}
	if err != nil {
		return Freeze{}, false, err
	}
	return freezeFromRow(record), true, nil
}

// ActiveFreezeUser 是写路径窄端口适配：只回答"该用户当前是否被冻结"。
func (repository *GORMRepository) ActiveFreezeUser(ctx context.Context, tenantID, userID string) (bool, error) {
	_, found, err := repository.ActiveFreeze(ctx, tenantID, userID)
	return found, err
}

// ListResponsibilities 返回未结束项目中由该用户担任项目经理的项目。
// 项目表没有版本列，因此用 updated_at（毫秒）作为并发标记；服务项与技术工作包
// 在当前 schema 中没有责任人列，无法归属到个人，故不纳入。
func (repository *GORMRepository) ListResponsibilities(ctx context.Context, tenantID, userID, cursor string, limit int) ([]Responsibility, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []struct {
		ID        string    `gorm:"column:id"`
		Name      string    `gorm:"column:name"`
		ManagerID string    `gorm:"column:manager_identity_id"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	query := repository.db.WithContext(ctx).Table("pm_project").
		Select("id, name, manager_identity_id, updated_at").
		Where("tenant_id = ? AND manager_identity_id = ? AND status NOT IN ?", tenantID, userID,
			[]string{projectStatusCompleted, projectStatusTerminated})
	if strings.TrimSpace(cursor) != "" {
		query = query.Where("id > ?", strings.TrimSpace(cursor))
	}
	if err := query.Order("id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]Responsibility, 0, len(rows))
	for _, row := range rows {
		items = append(items, Responsibility{
			ResourceType: ResourceProject, ResourceID: row.ID, ResourceName: row.Name,
			OwnerUserID: row.ManagerID, Category: CategoryProjectManager,
			Version: uint64(row.UpdatedAt.UTC().UnixMilli()), UpdatedAt: row.UpdatedAt,
		})
	}
	serviceItems, err := repository.listServiceItemResponsibilities(ctx, tenantID, userID, cursor, limit)
	if err != nil {
		return nil, err
	}
	items = append(items, serviceItems...)
	return items, nil
}

// listServiceItemResponsibilities 返回该用户在服务项上的实际实施责任。
// 服务项没有版本列，沿用 updated_at（毫秒）作为并发标记；已归档的服务项不再列出。
func (repository *GORMRepository) listServiceItemResponsibilities(ctx context.Context, tenantID, userID, cursor string, limit int) ([]Responsibility, error) {
	type serviceRow struct {
		ID        string    `gorm:"column:id"`
		Name      string    `gorm:"column:name"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	scopes := []struct {
		condition string
		args      []any
	}{
		{"team_lead_id = ?", []any{userID}},
		{"project_manager_id = ?", []any{userID}},
		{"JSON_CONTAINS(COALESCE(engineer_ids, JSON_ARRAY()), JSON_QUOTE(?))", []any{userID}},
	}
	items := make([]Responsibility, 0, len(scopes)*4)
	for _, scope := range scopes {
		var rows []serviceRow
		query := repository.db.WithContext(ctx).Table("pm_service_item").
			Select("id, requirement AS name, updated_at").
			Where("tenant_id = ? AND archived_at IS NULL AND "+scope.condition,
				append([]any{tenantID}, scope.args...)...)
		if strings.TrimSpace(cursor) != "" {
			query = query.Where("id > ?", strings.TrimSpace(cursor))
		}
		if err := query.Order("id ASC").Limit(limit).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			items = append(items, Responsibility{
				ResourceType: ResourceServiceItem, ResourceID: row.ID, ResourceName: row.Name,
				OwnerUserID: userID, Category: CategoryServiceItem,
				Version: uint64(row.UpdatedAt.UTC().UnixMilli()), UpdatedAt: row.UpdatedAt,
			})
		}
	}
	return items, nil
}

// TransferOwnership 在本地事务内变更项目经理并写入幂等回执。
func (repository *GORMRepository) TransferOwnership(ctx context.Context, in TransferInput, now time.Time) (Receipt, error) {
	if in.ResourceType != ResourceProject && in.ResourceType != ResourceServiceItem {
		return Receipt{}, fmt.Errorf("unsupported resource type %s: %w", in.ResourceType, ErrValidation)
	}
	var outcome Receipt
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if receipt, handled, err := existingReceipt(tx, in); err != nil || handled {
			outcome = receipt
			return err
		}
		expected := time.UnixMilli(int64(in.ExpectedVersion)).UTC()
		if in.ResourceType == ResourceServiceItem {
			return transferServiceItemOwnership(tx, in, now, expected, &outcome)
		}
		update := tx.Table("pm_project").
			Where("tenant_id = ? AND id = ? AND manager_identity_id = ? AND updated_at = ? AND status NOT IN ?",
				in.TenantID, in.ResourceID, in.FromUserID, expected, []string{projectStatusCompleted, projectStatusTerminated}).
			Updates(map[string]any{projectManagerColumn: in.ToUserID, "updated_at": now.UTC()})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return writeReceipt(tx, in, now, ReceiptRejected, 0, versionConflictReason, "", &outcome)
		}
		if err := writeReceipt(tx, in, now, ReceiptApplied, uint64(now.UTC().UnixMilli()), "", "pm:project:"+in.ResourceID, &outcome); err != nil {
			return err
		}
		return writeHandoverAudit(tx, in)
	})
	if err != nil {
		return Receipt{}, err
	}
	return outcome, nil
}

// writeHandoverAudit 记录离职交接对项目责任的变更。
// 交接由平台交接批次驱动（机器主体），但必须能追溯到原负责人、承接人与批次。
func writeHandoverAudit(tx *gorm.DB, in TransferInput) error {
	if in.ResourceType != ResourceProject {
		return nil
	}
	meta := auditctx.FromContext(tx.Statement.Context)
	actor := meta.ActorID
	if actor == "" {
		actor = "SYSTEM"
	}
	now := time.Now().UTC()
	return tx.Create(&handoverAuditRow{
		ID: ulid.Make().String(), TenantID: in.TenantID, EventID: ulid.Make().String(),
		ActorID: &actor, ActorType: "SYSTEM",
		Action:       "PROJECT_MANAGEMENT:HANDOVER_TRANSFERRED",
		ResourceType: "project", ResourceID: in.ResourceID,
		LoginIP: auditIP(meta.LoginIP), ClientIP: auditIP(meta.ClientIP), IPSource: auditSource(meta.IPSource),
		Result: "SUCCESS", DeliveryStatus: "pending", NextAttemptAt: now, CreatedAt: now,
	}).Error
}

// handoverAuditRow 直接映射 prj_audit_outbox，供交接事务内写审计使用。
type handoverAuditRow struct {
	ID             string    `gorm:"column:id;primaryKey"`
	TenantID       string    `gorm:"column:tenant_id"`
	EventID        string    `gorm:"column:event_id"`
	ActorID        *string   `gorm:"column:actor_id"`
	ActorType      string    `gorm:"column:actor_type"`
	Action         string    `gorm:"column:action"`
	ResourceType   string    `gorm:"column:resource_type"`
	ResourceID     string    `gorm:"column:resource_id"`
	LoginIP        []byte    `gorm:"column:login_ip"`
	ClientIP       []byte    `gorm:"column:client_ip"`
	IPSource       string    `gorm:"column:ip_source"`
	Result         string    `gorm:"column:result"`
	DeliveryStatus string    `gorm:"column:delivery_status"`
	NextAttemptAt  time.Time `gorm:"column:next_attempt_at"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (handoverAuditRow) TableName() string { return "prj_audit_outbox" }

// auditIP 把文本地址转换为二进制列；空值或非法值保持 NULL。
func auditIP(value string) []byte {
	parsed := net.ParseIP(strings.TrimSpace(value))
	if parsed == nil {
		return nil
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4
	}
	return parsed.To16()
}

func auditSource(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DIRECT", "TRUSTED_PROXY", "LEGACY_PROTOCOL", "UNKNOWN":
		return strings.ToUpper(strings.TrimSpace(value))
	}
	return "UNKNOWN"
}

// FindReceipt 按幂等键查询既有回执。
func (repository *GORMRepository) FindReceipt(ctx context.Context, tenantID, idempotencyKey string) (Receipt, bool, error) {
	var record receiptRow
	err := repository.db.WithContext(ctx).Where("tenant_id = ? AND idempotency_key = ?", tenantID, idempotencyKey).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	return receiptFromRow(record), true, nil
}

func existingReceipt(tx *gorm.DB, in TransferInput) (Receipt, bool, error) {
	var row receiptRow
	err := tx.Where("tenant_id = ? AND idempotency_key = ?", in.TenantID, in.IdempotencyKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	return receiptFromRow(row), true, nil
}

// writeReceipt 写入回执并填充输出（REJECTED 同样落库，便于平台核对）。
func writeReceipt(tx *gorm.DB, in TransferInput, now time.Time, status string, applied uint64, reason, evidence string, outcome *Receipt) error {
	record := receiptRow{ID: shortReceiptID(in.IdempotencyKey), TenantID: in.TenantID, BatchID: in.BatchID, ItemID: in.ItemID,
		ResourceType: in.ResourceType, ResourceID: in.ResourceID, FromUserID: in.FromUserID, ToUserID: in.ToUserID,
		IdempotencyKey: in.IdempotencyKey, Status: status, AppliedVersion: applied,
		ReasonCode: optional(reason), EvidenceRef: optional(evidence), ReceivedAt: now.UTC(), CreatedAt: now.UTC()}
	if err := tx.Create(&record).Error; err != nil && !isDuplicate(err) {
		return err
	}
	*outcome = receiptFromRow(record)
	return nil
}

func freezeFromRow(row freezeRow) Freeze {
	outcome := ""
	if row.Outcome != nil {
		outcome = *row.Outcome
	}
	return Freeze{ID: row.ID, TenantID: row.TenantID, BatchID: row.BatchID, RequestID: row.RequestID,
		UserID: row.UserID, Status: row.Status, FrozenAt: row.FrozenAt, ReleasedAt: row.ReleasedAt, Outcome: outcome}
}

func receiptFromRow(row receiptRow) Receipt {
	reason, evidence := "", ""
	if row.ReasonCode != nil {
		reason = *row.ReasonCode
	}
	if row.EvidenceRef != nil {
		evidence = *row.EvidenceRef
	}
	return Receipt{ItemID: row.ItemID, IdempotencyKey: row.IdempotencyKey, Status: row.Status,
		AppliedVersion: row.AppliedVersion, ReasonCode: reason, EvidenceRef: evidence, ReceivedAt: row.ReceivedAt}
}

func optional(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	return &trimmed
}

// shortReceiptID 为回执生成 26 字符主键。
//
// 必须同时满足两个性质：
//  1. 确定性：同一幂等键总是得到同一主键（重复提交会命中唯一键，从而走幂等回执）；
//  2. 抗碰撞：不同幂等键不应生成同一主键（旧实现用 mod 1e6，只有百万种结果，
//     例如 handover:batch:item-6609 与 handover:batch:item-10020 会撞成同一个主键）。
func shortReceiptID(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])
	if len(encoded) > 21 {
		encoded = encoded[:21]
	}
	return "01PMR" + encoded
}

func isDuplicate(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate")
}
