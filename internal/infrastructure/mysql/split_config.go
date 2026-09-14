package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 合同拆解规则配置 v2 的持久化：默认分组规则（每租户一行）+ 检测类别域 + 覆盖规则。
// 三张表都以 tenant_id 为边界；检测类别域在租户首次读取时按原型初始值落库
// （没有任何写入口能"初始化租户"，因此用惰性种子，避免运行时出现空域）。

type splitPolicyRecord struct {
	ID                         int64 `gorm:"primaryKey;autoIncrement"`
	TenantID                   string
	DimensionPrimary           string
	DimensionSecondary         string
	DimensionTertiary          string
	DefaultStatus              string
	GenerateRequirementSummary bool
	RequirementSummaryLocked   bool
	MissingRuleAction          string
	ScopeChangeDetection       bool
	Enabled                    bool
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	UpdatedBy                  string
}

func (splitPolicyRecord) TableName() string { return "pm_split_policy" }

type detectionCategoryRecord struct {
	ID                     int64 `gorm:"primaryKey;autoIncrement"`
	TenantID               string
	Category               string
	SystemStandard         string
	RequiredQualifications string
	SpecialMethod          string
	Enabled                bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
	UpdatedBy              string
}

func (detectionCategoryRecord) TableName() string { return "pm_detection_category" }

type splitOverrideRecord struct {
	ID               int64 `gorm:"primaryKey;autoIncrement"`
	TenantID         string
	Name             string
	MatchConditions  []byte `gorm:"type:json"`
	OverrideSettings []byte `gorm:"type:json"`
	Priority         int
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	UpdatedBy        string
}

func (splitOverrideRecord) TableName() string { return "pm_split_override" }

func splitPolicyFromRecord(record splitPolicyRecord) domain.SplitPolicy {
	return domain.SplitPolicy{
		TenantID: record.TenantID, ID: record.ID,
		DimensionPrimary: record.DimensionPrimary, DimensionSecondary: record.DimensionSecondary,
		DimensionTertiary: record.DimensionTertiary,
		DefaultStatus:     record.DefaultStatus, GenerateRequirementSummary: record.GenerateRequirementSummary,
		RequirementSummaryLocked: record.RequirementSummaryLocked, MissingRuleAction: record.MissingRuleAction,
		ScopeChangeDetection: record.ScopeChangeDetection, Enabled: record.Enabled,
		Updated: record.UpdatedAt.Format("2006-01-02 15:04"),
	}
}

func detectionCategoryFromRecord(record detectionCategoryRecord) domain.DetectionCategory {
	return domain.DetectionCategory{
		TenantID: record.TenantID, ID: record.ID, Category: record.Category,
		SystemStandard: record.SystemStandard, RequiredQualifications: record.RequiredQualifications,
		SpecialMethod: record.SpecialMethod, Enabled: record.Enabled,
		Updated: record.UpdatedAt.Format("2006-01-02 15:04"),
	}
}

func splitOverrideFromRecord(record splitOverrideRecord) domain.SplitOverride {
	item := domain.SplitOverride{
		TenantID: record.TenantID, ID: record.ID, Name: record.Name,
		Priority: record.Priority, Enabled: record.Enabled,
		Updated: record.UpdatedAt.Format("2006-01-02 15:04"),
	}
	if len(record.MatchConditions) > 0 {
		_ = json.Unmarshal(record.MatchConditions, &item.Match)
	}
	if len(record.OverrideSettings) > 0 {
		_ = json.Unmarshal(record.OverrideSettings, &item.Settings)
	}
	return item
}

// GetSplitPolicy 读取默认分组规则；租户未配置时返回原型默认值（不落库），
// 由保存动作真正写入，避免"读取接口产生写入"。
func (r *Repository) GetSplitPolicy(ctx context.Context, tenant string) (domain.SplitPolicy, error) {
	var record splitPolicyRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ?", tenant).Take(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			policy := domain.DefaultSplitPolicy()
			policy.TenantID = tenant
			return policy, nil
		}
		return domain.SplitPolicy{}, err
	}
	return splitPolicyFromRecord(record), nil
}

