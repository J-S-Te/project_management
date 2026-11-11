package mysql

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// lockMetricResource 串行化同资源的容量发布、跨项目实际用量与更正。
// 参数：tx 为写事务，seed 为事务外定位的档案身份，day 为北京业务日。
// 返回值：有效资源档案；缺失、停用、身份失效或设备过检定时拒绝。
func lockMetricResource(tx *gorm.DB, seed capabilityRecord, day string) (capabilityRecord, error) {
	row, err := lockMetricResourceIdentity(tx, seed, "")
	if err != nil {
		return row, err
	}
	kind := row.ResourceType
	if row.Status != "ACTIVE" {
		return row, application.PreconditionError("资源已停用，不能登记容量或实际用量")
	}
	if kind == "PERSON" && row.IdentityStatus != "ACTIVE" {
		return row, application.PreconditionError("请先在平台复核人员身份")
	}
	if kind == "EQUIPMENT" {
		date, err := time.ParseInLocation("2006-01-02", day, time.FixedZone("Asia/Shanghai", 8*3600))
		if err != nil {
			return row, application.ErrValidation
		}
		if row.ValidFrom != nil && date.AddDate(0, 0, 1).Add(-time.Nanosecond).Before(*row.ValidFrom) || row.ValidUntil != nil && date.After(*row.ValidUntil) {
			return row, application.PreconditionError("设备容量日期不在有效检定期内")
		}
	}
	return row, nil
}

// metricOrgAllowed 核验显式容量组织属于可信可写范围。
// 参数：filter 为真实项目范围，org 为容量业务归属组织。
// 返回值：允许写入时为true，SELF或PROJECT不会被扩大为组织管理范围。
func metricOrgAllowed(filter platform.ScopeFilter, org string) bool {
	if org == "" {
		return false
	}
	if filter.AllowAll {
		return true
	}
	for _, id := range filter.OrganizationIDs {
		if id == org {
			return true
		}
	}
	return false
}

// PublishMetricCapacity 版本化发布同资源同北京日的明确容量。
// 参数：ctx/filter 为授权请求，actor 为发布人，in 为组织、时段和业务依据。
// 返回值：发布的冻结容量；已有确认或在审用量时禁止改写原组织和时段。
func (r *Repository) PublishMetricCapacity(ctx context.Context, filter platform.ScopeFilter, actor string, in domain.ResourceCapacityInput) (domain.ResourceCapacity, error) {
	var out domain.ResourceCapacity
	seed, err := r.metricResourceSeed(ctx, filter.TenantID, in.ResourceType, in.ResourceID)
	if err != nil {
		return out, err
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resource, err := lockMetricResource(tx, seed, in.BusinessDate)
		if err != nil {
			return err
		}
		if !metricOrgAllowed(filter, in.OrganizationID) {
			return application.ErrForbidden
		}
		replay, hash, err := metricOperation(tx, filter.TenantID, in.IdempotencyKey, []any{"CAPACITY", actor, in}, &out)
		if err != nil || replay {
			return err
		}
		var previous metricCapacityRecord
		err = metricCapacityIdentityQuery(tx, filter.TenantID, in.ResourceType, in.ResourceID, resource.UserID, in.BusinessDate).Take(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if previous.Version != in.ExpectedVersion {
			return application.ErrConflict
		}
		if previous.ID != "" {
			if !metricOrgAllowed(filter, previous.OrganizationID) {
				return application.ErrForbidden
			}
			var count int64
			if err = tx.Model(&metricUsageRecord{}).Where("tenant_id=? AND capacity_id=? AND status IN ?", filter.TenantID, previous.ID, []string{"PENDING", "CONFIRMED", "CANCEL_PENDING"}).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return application.PreconditionError("原容量存在待审或确认用量，请先独立审核取消，再发布更正容量并重新确认用量")
			}
			if err = tx.Model(&metricCapacityRecord{}).Where("id=?", previous.ID).Update("status", "SUPERSEDED").Error; err != nil {
				return err
			}
		}
		minutes, err := domain.ValidateMetricIntervals(in.BusinessDate, in.Intervals)
		if err != nil {
			return application.ValidationError(err.Error())
		}
		out = domain.ResourceCapacity{ID: ulid.Make().String(), TenantID: filter.TenantID, ResourceType: in.ResourceType, ResourceID: in.ResourceID, IdentityID: resource.UserID, BusinessDate: in.BusinessDate, OrganizationID: in.OrganizationID, OrganizationWitnessID: in.OrganizationWitnessID, Intervals: in.Intervals, Minutes: minutes, Version: previous.Version + 1, Status: "PUBLISHED", Note: in.Note, PublishedBy: actor, CreatedAt: time.Now().UTC()}
		if in.ResourceType == "EQUIPMENT" {
			out.IdentityID = ""
		}
		if err = tx.Create(&metricCapacityRecord{out}).Error; err != nil {
			return err
		}
		return saveMetricOperation(tx, filter.TenantID, in.IdempotencyKey, hash, actor, "CAPACITY_PUBLISHED", out.ID, out)
	})
	return out, err
}

