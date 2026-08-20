package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) ListProjects(ctx context.Context, filter platform.ScopeFilter, q, status string) ([]domain.Project, error) {
	query := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project")
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + q + "%"
		query = query.Where("id LIKE ? OR name LIKE ? OR customer LIKE ? OR contract LIKE ? OR category LIKE ? OR manager LIKE ?", like, like, like, like, like, like)
	}
	var records []projectRecord
	if err := query.Order("id DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]domain.Project, 0, len(records))
	for _, record := range records {
		items = append(items, projectFromRecord(record))
	}
	return items, nil
}
func (r *Repository) GetProject(ctx context.Context, filter platform.ScopeFilter, id string) (domain.Project, error) {
	var record projectRecord
	err := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project").Where("pm_project.id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Project{}, application.ErrNotFound
	}
	return projectFromRecord(record), err
}
func (r *Repository) CreateProject(ctx context.Context, item domain.Project) error {
	return r.db.WithContext(ctx).Create(&projectRecord{ID: item.ID, TenantID: item.TenantID, OwnerOrgID: item.OwnerOrgID, Name: item.Name, Customer: item.Customer, Contract: item.Contract, ContractVersion: item.ContractVersion, SupplementStatus: firstValue(item.SupplementStatus, "NONE"), Services: item.Services, Category: item.Category, Team: item.Team, Manager: item.Manager, OwnerIdentityID: item.OwnerIdentityID, ManagerIdentityID: item.ManagerIdentityID, Health: item.Health, Status: item.Status, Progress: item.Progress, Due: item.Due, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}).Error
}
func (r *Repository) ListServiceItems(ctx context.Context, filter platform.ScopeFilter, projectID string) ([]domain.ServiceItem, error) {
	query := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter)
	if projectID != "" {
		query = query.Where("project_id = ?", projectID)
	}
	var records []serviceItemRecord
	if err := query.Order("id").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]domain.ServiceItem, 0, len(records))
	for _, record := range records {
		items = append(items, serviceFromRecord(record))
	}
	return items, nil
}
func (r *Repository) GetServiceItem(ctx context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	var record serviceItemRecord
	err := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).Where("pm_service_item.id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ServiceItem{}, application.ErrNotFound
	}
	return serviceFromRecord(record), err
}

func (r *Repository) ConfirmServiceItems(ctx context.Context, tenant string, ids []string, actor string) ([]domain.ServiceItem, error) {
	var result []domain.ServiceItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var records []serviceItemRecord
		// 对待确认服务项加排他锁，使状态校验、批量确认和项目状态推进处于同一串行化临界区。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id IN ?", tenant, ids).Find(&records).Error; err != nil {
			return err
		}
		if len(records) != len(unique(ids)) {
			return application.ErrNotFound
		}
		for _, record := range records {
			if record.Status != "待确认" && record.Status != "待复核" && record.Status != "待分配" {
				return application.ErrValidation
			}
		}
		now := time.Now().UTC()
		update := tx.Model(&serviceItemRecord{}).
			Where("tenant_id = ? AND id IN ? AND status IN ?", tenant, ids, []string{"待确认", "待复核", "待分配"}).
			Updates(map[string]any{"status": "待分配", "updated_at": now, "updated_by": actor})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != int64(len(records)) {
			// 条件更新行数不匹配表示锁等待期间状态已变化，不用旧快照覆盖并发操作。
			return application.ErrConflict
		}
		projectIDs := make([]string, 0)
		seenProjects := map[string]bool{}
		for _, record := range records {
			if !seenProjects[record.ProjectID] {
				seenProjects[record.ProjectID] = true
				projectIDs = append(projectIDs, record.ProjectID)
			}
		}
		for _, projectID := range projectIDs {
			var pending int64
			if err := tx.Model(&serviceItemRecord{}).Where("tenant_id = ? AND project_id = ? AND status IN ?", tenant, projectID, []string{"待确认", "待复核"}).Count(&pending).Error; err != nil {
				return err
			}
			if pending == 0 {
				if err := tx.Model(&projectRecord{}).Where("tenant_id = ? AND id = ? AND status = ?", tenant, projectID, "待拆解确认").Updates(map[string]any{"status": "待分配", "updated_at": now}).Error; err != nil {
					return err
				}
			}
		}
		result = make([]domain.ServiceItem, 0, len(records))
		for _, record := range records {
			record.Status = "待分配"
			result = append(result, serviceFromRecord(record))
		}
		return nil
	})
	return result, err
}
func (r *Repository) ListRules(ctx context.Context, tenant, kind string) ([]domain.Rule, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenant)
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	var records []ruleRecord
	if err := query.Order("id").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]domain.Rule, 0, len(records))
	for _, record := range records {
		items = append(items, ruleFromRecord(record))
	}
	return items, nil
}
func (r *Repository) CreateRule(ctx context.Context, item domain.Rule) (domain.Rule, error) {
	record := ruleRecord{TenantID: item.TenantID, Kind: item.Kind, Name: item.Name, Scope: item.Scope, Trigger: item.Trigger, Enabled: item.Enabled, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return item, err
	}
	return ruleFromRecord(record), nil
}
func (r *Repository) SetRuleEnabled(ctx context.Context, tenant string, id int64, enabled bool, actor string) (domain.Rule, error) {
	var record ruleRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND id = ?", tenant, id).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return application.ErrNotFound
			}
			return err
		}
		record.Enabled = enabled
		record.UpdatedAt = time.Now().UTC()
		record.UpdatedBy = actor
		return tx.Save(&record).Error
	})
	return ruleFromRecord(record), err
}
func (r *Repository) Dashboard(ctx context.Context, filter platform.ScopeFilter) (domain.Dashboard, error) {
	result := domain.Dashboard{StatusCounts: map[string]int{}}
	var records []struct {
		Status string
		Count  int
	}
	if err := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project").Select("status, COUNT(*) AS count").Group("status").Scan(&records).Error; err != nil {
		return result, err
	}
	for _, row := range records {
		result.StatusCounts[row.Status] = row.Count
		result.ProjectCount += row.Count
		if row.Status != "已完成" {
			result.InFlightProjects += row.Count
		}
	}
	var riskCount int64
	if err := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project").Where("health = ?", "风险").Count(&riskCount).Error; err != nil {
		return result, err
	}
	result.RiskProjects = int(riskCount)
	var count int64
	if err := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).Count(&count).Error; err != nil {
		return result, err
	}
	result.ServiceItems = int(count)
	return result, nil
}

