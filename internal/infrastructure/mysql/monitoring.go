package mysql

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
)

// LoadProjectMonitoringPage performs filtering, counting, faceting and pagination in MySQL. Detail
// rows are then loaded only for the selected project page, preventing the monitoring endpoint from
// hydrating every project, service item, SLA candidate and event in the tenant on each refresh.
func (r *Repository) LoadProjectMonitoringPage(ctx context.Context, filter platform.ScopeFilter, input domain.ProjectMonitoringQuery) (domain.ProjectMonitoringPageData, error) {
	page, pageSize := input.Page, input.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	base := func() *gorm.DB {
		query := applyProjectScope(r.db.WithContext(ctx).Table("pm_project"), filter, "pm_project").
			Where("pm_project.status <> ?", domain.ProjectStatusCompleted)
		if keyword := strings.TrimSpace(input.Keyword); keyword != "" {
			like := "%" + keyword + "%"
			query = query.Where("(pm_project.id LIKE ? OR pm_project.name LIKE ? OR pm_project.customer LIKE ? OR pm_project.contract LIKE ? OR pm_project.category LIKE ? OR pm_project.manager LIKE ?)", like, like, like, like, like, like)
		}
		if value := strings.TrimSpace(input.Status); value != "" {
			query = query.Where("pm_project.status = ?", value)
		}
		if value := strings.TrimSpace(input.Customer); value != "" {
			query = query.Where("LOWER(pm_project.customer) LIKE ?", "%"+strings.ToLower(value)+"%")
		}
		if value := strings.TrimSpace(input.Team); value != "" {
			query = query.Where("pm_project.team = ?", value)
		}

		visibleItems := func() *gorm.DB {
			return applyServiceItemScope(r.db.WithContext(ctx).Table("pm_service_item"), r.db.WithContext(ctx), filter)
		}
		if value := strings.TrimSpace(input.Category); value != "" {
			query = query.Where("pm_project.id IN (?)", visibleItems().Select("pm_service_item.project_id").Where("pm_service_item.category = ?", value))
		}
		if value := strings.TrimSpace(input.ProjectManagerID); value != "" {
			query = query.Where("pm_project.id IN (?)", visibleItems().Select("pm_service_item.project_id").Where("pm_service_item.project_manager_id = ?", value))
		}
		conflictProjects := visibleItems().Select("pm_service_item.project_id").Where("pm_service_item.conflict_status = ?", "CONFLICT")
		if input.ConflictOnly {
			query = query.Where("pm_project.id IN (?)", conflictProjects)
		}

		dueProjects := visibleItems().Select("pm_service_item.project_id").Where("pm_service_item.planned_end IS NOT NULL").Group("pm_service_item.project_id")
		if input.DueFrom != "" {
			dueProjects = dueProjects.Having("DATE(MAX(pm_service_item.planned_end)) >= ?", input.DueFrom)
		}
		if input.DueTo != "" {
			dueProjects = dueProjects.Having("DATE(MAX(pm_service_item.planned_end)) <= ?", input.DueTo)
		}
		if input.DueFrom != "" || input.DueTo != "" {
			query = query.Where("pm_project.id IN (?)", dueProjects)
		}

		slaPredicate := `pm_service_item.status <> ? AND ((pm_service_item.status <> ? AND pm_service_item.planned_end IS NOT NULL AND pm_service_item.planned_end < UTC_TIMESTAMP()) OR EXISTS (
			SELECT 1 FROM pm_sla monitoring_sla
			WHERE monitoring_sla.tenant_id = pm_service_item.tenant_id AND monitoring_sla.enabled = TRUE
			AND TRIM(monitoring_sla.status) = TRIM(pm_service_item.status) AND pm_service_item.status_changed_at IS NOT NULL
			AND (TIMESTAMPDIFF(HOUR, pm_service_item.status_changed_at, UTC_TIMESTAMP()) > monitoring_sla.deadline_hours
			OR (monitoring_sla.remind_hours > 0 AND monitoring_sla.deadline_hours - TIMESTAMPDIFF(HOUR, pm_service_item.status_changed_at, UTC_TIMESTAMP()) <= monitoring_sla.remind_hours))
		))`
		slaProjects := visibleItems().Select("pm_service_item.project_id").Where(slaPredicate, domain.ProjectStatusTerminated, domain.ProjectStatusFieldCompleted)
		if input.SLAOnly {
			query = query.Where("pm_project.id IN (?)", slaProjects)
		}
		if input.RiskOnly {
			terminatedProjects := r.db.WithContext(ctx).Table("pm_service_item").Select("project_id").
				Where("tenant_id = pm_project.tenant_id AND archived_at IS NULL AND status = ?", domain.ProjectStatusTerminated)
			query = query.Where("(pm_project.status IN ? OR EXISTS (?) OR pm_project.id IN (?) OR pm_project.id IN (?))",
				[]string{domain.ProjectStatusException, domain.ProjectStatusTerminated}, terminatedProjects, conflictProjects, slaProjects)
		}
		return query
	}

	result := domain.ProjectMonitoringPageData{StatusCounts: map[string]int{}, Categories: []string{}, Teams: []string{}, ProjectManagerIDs: []string{}}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return result, err
	}
	result.Total = int(total)
	var records []projectRecord
	if err := base().Select("pm_project.*").Order("pm_project.updated_at DESC, pm_project.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error; err != nil {
		return result, err
	}
	ids := projectIDsOf(records)
	statusInputs, err := r.projectStatusInputs(ctx, filter.TenantID, ids)
	if err != nil {
		return result, err
	}
	for _, record := range records {
		project := projectFromRecord(record)
		applyDerivedProjectMetrics(&project, statusInputs[record.ID])
		result.Projects = append(result.Projects, project)
	}

	var statusRows []struct {
		Status string
		Count  int
	}
	if err := base().Select("pm_project.status AS status, COUNT(*) AS count").Group("pm_project.status").Scan(&statusRows).Error; err != nil {
		return result, err
	}
	for _, row := range statusRows {
		result.StatusCounts[row.Status] = row.Count
	}
	if err := base().Distinct("pm_project.team").Where("pm_project.team <> ''").Order("pm_project.team").Pluck("pm_project.team", &result.Teams).Error; err != nil {
		return result, err
	}
	filteredIDs := base().Select("pm_project.id")
	visibleFacetItems := applyServiceItemScope(r.db.WithContext(ctx).Table("pm_service_item"), r.db.WithContext(ctx), filter).Where("pm_service_item.project_id IN (?)", filteredIDs)
	if err := visibleFacetItems.Session(&gorm.Session{}).Distinct("pm_service_item.category").Where("pm_service_item.category <> ''").Order("pm_service_item.category").Pluck("pm_service_item.category", &result.Categories).Error; err != nil {
		return result, err
	}
	visibleManagerItems := applyServiceItemScope(r.db.WithContext(ctx).Table("pm_service_item"), r.db.WithContext(ctx), filter).Where("pm_service_item.project_id IN (?)", base().Select("pm_project.id"))
	if err := visibleManagerItems.Distinct("pm_service_item.project_manager_id").Where("pm_service_item.project_manager_id <> ''").Order("pm_service_item.project_manager_id").Pluck("pm_service_item.project_manager_id", &result.ProjectManagerIDs).Error; err != nil {
		return result, err
	}
	var latest struct{ UpdatedAt *time.Time }
	if err := base().Select("MAX(pm_project.updated_at) AS updated_at").Scan(&latest).Error; err != nil {
		return result, err
	}
	if latest.UpdatedAt != nil {
		result.LatestUpdatedAt = *latest.UpdatedAt
	}
	if len(ids) == 0 {
		result.Projects = []domain.Project{}
		result.ServiceItems = []domain.ServiceItem{}
		result.SLACandidates = []domain.SlaOverdueItem{}
		result.Events = []domain.DeliveryEvent{}
		return result, nil
	}

	itemQuery := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).Where("pm_service_item.project_id IN ?", ids)
	var itemRecords []serviceItemRecord
	if err := itemQuery.Order("pm_service_item.id").Find(&itemRecords).Error; err != nil {
		return result, err
	}
	planByItem := map[string]domain.ImplementationPlan{}
	if len(itemRecords) > 0 {
		itemIDs := make([]string, 0, len(itemRecords))
		for _, record := range itemRecords {
			itemIDs = append(itemIDs, record.ID)
		}
		var plans []implPlanRecord
		if err := r.db.WithContext(ctx).Where("tenant_id = ? AND service_item_id IN ?", filter.TenantID, itemIDs).Find(&plans).Error; err != nil {
			return result, err
		}
		for _, record := range plans {
			planByItem[record.ServiceItemID] = implPlanFromRecord(record)
		}
	}
	for _, record := range itemRecords {
		item := serviceFromRecord(record)
		if plan, ok := planByItem[item.ID]; ok {
			item.ImplementationPlan = &plan
		}
		result.ServiceItems = append(result.ServiceItems, item)
	}

	var slaRows []struct {
		ID              string
		ProjectID       string
		Site            string
		Category        string
		Status          string
		PlannedEnd      *time.Time
		StatusChangedAt *time.Time
	}
	if err := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).
		Select("pm_service_item.id, pm_service_item.project_id, pm_service_item.site, pm_service_item.category, pm_service_item.status, pm_service_item.planned_end, pm_service_item.status_changed_at").
		Where("pm_service_item.project_id IN ? AND pm_service_item.status <> ?", ids, domain.ProjectStatusTerminated).Scan(&slaRows).Error; err != nil {
		return result, err
	}
	for _, row := range slaRows {
		candidate := domain.SlaOverdueItem{ID: row.ID, ProjectID: row.ProjectID, Site: row.Site, Category: row.Category, Status: row.Status}
		if row.PlannedEnd != nil {
			candidate.PlannedEnd = row.PlannedEnd.Format(time.RFC3339)
		}
		if row.StatusChangedAt != nil {
			candidate.StatusChangedAt = *row.StatusChangedAt
		}
		result.SLACandidates = append(result.SLACandidates, candidate)
	}

	eventQuery := r.db.WithContext(ctx).Where("tenant_id = ? AND project_id IN ?", filter.TenantID, ids)
	if filter.AssignedItemsOnly {
		visibleItemIDs := applyServiceItemScope(r.db.WithContext(ctx).Table("pm_service_item").Select("pm_service_item.id"), r.db.WithContext(ctx), filter)
		eventQuery = eventQuery.Where("service_item_id IN (?)", visibleItemIDs)
	}
	var eventRecords []deliveryEventRecord
	if err := eventQuery.Order("created_at DESC").Limit(100).Find(&eventRecords).Error; err != nil {
		return result, err
	}
	for _, record := range eventRecords {
		payload := map[string]any{}
		_ = json.Unmarshal(record.Payload, &payload)
		result.Events = append(result.Events, domain.DeliveryEvent{ID: record.ID, ProjectID: record.ProjectID, ServiceItemID: record.ServiceItemID, Type: record.EventType, ActorUserID: record.ActorUserID, Payload: payload, CreatedAt: record.CreatedAt})
	}
	return result, nil
}