// SaveSplitPolicy 写入（或覆盖）默认分组规则。整行覆盖：页面提交的就是完整配置。
func (r *Repository) SaveSplitPolicy(ctx context.Context, tenant string, policy domain.SplitPolicy, actor string) (domain.SplitPolicy, error) {
	now := time.Now().UTC()
	record := splitPolicyRecord{
		TenantID: tenant, DimensionPrimary: policy.DimensionPrimary, DimensionSecondary: policy.DimensionSecondary,
		DimensionTertiary: policy.DimensionTertiary,
		DefaultStatus:     policy.DefaultStatus, GenerateRequirementSummary: policy.GenerateRequirementSummary,
		RequirementSummaryLocked: policy.RequirementSummaryLocked, MissingRuleAction: policy.MissingRuleAction,
		ScopeChangeDetection: policy.ScopeChangeDetection, Enabled: policy.Enabled,
		CreatedAt: now, UpdatedAt: now, UpdatedBy: actor,
	}
	// 唯一键冲突即更新：ON DUPLICATE KEY 保持"一个租户一行"的约束不变。
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"dimension_primary", "dimension_secondary", "dimension_tertiary", "default_status", "generate_requirement_summary",
			"requirement_summary_locked", "missing_rule_action", "scope_change_detection", "enabled",
			"updated_at", "updated_by",
		}),
	}).Create(&record).Error; err != nil {
		return domain.SplitPolicy{}, err
	}
	return r.GetSplitPolicy(ctx, tenant)
}

// ListDetectionCategories 返回检测类别域；首次读取时按原型初始值惰性落库。
func (r *Repository) ListDetectionCategories(ctx context.Context, tenant string) ([]domain.DetectionCategory, error) {
	var records []detectionCategoryRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ?", tenant).Order("id").Find(&records).Error; err != nil {
		return nil, err
	}
	if len(records) == 0 {
		seeded, err := r.seedDetectionCategories(ctx, tenant)
		if err != nil {
			return nil, err
		}
		records = seeded
	}
	items := make([]domain.DetectionCategory, 0, len(records))
	for _, record := range records {
		items = append(items, detectionCategoryFromRecord(record))
	}
	counts, err := r.countServiceItemsByCategory(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].ServiceItemCount = counts[strings.ToLower(items[index].Category)]
	}
	return items, nil
}

// seedDetectionCategories 写入原型「Q1 已确认」的初始检测类别域。
// 并发首读可能同时触发，唯一键冲突按已存在处理（另一个请求已经写好）。
func (r *Repository) seedDetectionCategories(ctx context.Context, tenant string) ([]detectionCategoryRecord, error) {
	now := time.Now().UTC()
	records := make([]detectionCategoryRecord, 0, 32)
	for _, item := range domain.DefaultDetectionCategories() {
		records = append(records, detectionCategoryRecord{
			TenantID: tenant, Category: item.Category, SystemStandard: item.SystemStandard,
			RequiredQualifications: item.RequiredQualifications, SpecialMethod: item.SpecialMethod,
			Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: "system-seed",
		})
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&records).Error; err != nil {
		return nil, err
	}
	var stored []detectionCategoryRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ?", tenant).Order("id").Find(&stored).Error; err != nil {
		return nil, err
	}
	return stored, nil
}

// countServiceItemsByCategory 统计每个检测类别被多少个服务项引用（列表页展示）。
func (r *Repository) countServiceItemsByCategory(ctx context.Context, tenant string) (map[string]int, error) {
	type row struct {
		Category string
		Total    int
	}
	var rows []row
	if err := r.db.WithContext(ctx).Table("pm_service_item").
		Select("LOWER(TRIM(category)) AS category, COUNT(*) AS total").
		Where("tenant_id = ?", tenant).Group("LOWER(TRIM(category))").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, item := range rows {
		counts[item.Category] = item.Total
	}
	return counts, nil
}

