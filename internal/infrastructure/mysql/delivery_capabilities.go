package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *Repository) UpsertCapability(ctx context.Context, item domain.Capability, actor string) (domain.Capability, error) {
	if item.ID == "" {
		item.ID = ulid.Make().String()
	}
	item.UpdatedAt = time.Now().UTC()
	codes := jsonValue(item.Codes)
	// 人员档案的身份复核状态不能由导入/编辑覆盖：新档案默认 UNLINKED（未关联平台账号）
	// 或 ACTIVE（已关联但尚未复核），真实状态只能由回基础平台的复核写入。
	identityStatus := domain.IdentityStatusUnlinked
	if strings.TrimSpace(item.UserID) != "" {
		identityStatus = firstValue(item.IdentityStatus, domain.IdentityStatusActive)
	}
	rec := capabilityRecord{ID: item.ID, TenantID: item.TenantID, ResourceType: item.ResourceType, ResourceID: item.ResourceID, ResourceName: item.ResourceName, UserID: strings.TrimSpace(item.UserID), CapabilityCodes: codes, ValidFrom: timePtr(item.ValidFrom), ValidUntil: timePtr(item.ValidUntil), Status: item.Status, UsageScope: firstValue(item.UsageScope, domain.EquipmentUsageAny), IdentityStatus: identityStatus, UpdatedAt: item.UpdatedAt, UpdatedBy: actor}
	if item.ResourceType == "PERSON" {
		// 应用层归属检查无法防止并发导入
		// 选择同一生成编号。锁定既有行，绝不能
		// 将冲突插入转为无条件身份覆盖。
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current capabilityRecord
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND resource_type=? AND resource_id=?", item.TenantID, "PERSON", item.ResourceID).Take(&current).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err = tx.Create(&rec).Error; isDuplicateKey(err) {
					return application.ValidationError("资质编号已被并发导入占用，请重新检测")
				}
				return err
			}
			if err != nil {
				return err
			}
			if current.UserID != "" && current.UserID != rec.UserID {
				return application.ValidationError("资质编号已属于其他人员，不能覆盖")
			}
			item.ID = current.ID
			return tx.Model(&capabilityRecord{}).Where("tenant_id=? AND id=?", item.TenantID, current.ID).Updates(map[string]any{
				"resource_name": rec.ResourceName, "user_id": rec.UserID, "capability_codes": rec.CapabilityCodes,
				"valid_from": rec.ValidFrom, "valid_until": rec.ValidUntil, "status": rec.Status,
				"usage_scope": rec.UsageScope, "updated_at": rec.UpdatedAt, "updated_by": actor,
			}).Error
		})
		return item, err
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "resource_type"}, {Name: "resource_id"}}, DoUpdates: clause.AssignmentColumns([]string{"resource_name", "user_id", "capability_codes", "valid_from", "valid_until", "status", "usage_scope", "updated_at", "updated_by"})}).Create(&rec).Error
	return item, err
}

// CreateEquipmentForImport 拒绝预览后已被占用的编号。导入
// 创建绝不能变成对无关并发创建记录的更新。
func (r *Repository) CreateEquipmentForImport(ctx context.Context, item domain.Capability, actor string) (domain.Capability, error) {
	if item.ResourceType != "EQUIPMENT" || item.TenantID == "" || item.ResourceID == "" {
		return item, application.ErrValidation
	}
	item.ID = ulid.Make().String()
	item.UpdatedAt = time.Now().UTC()
	rec := capabilityRecord{ID: item.ID, TenantID: item.TenantID, ResourceType: "EQUIPMENT", ResourceID: item.ResourceID, ResourceName: item.ResourceName, CapabilityCodes: jsonValue(item.Codes), ValidFrom: timePtr(item.ValidFrom), ValidUntil: timePtr(item.ValidUntil), Status: item.Status, UsageScope: firstValue(item.UsageScope, domain.EquipmentUsageAny), IdentityStatus: domain.IdentityStatusUnlinked, UpdatedAt: item.UpdatedAt, UpdatedBy: actor}
	err := r.db.WithContext(ctx).Create(&rec).Error
	if isDuplicateKey(err) {
		return item, application.ValidationError("设备编号已被其他请求占用，请重新预检")
	}
	return item, err
}

