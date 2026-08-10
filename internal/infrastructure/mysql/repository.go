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
)

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) ListProjects(ctx context.Context, tenant, q, status string) ([]domain.Project, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenant)
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
func (r *Repository) GetProject(ctx context.Context, tenant, id string) (domain.Project, error) {
	var record projectRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenant, id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Project{}, application.ErrNotFound
	}
	return projectFromRecord(record), err
}
func (r *Repository) CreateProject(ctx context.Context, item domain.Project) error {
	return r.db.WithContext(ctx).Create(&projectRecord{ID: item.ID, TenantID: item.TenantID, Name: item.Name, Customer: item.Customer, Contract: item.Contract, ContractVersion: item.ContractVersion, SupplementStatus: firstValue(item.SupplementStatus, "NONE"), Services: item.Services, Category: item.Category, Team: item.Team, Manager: item.Manager, Health: item.Health, Status: item.Status, Progress: item.Progress, Due: item.Due, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}).Error
}
func (r *Repository) ListServiceItems(ctx context.Context, tenant, projectID string) ([]domain.ServiceItem, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenant)
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
func (r *Repository) ConfirmServiceItems(ctx context.Context, tenant string, ids []string, actor string) ([]domain.ServiceItem, error) {
	var result []domain.ServiceItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var records []serviceItemRecord
		if err := tx.Where("tenant_id = ? AND id IN ?", tenant, ids).Find(&records).Error; err != nil {
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
		if err := tx.Model(&serviceItemRecord{}).Where("tenant_id = ? AND id IN ?", tenant, ids).Updates(map[string]any{"status": "待分配", "updated_at": now, "updated_by": actor}).Error; err != nil {
			return err
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
func (r *Repository) Dashboard(ctx context.Context, tenant string) (domain.Dashboard, error) {
	result := domain.Dashboard{StatusCounts: map[string]int{}}
	var records []struct {
		Status string
		Count  int
	}
	if err := r.db.WithContext(ctx).Model(&projectRecord{}).Select("status, COUNT(*) AS count").Where("tenant_id = ?", tenant).Group("status").Scan(&records).Error; err != nil {
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
	if err := r.db.WithContext(ctx).Model(&projectRecord{}).Where("tenant_id = ? AND health = ?", tenant, "风险").Count(&riskCount).Error; err != nil {
		return result, err
	}
	result.RiskProjects = int(riskCount)
	var count int64
	if err := r.db.WithContext(ctx).Model(&serviceItemRecord{}).Where("tenant_id = ?", tenant).Count(&count).Error; err != nil {
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
	return domain.Project{TenantID: r.TenantID, ID: r.ID, Name: r.Name, Customer: r.Customer, Contract: r.Contract, ContractVersion: r.ContractVersion, SupplementStatus: r.SupplementStatus, Services: r.Services, Category: r.Category, Team: r.Team, Manager: r.Manager, Health: r.Health, Status: r.Status, Progress: r.Progress, Due: r.Due, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
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