// metricUsageCapacity 冻结实际用量对应的有效容量与业务组织。
// 参数：tx 为身份锁内事务，tenant 为租户，in 为实际时段，identity 为稳定人员身份。
// 返回值：完整覆盖该实际时段的容量；缺失或未覆盖时失败关闭。
func metricUsageCapacity(tx *gorm.DB, tenant string, in domain.ResourceUsageInput, identity string) (metricCapacityRecord, error) {
	var cap metricCapacityRecord
	err := metricCapacityIdentityQuery(tx, tenant, in.ResourceType, in.ResourceID, identity, in.BusinessDate).Take(&cap).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return cap, application.PreconditionError("请先发布明确可用容量；不能按每天8小时推算")
	}
	if err != nil {
		return cap, err
	}
	if !domain.MetricContains(cap.Intervals, domain.MetricInterval{Start: in.Start, End: in.End}) {
		return cap, application.PreconditionError("实际时段超出明确容量，请先登记加班或新增容量")
	}
	return cap, nil
}

// metricUsageOverlap 检查同一资源跨项目实际时段不可重复占用。
// 参数：tx 为资源锁内事务，usage 为待确认或提交用量，ignore 为合法更正的原记录。
// 返回值：发现半开区间相交时返回冲突，紧邻时段允许。
func metricUsageOverlap(tx *gorm.DB, usage *domain.ResourceUsage, ignore string) error {
	// 用量身份的预读可能先于资源锁建立RR快照；冲突检查必须采用当前读，
	// 否则后取得资源锁的事务仍看不到前一事务刚提交的待审区间。
	var conflicts []metricUsageRecord
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
		Where("tenant_id=? AND resource_type=? AND id<>? AND id<>? AND status IN ? AND started_at < ? AND ended_at > ?", usage.TenantID, usage.ResourceType, usage.ID, ignore, []string{"PENDING", "CONFIRMED", "CANCEL_PENDING"}, usage.End, usage.Start)
	if usage.ResourceType == "PERSON" {
		query = query.Where("identity_id=?", usage.IdentityID)
	} else {
		query = query.Where("resource_id=?", usage.ResourceID)
	}
	err := query.Limit(1).Find(&conflicts).Error
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return application.ConflictError("同一资源存在跨项目重叠的待审或已确认实际时段")
	}
	return nil
}

// CreateMetricUsage 登记不可变实际用量草稿，更正通过关联新记录完成。
// 参数：ctx/filter 为授权范围，actor 为登记人，canPlan 为显式计划登记能力，in 为实际时段和证据。
// 返回值：草稿；未获项目范围、非本人无代登能力、缺容量或更正身份错误时拒绝。
func (r *Repository) CreateMetricUsage(ctx context.Context, filter platform.ScopeFilter, actor string, canPlan bool, in domain.ResourceUsageInput) (domain.ResourceUsage, error) {
	var out domain.ResourceUsage
	seed, err := r.metricResourceSeed(ctx, filter.TenantID, in.ResourceType, in.ResourceID)
	if err != nil {
		return out, err
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resource, err := lockMetricResource(tx, seed, in.BusinessDate)
		if err != nil {
			return err
		}
		if !canPlan && (in.ResourceType != "PERSON" || resource.UserID != actor) {
			return application.ErrForbidden
		}
		scoped := &Repository{db: tx}
		item, err := scoped.GetServiceItem(ctx, filter, in.ServiceItemID)
		if err != nil {
			return err
		}
		replay, hash, err := metricOperation(tx, filter.TenantID, in.IdempotencyKey, []any{"USAGE_CREATE", actor, in}, &out)
		if err != nil || replay {
			return err
		}
		cap, err := metricUsageCapacity(tx, filter.TenantID, in, resource.UserID)
		if err != nil {
			return err
		}
		if in.CorrectsID != "" {
			var old metricUsageRecord
			if err = tx.Where("tenant_id=? AND id=?", filter.TenantID, in.CorrectsID).Take(&old).Error; err != nil {
				return mapNotFound(err)
			}
			if old.Status != "CONFIRMED" || old.ResourceType != in.ResourceType || old.ResourceID != in.ResourceID || old.ServiceItemID != in.ServiceItemID {
				return application.ErrConflict
			}
		}
		now := time.Now().UTC()
		out = domain.ResourceUsage{ID: ulid.Make().String(), TenantID: filter.TenantID, ResourceType: in.ResourceType, ResourceID: in.ResourceID, IdentityID: cap.IdentityID, ProjectID: item.ProjectID, ServiceItemID: item.ID, BusinessDate: in.BusinessDate, OrganizationID: cap.OrganizationID, CapacityID: cap.ID, Start: in.Start.UTC(), End: in.End.UTC(), Minutes: int64(in.End.Sub(in.Start) / time.Minute), Status: "DRAFT", Version: 1, Note: in.Note, Evidence: in.Evidence, CorrectsID: in.CorrectsID, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
		if err = tx.Create(&metricUsageRecord{out}).Error; err != nil {
			return err
		}
		return saveMetricOperation(tx, filter.TenantID, in.IdempotencyKey, hash, actor, "USAGE_CREATED", out.ID, out)
	})
	return out, err
}