func unique(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}
func projectFromRecord(r projectRecord) domain.Project {
	return domain.Project{TenantID: r.TenantID, OwnerOrgID: r.OwnerOrgID, ID: r.ID, Name: r.Name, Customer: r.Customer, Contract: r.Contract, ContractVersion: r.ContractVersion, SupplementStatus: r.SupplementStatus, Services: r.Services, Category: r.Category, Team: r.Team, Manager: r.Manager, OwnerIdentityID: r.OwnerIdentityID, ManagerIdentityID: r.ManagerIdentityID, Health: r.Health, Status: r.Status, Progress: r.Progress, Due: r.Due, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func applyProjectScope(query *gorm.DB, filter platform.ScopeFilter, alias string) *gorm.DB {
	query = query.Where(alias+".tenant_id = ?", filter.TenantID)
	if filter.AllowAll {
		return query
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 8)
	if len(filter.OrganizationIDs) > 0 {
		conditions = append(conditions, alias+".owner_org_id IN ?")
		args = append(args, filter.OrganizationIDs)
	}
	if len(filter.ProjectIDs) > 0 {
		conditions = append(conditions, alias+".id IN ?")
		args = append(args, filter.ProjectIDs)
	}
	if filter.AllowSelf {
		conditions = append(conditions, "("+alias+".owner_identity_id = ? OR "+alias+".manager_identity_id = ? OR EXISTS (SELECT 1 FROM pm_service_item scope_item WHERE scope_item.tenant_id = "+alias+".tenant_id AND scope_item.project_id = "+alias+".id AND (scope_item.team_lead_id = ? OR scope_item.project_manager_id = ? OR JSON_CONTAINS(scope_item.engineer_ids, JSON_QUOTE(?)))))")
		args = append(args, filter.IdentityID, filter.IdentityID, filter.IdentityID, filter.IdentityID, filter.IdentityID)
	}
	if len(conditions) == 0 {
		return query.Where("1 = 0")
	}
	return query.Where("("+strings.Join(conditions, " OR ")+")", args...)
}

func applyServiceItemScope(query, subqueryDB *gorm.DB, filter platform.ScopeFilter) *gorm.DB {
	projects := applyProjectScope(subqueryDB.Table("pm_project AS scope_project").Select("scope_project.id"), filter, "scope_project")
	return query.Where("pm_service_item.tenant_id = ? AND pm_service_item.project_id IN (?)", filter.TenantID, projects)
}
func serviceFromRecord(r serviceItemRecord) domain.ServiceItem {
	item := domain.ServiceItem{TenantID: r.TenantID, ID: r.ID, ProjectID: r.ProjectID, SourceServiceID: r.SourceServiceID, Batch: r.Batch, Site: r.Site, Category: r.Category, Requirement: r.Requirement, System: r.System, Special: r.Special, TestMode: r.TestMode, TeamLeadID: r.TeamLeadID, ProjectManagerID: r.ProjectManagerID, ConflictStatus: r.ConflictStatus, Status: r.Status}
	_ = json.Unmarshal(r.EngineerIDs, &item.EngineerIDs)
	_ = json.Unmarshal(r.EquipmentIDs, &item.EquipmentIDs)
	_ = json.Unmarshal(r.RequiredCodes, &item.RequiredCodes)
	if r.PlannedStart != nil {
		item.PlannedStart = r.PlannedStart.Format(time.RFC3339)
	}
	if r.PlannedEnd != nil {
		item.PlannedEnd = r.PlannedEnd.Format(time.RFC3339)
	}
	return item
}
func ruleFromRecord(r ruleRecord) domain.Rule {
	return domain.Rule{TenantID: r.TenantID, ID: r.ID, Kind: r.Kind, Name: r.Name, Scope: r.Scope, Trigger: r.Trigger, Enabled: r.Enabled, Updated: r.UpdatedAt.Format("2006-01-02 15:04")}
}
func firstValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
