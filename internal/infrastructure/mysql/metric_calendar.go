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

type metricCalendarRuleRecord struct{ domain.MetricCalendarRule }

// TableName 指定组织工作日规则表。
func (metricCalendarRuleRecord) TableName() string { return "pm_metric_work_calendar" }

type metricCalendarExceptionRecord struct{ domain.MetricCalendarException }

// TableName 指定资源容量例外表。
func (metricCalendarExceptionRecord) TableName() string { return "pm_metric_calendar_exception" }

type metricAutoConfirmSettingRecord struct {
	TenantID  string    `gorm:"column:tenant_id;primaryKey;size:64"`
	Enabled   bool      `gorm:"column:enabled"`
	Version   uint64    `gorm:"column:version"`
	UpdatedBy string    `gorm:"column:updated_by;size:64"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName 指定强证据自动确认策略表。
func (metricAutoConfirmSettingRecord) TableName() string { return "pm_metric_auto_confirm_setting" }

// GetMetricCalendarRules 读取组织全部星期规则。
func (r *Repository) GetMetricCalendarRules(ctx context.Context, tenant, organization string) ([]domain.MetricCalendarRule, error) {
	var rows []metricCalendarRuleRecord
	err := r.db.WithContext(ctx).Where("tenant_id=? AND organization_id=?", tenant, organization).Order("weekday,id").Find(&rows).Error
	out := make([]domain.MetricCalendarRule, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.MetricCalendarRule)
	}
	return out, err
}

// SaveMetricCalendarRule 版本化保存组织某个星期的明确可用时段。
func (r *Repository) SaveMetricCalendarRule(ctx context.Context, filter platform.ScopeFilter, actor string, in domain.MetricCalendarRuleInput) (domain.MetricCalendarRule, error) {
	var out domain.MetricCalendarRule
	if !metricOrgAllowed(filter, in.OrganizationID) {
		return out, application.ErrForbidden
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		replay, hash, err := metricOperation(tx, filter.TenantID, in.IdempotencyKey, []any{"CALENDAR_RULE", actor, in}, &out)
		if err != nil || replay {
			return err
		}
		var previous metricCalendarRuleRecord
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND organization_id=? AND weekday=?", filter.TenantID, in.OrganizationID, in.Weekday).Take(&previous).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if in.ExpectedVersion != 0 {
				return application.ErrConflict
			}
			out = domain.MetricCalendarRule{ID: ulid.Make().String(), TenantID: filter.TenantID, OrganizationID: in.OrganizationID, Weekday: in.Weekday, Intervals: append([]domain.MetricInterval(nil), in.Intervals...), Version: 1, Note: strings.TrimSpace(in.Note), OrganizationWitnessID: in.OrganizationWitnessID, UpdatedBy: actor, UpdatedAt: time.Now().UTC()}
			if err = tx.Create(&metricCalendarRuleRecord{out}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if previous.Version != in.ExpectedVersion {
				return application.ErrConflict
			}
			out = previous.MetricCalendarRule
			out.Intervals = append([]domain.MetricInterval(nil), in.Intervals...)
			out.Version++
			out.Note = strings.TrimSpace(in.Note)
			out.OrganizationWitnessID = in.OrganizationWitnessID
			out.UpdatedBy = actor
			out.UpdatedAt = time.Now().UTC()
			if err = tx.Save(&metricCalendarRuleRecord{out}).Error; err != nil {
				return err
			}
		}
		return saveMetricOperation(tx, filter.TenantID, in.IdempotencyKey, hash, actor, "CALENDAR_RULE_SAVED", out.ID, out)
	})
	return out, err
}

// ListMetricCalendarExceptions 读取组织在日期范围内的资源例外。
func (r *Repository) ListMetricCalendarExceptions(ctx context.Context, tenant, organization, from, to string) ([]domain.MetricCalendarException, error) {
	var rows []metricCalendarExceptionRecord
	err := r.db.WithContext(ctx).Where("tenant_id=? AND organization_id=? AND business_date>=? AND business_date<=?", tenant, organization, from, to).Order("business_date,resource_type,resource_id").Find(&rows).Error
	out := make([]domain.MetricCalendarException, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.MetricCalendarException)
	}
	return out, err
}

// SaveMetricCalendarException 版本化保存资源例外；人员同时冻结稳定身份。
func (r *Repository) SaveMetricCalendarException(ctx context.Context, filter platform.ScopeFilter, actor string, in domain.MetricCalendarExceptionInput) (domain.MetricCalendarException, error) {
	var out domain.MetricCalendarException
	if !metricOrgAllowed(filter, in.OrganizationID) {
		return out, application.ErrForbidden
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		replay, hash, err := metricOperation(tx, filter.TenantID, in.IdempotencyKey, []any{"CALENDAR_EXCEPTION", actor, in}, &out)
		if err != nil || replay {
			return err
		}
		identity := ""
		if in.ResourceType == "PERSON" || in.ResourceType == "EQUIPMENT" {
			var resource capabilityRecord
			if err = tx.Where("tenant_id=? AND resource_type=? AND resource_id=?", filter.TenantID, in.ResourceType, in.ResourceID).Take(&resource).Error; err != nil {
				return mapNotFound(err)
			}
			identity = resource.UserID
		}
		var previous metricCalendarExceptionRecord
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND organization_id=? AND resource_type=? AND resource_id=? AND business_date=?", filter.TenantID, in.OrganizationID, in.ResourceType, in.ResourceID, in.BusinessDate).Take(&previous).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if in.ExpectedVersion != 0 {
				return application.ErrConflict
			}
			out = domain.MetricCalendarException{ID: ulid.Make().String(), TenantID: filter.TenantID, OrganizationID: in.OrganizationID, ResourceType: in.ResourceType, ResourceID: in.ResourceID, IdentityID: identity, BusinessDate: in.BusinessDate, Kind: strings.ToUpper(in.Kind), Intervals: append([]domain.MetricInterval(nil), in.Intervals...), Version: 1, Note: strings.TrimSpace(in.Note), CreatedBy: actor, CreatedAt: time.Now().UTC()}
			if err = tx.Create(&metricCalendarExceptionRecord{out}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if previous.Version != in.ExpectedVersion {
				return application.ErrConflict
			}
			out = previous.MetricCalendarException
			out.Kind = strings.ToUpper(in.Kind)
			out.Intervals = append([]domain.MetricInterval(nil), in.Intervals...)
			out.Version++
			out.Note = strings.TrimSpace(in.Note)
			if err = tx.Save(&metricCalendarExceptionRecord{out}).Error; err != nil {
				return err
			}
		}
		return saveMetricOperation(tx, filter.TenantID, in.IdempotencyKey, hash, actor, "CALENDAR_EXCEPTION_SAVED", out.ID, out)
	})
	return out, err
}

// DeleteMetricCalendarException 以版本围栏删除一条例外并写审计。
func (r *Repository) DeleteMetricCalendarException(ctx context.Context, filter platform.ScopeFilter, actor, id string, expectedVersion uint64, key string) error {
	if key == "" || len(key) > 128 {
		return application.ErrValidation
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row metricCalendarExceptionRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", filter.TenantID, id).Take(&row).Error; err != nil {
			return mapNotFound(err)
		}
		if !metricOrgAllowed(filter, row.OrganizationID) {
			return application.ErrForbidden
		}
		if row.Version != expectedVersion {
			return application.ErrConflict
		}
		replay, hash, err := metricOperation(tx, filter.TenantID, key, []any{"CALENDAR_EXCEPTION_DELETE", actor, id, expectedVersion}, &row)
		if err != nil || replay {
			return err
		}
		if err = tx.Delete(&row).Error; err != nil {
			return err
		}
		return saveMetricOperation(tx, filter.TenantID, key, hash, actor, "CALENDAR_EXCEPTION_DELETED", id, map[string]any{"id": id})
	})
}

// CurrentMetricCapacityVersion 返回资源当日当前 PUBLISHED 容量版本；无容量时返回0。
func (r *Repository) CurrentMetricCapacityVersion(ctx context.Context, tenant, kind, id, day string) (uint64, error) {
	seed, err := r.metricResourceSeed(ctx, tenant, kind, id)
	if err != nil {
		return 0, err
	}
	query := r.db.WithContext(ctx).Model(&metricCapacityRecord{}).Where("tenant_id=? AND resource_type=? AND business_date=? AND status='PUBLISHED'", tenant, kind, day)
	if kind == "PERSON" {
		query = query.Where("identity_id=?", seed.UserID)
	} else {
		query = query.Where("resource_id=?", id)
	}
	var row metricCapacityRecord
	err = query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return row.Version, err
}

// GetMetricAutoConfirmSetting 读取租户策略；没有记录时返回关闭状态。
func (r *Repository) GetMetricAutoConfirmSetting(ctx context.Context, tenant string) (domain.MetricAutoConfirmSetting, error) {
	var row metricAutoConfirmSettingRecord
	err := r.db.WithContext(ctx).Where("tenant_id=?", tenant).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.MetricAutoConfirmSetting{TenantID: tenant}, nil
	}
	if err != nil {
		return domain.MetricAutoConfirmSetting{}, err
	}
	return domain.MetricAutoConfirmSetting{TenantID: tenant, Enabled: row.Enabled, Version: row.Version, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt}, nil
}

// SetMetricAutoConfirmSetting 以乐观版本保存租户策略并写审计。
func (r *Repository) SetMetricAutoConfirmSetting(ctx context.Context, tenant, actor string, in domain.MetricAutoConfirmSettingInput) (domain.MetricAutoConfirmSetting, error) {
	var out domain.MetricAutoConfirmSetting
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 128 {
		return out, application.ErrValidation
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		replay, hash, err := metricOperation(tx, tenant, in.IdempotencyKey, []any{"AUTO_CONFIRM_SETTING", actor, in}, &out)
		if err != nil || replay {
			return err
		}
		var row metricAutoConfirmSettingRecord
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=?", tenant).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if in.ExpectedVersion != 0 {
				return application.ErrConflict
			}
			out = domain.MetricAutoConfirmSetting{TenantID: tenant, Enabled: in.Enabled, Version: 1, UpdatedBy: actor, UpdatedAt: time.Now().UTC()}
			if err = tx.Create(&metricAutoConfirmSettingRecord{TenantID: tenant, Enabled: out.Enabled, Version: out.Version, UpdatedBy: out.UpdatedBy, UpdatedAt: out.UpdatedAt}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if row.Version != in.ExpectedVersion {
				return application.ErrConflict
			}
			row.Enabled = in.Enabled
			row.Version++
			row.UpdatedBy = actor
			row.UpdatedAt = time.Now().UTC()
			if err = tx.Save(&row).Error; err != nil {
				return err
			}
			out = domain.MetricAutoConfirmSetting{TenantID: tenant, Enabled: row.Enabled, Version: row.Version, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt}
		}
		return saveMetricOperation(tx, tenant, in.IdempotencyKey, hash, actor, "AUTO_CONFIRM_SETTING_SAVED", tenant, out)
	})
	return out, err
}