// DeleteEquipment 与预留写入方共享设备主记录锁。
// 取得锁后读取有效 JSON 计划：旧计划可能早于每日
// 预留索引。历史或已归还快照保留为不可变证据。
func (r *Repository) DeleteEquipment(ctx context.Context, tenantID, resourceID string) error {
	resourceID = strings.TrimSpace(resourceID)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockEquipmentResources(tx, tenantID, []string{resourceID})
		if err != nil {
			return err
		}
		master, exists := locked[strings.ToLower(resourceID)]
		if !exists {
			return application.ErrNotFound
		}
		var plans []struct {
			ServiceItemID string
			Equipment     []byte
		}
		returned := map[string]bool{}
		if err := tx.Table("pm_impl_plan AS plan").Select("plan.service_item_id, plan.equipment").
			Joins("JOIN pm_service_item AS item ON item.tenant_id=plan.tenant_id AND item.id=plan.service_item_id").
			Where("plan.tenant_id=? AND plan.equipment IS NOT NULL AND item.status IN ?", tenantID, []string{domain.ProjectStatusPreparing, domain.ProjectStatusInProgress, domain.ProjectStatusException, domain.ProjectStatusFieldCompleted}).
			Scan(&plans).Error; err != nil {
			return err
		}
		for _, plan := range plans {
			var resources []domain.PlanResource
			if err := json.Unmarshal(plan.Equipment, &resources); err != nil {
				return application.ConflictError("活动实施计划设备快照异常，请先核对占用")
			}
			for _, resource := range resources {
				if !strings.EqualFold(strings.TrimSpace(resource.ResourceID), master.ResourceID) {
					continue
				}
				if strings.TrimSpace(resource.ReturnedAt) == "" {
					return application.ConflictError("设备仍被服务项「" + plan.ServiceItemID + "」占用，请先归还设备或撤销实施准备后再删除")
				}
				returned[plan.ServiceItemID] = true
			}
		}
		var indexed []equipmentReservationRecord
		if err := tx.Table("pm_equipment_reservation AS reservation").Select("reservation.*").
			Joins("LEFT JOIN pm_service_item AS item ON item.tenant_id=reservation.tenant_id AND item.id=reservation.service_item_id").
			Where("reservation.tenant_id=? AND reservation.resource_id=? AND (item.id IS NULL OR item.status IN ?)", tenantID, master.ResourceID, []string{domain.ProjectStatusPreparing, domain.ProjectStatusInProgress, domain.ProjectStatusException, domain.ProjectStatusFieldCompleted}).
			Scan(&indexed).Error; err != nil {
			return err
		}
		for _, row := range indexed {
			if !returned[row.ServiceItemID] {
				return application.ConflictError("设备仍有活动占用，请先归还设备或核对实施计划后再删除")
			}
		}
		// 旧版归还或终止命令留下的行仅是索引；
		// 检查权威计划后删除主记录时，它们不得继续存在。
		if err := tx.Where("tenant_id=? AND resource_id=?", tenantID, master.ResourceID).Delete(&equipmentReservationRecord{}).Error; err != nil {
			return err
		}
		return tx.Where("tenant_id=? AND id=?", tenantID, master.ID).Delete(&capabilityRecord{}).Error
	})
}

func (r *Repository) ListCapabilities(ctx context.Context, tenant, typ string) ([]domain.Capability, error) {
	q := r.db.WithContext(ctx).Where("tenant_id=?", tenant)
	if typ != "" {
		q = q.Where("resource_type=?", typ)
	}
	var records []capabilityRecord
	if err := q.Order("resource_type, resource_name").Find(&records).Error; err != nil {
		return nil, err
	}
	return capabilitiesFromRecords(records), nil
}

func (r *Repository) FindCapabilities(ctx context.Context, tenant, at string, ids []string) ([]domain.Capability, error) {
	if len(ids) == 0 {
		return []domain.Capability{}, nil
	}
	var records []capabilityRecord
	err := activePersonnelCapabilitiesQuery(r.db.WithContext(ctx), tenant, at, ids).Find(&records).Error
	return capabilitiesFromRecords(records), err
}

func (r *Repository) UpdateCapabilityIdentities(ctx context.Context, tenantID string, statuses map[string]string, checkedAt time.Time) error {
	if len(statuses) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for userID, status := range statuses {
			if err := tx.Model(&capabilityRecord{}).
				Where("tenant_id = ? AND resource_type = ? AND user_id = ?", tenantID, "PERSON", userID).
				Updates(map[string]any{"identity_status": status, "identity_checked_at": checkedAt, "updated_at": checkedAt}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// auditOutboxRecord 是交付事件事务内写下的审计事件（批次 2）。
type auditOutboxRecord struct {
	ID             string     `gorm:"column:id;primaryKey"`
	TenantID       string     `gorm:"column:tenant_id"`
	EventID        string     `gorm:"column:event_id"`
	ActorID        *string    `gorm:"column:actor_id"`
	ActorType      string     `gorm:"column:actor_type"`
	Action         string     `gorm:"column:action"`
	ResourceType   string     `gorm:"column:resource_type"`
	ResourceID     string     `gorm:"column:resource_id"`
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

func activePersonnelCapabilitiesQuery(db *gorm.DB, tenant, _ string, ids []string) *gorm.DB {
	// 人员只校验资质启停状态和基础平台身份状态，不按资质日期限制派工。
	// 日期有效期仅属于设备检定规则。
	return db.Model(&capabilityRecord{}).Where(
		"tenant_id=? AND resource_type='PERSON' AND (resource_id IN ? OR user_id IN ?) AND status='ACTIVE' AND identity_status=?",
		tenant, ids, ids, domain.IdentityStatusActive)
}

func capabilitiesFromRecords(records []capabilityRecord) []domain.Capability {
	out := make([]domain.Capability, 0, len(records))
	for _, v := range records {
		codes := []string{}
		_ = json.Unmarshal(v.CapabilityCodes, &codes)
		item := domain.Capability{ID: v.ID, ResourceType: v.ResourceType, ResourceID: v.ResourceID, ResourceName: v.ResourceName, UserID: v.UserID, Codes: codes, Status: v.Status, UsageScope: firstValue(v.UsageScope, domain.EquipmentUsageAny), IdentityStatus: firstValue(v.IdentityStatus, domain.IdentityStatusUnlinked), UpdatedAt: v.UpdatedAt}
		if v.IdentityCheckedAt != nil {
			item.IdentityCheckedAt = *v.IdentityCheckedAt
		}
		if v.ValidFrom != nil {
			item.ValidFrom = *v.ValidFrom
		}
		if v.ValidUntil != nil {
			item.ValidUntil = *v.ValidUntil
		}
		out = append(out, item)
	}
	return out
}

// auditActorType 区分人工操作与系统自动执行。
// 自动化/告警类事件由 Worker 在源事件之后追加，不能伪装成人工操作。