// MutateMetricUsage 完成实际用量提交、独立确认、退回及独立取消更正。
// 参数：ctx/filter 为授权范围，actor 为命令人，id/action/in 为用量动作与并发版本。
// 返回值：新状态；自审、过期版本、跨项目重叠或容量已替换时拒绝。
func (r *Repository) MutateMetricUsage(ctx context.Context, filter platform.ScopeFilter, actor, id, action string, in domain.ResourceUsageDecision) (domain.ResourceUsage, error) {
	var out domain.ResourceUsage
	var row metricUsageRecord
	// 资源身份不可变；在事务外定位身份，避免提前建立RR一致性快照。
	// 事务内仍按资源主锁→用量行锁顺序重读全部业务字段并重新授权。
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND id=?", filter.TenantID, id).Take(&row).Error; err != nil {
		return out, mapNotFound(err)
	}
	seed, err := r.metricResourceSeed(ctx, filter.TenantID, row.ResourceType, row.ResourceID)
	if err != nil {
		return out, err
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 取消与独立更正属于历史事实收口，资源后续停用不能阻断它们。
		if _, err := lockMetricResourceIdentity(tx, seed, row.IdentityID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", filter.TenantID, id).Take(&row).Error; err != nil {
			return err
		}
		scoped := &Repository{db: tx}
		if _, err := scoped.GetServiceItem(ctx, filter, row.ServiceItemID); err != nil {
			return err
		}
		replay, hash, err := metricOperation(tx, filter.TenantID, in.IdempotencyKey, []any{action, actor, id, in}, &out)
		if err != nil || replay {
			return err
		}
		if action == "DECISION" && !metricOrgAllowed(filter, row.OrganizationID) {
			return application.ErrForbidden
		}
		if row.Version != in.ExpectedVersion {
			return application.ErrConflict
		}
		if err = metricUsageTransition(tx, &row, actor, action, in); err != nil {
			return err
		}
		row.Version++
		row.UpdatedAt = time.Now().UTC()
		if err = tx.Save(&row).Error; err != nil {
			return err
		}
		out = row.ResourceUsage
		return saveMetricOperation(tx, filter.TenantID, in.IdempotencyKey, hash, actor, "USAGE_"+action, out.ID, out)
	})
	return out, err
}

