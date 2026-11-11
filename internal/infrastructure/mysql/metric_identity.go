package mysql

import (
	"context"
	"github.com/j-s-te/project-management/internal/application"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type metricResourceMutexRecord struct {
	TenantID     string `gorm:"primaryKey;size:64"`
	ResourceType string `gorm:"primaryKey;size:16"`
	ResourceKey  string `gorm:"primaryKey;size:64"`
}

// TableName 指定人员稳定身份和设备身份共同互斥表。
// 参数：无。
// 返回值：固定表名。
func (metricResourceMutexRecord) TableName() string { return "pm_metric_resource_mutex" }

// metricResourceSeed 在事务外仅解析不可变档案身份，避免提前建立业务RR快照。
// 参数：ctx 为请求，tenant/kind/id 为租户内资源档案键。
// 返回值：待复核档案身份；不存在时返回错误。
func (r *Repository) metricResourceSeed(ctx context.Context, tenant, kind, id string) (capabilityRecord, error) {
	var row capabilityRecord
	err := r.db.WithContext(ctx).Where("tenant_id=? AND resource_type=? AND resource_id=?", tenant, kind, id).Take(&row).Error
	return row, mapNotFound(err)
}

// lockMetricResourceIdentity 统一按稳定身份锁→档案锁的顺序串行化人员跨档案命令。
// 参数：tx 为写事务，seed 为事务外身份，frozenIdentity 为历史用量原始人员身份（新命令为空）。
// 返回值：重新加锁复核的档案；身份漂移、缺失或锁失败时拒绝。
func lockMetricResourceIdentity(tx *gorm.DB, seed capabilityRecord, frozenIdentity string) (capabilityRecord, error) {
	key := seed.ResourceID
	if seed.ResourceType == "PERSON" {
		key = seed.UserID
		if frozenIdentity != "" {
			key = frozenIdentity
		}
		if key == "" {
			return seed, application.PreconditionError("人员档案未绑定稳定平台身份")
		}
	}
	mutex := metricResourceMutexRecord{TenantID: seed.TenantID, ResourceType: seed.ResourceType, ResourceKey: key}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mutex).Error; err != nil {
		return seed, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND resource_type=? AND resource_key=?", seed.TenantID, seed.ResourceType, key).Take(&mutex).Error; err != nil {
		return seed, err
	}
	var row capabilityRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND resource_type=? AND resource_id=?", seed.TenantID, seed.ResourceType, seed.ResourceID).Take(&row).Error; err != nil {
		return row, mapNotFound(err)
	}
	if row.UserID != seed.UserID || row.ResourceType == "PERSON" && row.UserID != key {
		return row, application.ErrConflict
	}
	return row, nil
}

// metricCapacityIdentityQuery 以人员稳定身份或设备档案身份读取同日当前容量。
// 参数：tx 为写事务，tenant/kind/id/identity/day 为已锁定资源和业务日。
// 返回值：带FOR UPDATE当前读的容量查询，不使用锁前RR旧快照。
func metricCapacityIdentityQuery(tx *gorm.DB, tenant, kind, id, identity, day string) *gorm.DB {
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND resource_type=? AND business_date=? AND status='PUBLISHED'", tenant, kind, day)
	if kind == "PERSON" {
		return query.Where("identity_id=?", identity)
	}
	return query.Where("resource_id=?", id)
}