// SaveDetectionCategory 按「租户 + 检测类别」幂等写入一项检测类别。
func (r *Repository) SaveDetectionCategory(ctx context.Context, tenant string, item domain.DetectionCategory, actor string) (domain.DetectionCategory, error) {
	now := time.Now().UTC()
	record := detectionCategoryRecord{
		TenantID: tenant, Category: item.Category, SystemStandard: item.SystemStandard,
		RequiredQualifications: item.RequiredQualifications, SpecialMethod: item.SpecialMethod,
		Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: actor,
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "category"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"system_standard", "required_qualifications", "special_method", "enabled", "updated_at", "updated_by",
		}),
	}).Create(&record).Error; err != nil {
		return domain.DetectionCategory{}, err
	}
	var stored detectionCategoryRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND category = ?", tenant, item.Category).Take(&stored).Error; err != nil {
		return domain.DetectionCategory{}, err
	}
	return detectionCategoryFromRecord(stored), nil
}

// DeleteDetectionCategory 删除一项检测类别；仍被服务项引用时拒绝，
// 否则历史服务项会指向域外类别（正是"缺规则"要暴露的状态）。
func (r *Repository) DeleteDetectionCategory(ctx context.Context, tenant, category string) error {
	counts, err := r.countServiceItemsByCategory(ctx, tenant)
	if err != nil {
		return err
	}
	if counts[strings.ToLower(strings.TrimSpace(category))] > 0 {
		return application.ConflictError("该检测类别仍被服务项引用，不能删除；如需停用请改为禁用。")
	}
	result := r.db.WithContext(ctx).Where("tenant_id = ? AND category = ?", tenant, strings.TrimSpace(category)).
		Delete(&detectionCategoryRecord{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return application.ErrNotFound
	}
	return nil
}

// ListSplitOverrides 返回覆盖规则，按优先级升序（越小越先匹配）。
func (r *Repository) ListSplitOverrides(ctx context.Context, tenant string) ([]domain.SplitOverride, error) {
	var records []splitOverrideRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ?", tenant).
		Order("priority, id").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]domain.SplitOverride, 0, len(records))
	for _, record := range records {
		items = append(items, splitOverrideFromRecord(record))
	}
	return items, nil
}

// SaveSplitOverride 新增或整行更新一条覆盖规则。
func (r *Repository) SaveSplitOverride(ctx context.Context, tenant string, item domain.SplitOverride, actor string) (domain.SplitOverride, error) {
	match, err := json.Marshal(item.Match)
	if err != nil {
		return domain.SplitOverride{}, err
	}
	settings, err := json.Marshal(item.Settings)
	if err != nil {
		return domain.SplitOverride{}, err
	}
	now := time.Now().UTC()
	if item.ID > 0 {
		result := r.db.WithContext(ctx).Model(&splitOverrideRecord{}).
			Where("tenant_id = ? AND id = ?", tenant, item.ID).
			Updates(map[string]any{
				"name": item.Name, "match_conditions": match, "override_settings": settings,
				"priority": item.Priority, "enabled": item.Enabled, "updated_at": now, "updated_by": actor,
			})
		if result.Error != nil {
			return domain.SplitOverride{}, result.Error
		}
		if result.RowsAffected == 0 {
			return domain.SplitOverride{}, application.ErrNotFound
		}
		return r.GetSplitOverride(ctx, tenant, item.ID)
	}
	record := splitOverrideRecord{
		TenantID: tenant, Name: item.Name, MatchConditions: match, OverrideSettings: settings,
		Priority: item.Priority, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: actor,
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return domain.SplitOverride{}, err
	}
	return splitOverrideFromRecord(record), nil
}

// GetSplitOverride 按租户 + 主键读取一条覆盖规则。
func (r *Repository) GetSplitOverride(ctx context.Context, tenant string, id int64) (domain.SplitOverride, error) {
	var record splitOverrideRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenant, id).Take(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.SplitOverride{}, application.ErrNotFound
		}
		return domain.SplitOverride{}, err
	}
	return splitOverrideFromRecord(record), nil
}

// DeleteSplitOverride 删除一条覆盖规则。
func (r *Repository) DeleteSplitOverride(ctx context.Context, tenant string, id int64) (domain.SplitOverride, error) {
	existing, err := r.GetSplitOverride(ctx, tenant, id)
	if err != nil {
		return domain.SplitOverride{}, err
	}
	result := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenant, id).Delete(&splitOverrideRecord{})
	if result.Error != nil {
		return domain.SplitOverride{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.SplitOverride{}, application.ErrNotFound
	}
	return existing, nil
}