// metricUsageTransition 在资源锁内核验真实状态机和独立确认。
// 参数：tx 为写事务，row 为当前用量，actor/action/in 为操作者及决定。
// 返回值：非法状态、审核人与登记人相同或替代原记录失效时拒绝。
func metricUsageTransition(tx *gorm.DB, row *metricUsageRecord, actor, action string, in domain.ResourceUsageDecision) error {
	if action == "SUBMIT" {
		if (row.Status != "DRAFT" && row.Status != "RETURNED") || actor != row.CreatedBy {
			return application.ErrConflict
		}
		cap, err := metricUsageCapacity(tx, row.TenantID, domain.ResourceUsageInput{ResourceType: row.ResourceType, ResourceID: row.ResourceID, BusinessDate: row.BusinessDate, Start: row.Start, End: row.End}, row.IdentityID)
		if err != nil {
			return err
		}
		if cap.ID != row.CapacityID {
			return application.PreconditionError("容量已更正，请取消草稿并按新容量重新登记")
		}
		if err = metricUsageOverlap(tx, &row.ResourceUsage, row.CorrectsID); err != nil {
			return err
		}
		row.Status = "PENDING"
		row.SubmittedBy = actor
		return nil
	}
	if action == "CANCEL" {
		if actor != row.CreatedBy {
			return application.ErrForbidden
		}
		if row.Status == "CONFIRMED" {
			row.Status = "CANCEL_PENDING"
			row.DecisionNote = in.Note
			return nil
		}
		if row.Status != "DRAFT" && row.Status != "RETURNED" && row.Status != "PENDING" {
			return application.ErrConflict
		}
		row.Status = "CANCELLED"
		row.DecisionNote = in.Note
		return nil
	}
	if actor == row.CreatedBy || actor == row.SubmittedBy {
		return application.ErrForbidden
	}
	if row.Status == "CANCEL_PENDING" {
		if in.Decision == "CONFIRMED" {
			row.Status = "CANCELLED"
		} else {
			row.Status = "CONFIRMED"
		}
	} else {
		if row.Status != "PENDING" {
			return application.ErrConflict
		}
		row.Status = in.Decision
		if in.Decision == "CONFIRMED" {
			if err := metricUsageOverlap(tx, &row.ResourceUsage, row.CorrectsID); err != nil {
				return err
			}
			if row.CorrectsID != "" {
				result := tx.Model(&metricUsageRecord{}).Where("tenant_id=? AND id=? AND status='CONFIRMED'", row.TenantID, row.CorrectsID).Updates(map[string]any{"status": "REPLACED", "updated_at": time.Now().UTC(), "version": gorm.Expr("version+1")})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return application.ErrConflict
				}
			}
		}
	}
	row.DecidedBy = actor
	row.DecisionNote = in.Note
	return nil
}

// ListMetricCapacities 按明确组织或本人范围分页返回容量，不允许项目权限扩大为资源租户全量。
// 参数：ctx/filter 为授权范围，page 为页码。
// 返回值：最多50个容量版本和查询错误。
func (r *Repository) ListMetricCapacities(ctx context.Context, filter platform.ScopeFilter, page int) ([]domain.ResourceCapacity, error) {
	query := r.db.WithContext(ctx).Model(&metricCapacityRecord{}).Where("tenant_id=?", filter.TenantID)
	query = metricResourceReadScope(query, filter, false)
	var rows []metricCapacityRecord
	err := query.Order("business_date DESC,created_at DESC,id DESC").Offset((page - 1) * 50).Limit(50).Find(&rows).Error
	out := make([]domain.ResourceCapacity, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ResourceCapacity)
	}
	return out, err
}

// metricResourceReadScope 构造非空的组织、本人及已授权代登记录范围。
// 参数：query 为租户查询，filter 为可信范围，includeCreator 为是否允许读取本人在可见项目的代登记录。
// 返回值：已添加范围约束的查询，空身份不会匹配所有设备空身份。
func metricResourceReadScope(query *gorm.DB, filter platform.ScopeFilter, includeCreator bool) *gorm.DB {
	if filter.AllowAll {
		return query
	}
	conditions := []string{}
	args := []any{}
	if len(filter.OrganizationIDs) > 0 {
		conditions = append(conditions, "organization_id IN ?")
		args = append(args, filter.OrganizationIDs)
	}
	if filter.AllowSelf && filter.UserID != "" {
		conditions = append(conditions, "identity_id=?")
		args = append(args, filter.UserID)
	}
	if includeCreator && filter.UserID != "" {
		scoped := query.Session(&gorm.Session{NewDB: true})
		items := applyServiceItemScope(scoped.Model(&serviceItemRecord{}).Select("pm_service_item.id"), scoped, filter)
		conditions = append(conditions, "(created_by=? AND service_item_id IN (?))")
		args = append(args, filter.UserID, items)
	}
	if len(conditions) == 0 {
		return query.Where("1=0")
	}
	return query.Where("("+strings.Join(conditions, " OR ")+")", args...)
}

// ListMetricUsages 按可信项目范围或本人分页读取实际用量。
// 参数：ctx/filter 为授权范围，page 为页码。
// 返回值：最多50条用量；不暴露其他项目成员的私有明细。
func (r *Repository) ListMetricUsages(ctx context.Context, filter platform.ScopeFilter, page int) ([]domain.ResourceUsage, error) {
	query := r.db.WithContext(ctx).Model(&metricUsageRecord{}).Where("tenant_id=?", filter.TenantID)
	query = metricResourceReadScope(query, filter, true)
	var rows []metricUsageRecord
	err := query.Order("business_date DESC,created_at DESC,id DESC").Offset((page - 1) * 50).Limit(50).Find(&rows).Error
	out := make([]domain.ResourceUsage, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ResourceUsage)
	}
	return out, err
}
