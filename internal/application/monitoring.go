package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

const defaultMonitoringPageSize = 20

// MonitorProjects returns one authorization-consistent monitoring snapshot. Projects, service
// items, SLA decisions and events all pass through their existing scoped and field-masked read
// paths; the browser never rebuilds the tenant/role boundary from a general project list.
func (s *Service) MonitorProjects(ctx context.Context, p platform.Principal, query domain.ProjectMonitoringQuery) (domain.ProjectMonitoringSnapshot, error) {
	if _, err := authorizeProjectScope(p, "project.read"); err != nil {
		return domain.ProjectMonitoringSnapshot{}, err
	}
	for _, value := range []string{query.DueFrom, query.DueTo} {
		if value != "" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return domain.ProjectMonitoringSnapshot{}, ValidationError("计划完成日期必须为 YYYY-MM-DD")
			}
		}
	}
	if query.DueFrom != "" && query.DueTo != "" && query.DueFrom > query.DueTo {
		return domain.ProjectMonitoringSnapshot{}, ValidationError("计划完成起始日期不能晚于结束日期")
	}
	projects, err := s.ListProjects(ctx, p, query.Keyword, "")
	if err != nil {
		return domain.ProjectMonitoringSnapshot{}, err
	}
	items, err := s.ListServiceItems(ctx, p, "")
	if err != nil {
		return domain.ProjectMonitoringSnapshot{}, err
	}
	slaItems, err := s.ListSlaOverdue(ctx, p)
	if err != nil {
		return domain.ProjectMonitoringSnapshot{}, err
	}
	events, err := s.ListDeliveryEvents(ctx, p, "")
	if err != nil {
		return domain.ProjectMonitoringSnapshot{}, err
	}

	itemsByProject := map[string][]domain.ServiceItem{}
	for _, item := range items {
		itemsByProject[item.ProjectID] = append(itemsByProject[item.ProjectID], item)
	}
	slaByProject := map[string][]domain.SlaOverdueItem{}
	for _, item := range slaItems {
		slaByProject[item.ProjectID] = append(slaByProject[item.ProjectID], item)
	}
	latestEventByProject := map[string]domain.DeliveryEvent{}
	for _, event := range events { // repository order is newest first
		if _, exists := latestEventByProject[event.ProjectID]; !exists {
			latestEventByProject[event.ProjectID] = event
		}
	}

	rows := make([]domain.MonitoringProject, 0, len(projects))
	statusCounts := map[string]int{}
	categoryFacets, teamFacets, managerFacets := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	latestVersionTime := time.Time{}
	for _, project := range projects {
		if project.Status == domain.ProjectStatusCompleted {
			continue
		}
		row := buildMonitoringProject(project, itemsByProject[project.ID], slaByProject[project.ID], latestEventByProject)
		if !monitoringMatches(row, query) {
			continue
		}
		rows = append(rows, row)
		statusCounts[project.Status]++
		if project.Team != "" {
			teamFacets[project.Team] = struct{}{}
		}
		for _, category := range row.Categories {
			categoryFacets[category] = struct{}{}
		}
		for _, managerID := range row.ProjectManagerIDs {
			managerFacets[managerID] = struct{}{}
		}
		if row.UpdatedAt.After(latestVersionTime) {
			latestVersionTime = row.UpdatedAt
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].UpdatedAt.After(rows[j].UpdatedAt) })

	page, pageSize := query.Page, query.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = defaultMonitoringPageSize
	}
	total := len(rows)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := min(start+pageSize, total)
	paged := rows[start:end]
	if paged == nil {
		paged = []domain.MonitoringProject{}
	}
	recentEvents := make([]domain.DeliveryEvent, 0, min(20, len(events)))
	visibleProjects := map[string]struct{}{}
	for _, row := range rows {
		visibleProjects[row.Project.ID] = struct{}{}
	}
	for _, event := range events {
		if _, visible := visibleProjects[event.ProjectID]; !visible {
			continue
		}
		recentEvents = append(recentEvents, event)
		if len(recentEvents) == 20 {
			break
		}
	}
	now := time.Now().UTC()
	return domain.ProjectMonitoringSnapshot{
		Items: paged, RecentEvents: recentEvents, StatusCounts: statusCounts,
		Categories: sortedKeys(categoryFacets), Teams: sortedKeys(teamFacets), ProjectManagerIDs: sortedKeys(managerFacets),
		Total: total, Page: page, PageSize: pageSize, ServerTime: now,
		SnapshotVersion: fmt.Sprintf("%d-%d", latestVersionTime.UnixNano(), total),
	}, nil
}

func buildMonitoringProject(project domain.Project, items []domain.ServiceItem, slaItems []domain.SlaOverdueItem, latestEvents map[string]domain.DeliveryEvent) domain.MonitoringProject {
	statusCounts := map[string]int{}
	teamLeads, managers, engineers, equipment, categories := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	conflicts, expiredEquipment, releasedEquipment, plannedEnd := 0, 0, 0, ""
	for _, item := range items {
		statusCounts[item.Status]++
		if item.TeamLeadID != "" {
			teamLeads[item.TeamLeadID] = struct{}{}
		}
		if item.ProjectManagerID != "" {
			managers[item.ProjectManagerID] = struct{}{}
		}
		if item.Category != "" {
			categories[item.Category] = struct{}{}
		}
		for _, id := range item.EngineerIDs {
			if id != "" {
				engineers[id] = struct{}{}
			}
		}
		for _, id := range item.EquipmentIDs {
			if id != "" {
				equipment[id] = struct{}{}
			}
		}
		if item.ImplementationPlan != nil {
			for _, resource := range item.ImplementationPlan.Equipment {
				if resource.ResourceID != "" {
					equipment[resource.ResourceID] = struct{}{}
				}
				if resource.ReturnedAt != "" {
					releasedEquipment++
				}
				if resource.ValidUntil != "" && resource.ValidUntil[:min(10, len(resource.ValidUntil))] < time.Now().UTC().Format("2006-01-02") {
					expiredEquipment++
				}
			}
		}
		if strings.EqualFold(item.ConflictStatus, "CONFLICT") {
			conflicts++
		}
		if item.PlannedEnd != "" && (plannedEnd == "" || item.PlannedEnd > plannedEnd) {
			plannedEnd = item.PlannedEnd
		}
	}
	updatedAt := project.UpdatedAt
	var latestEvent *domain.DeliveryEvent
	if event, ok := latestEvents[project.ID]; ok {
		eventCopy := event
		latestEvent = &eventCopy
		if event.CreatedAt.After(updatedAt) {
			updatedAt = event.CreatedAt
		}
	}
	categoryValues, managerValues := sortedKeys(categories), sortedKeys(managers)
	project.Services = len(items)
	return domain.MonitoringProject{
		Project: project, ServiceStatusCounts: statusCounts, Milestone: monitoringMilestone(project.Status),
		Categories: categoryValues, ProjectManagerIDs: managerValues,
		Resources: domain.MonitoringResources{TeamLeads: len(teamLeads), ProjectManagers: len(managers), Engineers: len(engineers), Equipment: len(equipment), ExpiredEquipment: expiredEquipment, ReleasedEquipment: releasedEquipment, ConflictItems: conflicts},
		SLAItems:  slaItems, LatestEvent: latestEvent, PlannedEnd: plannedEnd, UpdatedAt: updatedAt,
		ActionSection: monitoringActionSection(project.Status),
	}
}

func monitoringMilestone(status string) domain.MonitoringMilestone {
	stages := []string{"拆解确认", "资源分配", "实施计划", "实施准备", "现场实施", "报告编制", "交付归档"}
	rank := map[string]int{"待拆解确认": 0, "待分配": 1, "待实施": 2, "实施准备中": 3, "实施中": 4, "现场实施完成": 5, "报告编制": 5, "已完成": 6}
	index := rank[status]
	current := stages[index]
	next := ""
	if index+1 < len(stages) {
		next = stages[index+1]
	}
	if status == "异常处理中" {
		current, next = "异常处理", "恢复原流程"
	}
	if status == "已终止" {
		current, next = "项目终止", ""
	}
	return domain.MonitoringMilestone{Current: current, Next: next, Done: min(index, 6), Total: 7}
}

func monitoringActionSection(status string) string {
	return map[string]string{
		"待拆解确认": "decomposition", "待分配": "allocation", "待实施": "planning",
		"实施准备中": "preparation", "实施中": "implementation", "异常处理中": "exceptions",
		"现场实施完成": "reports", "报告编制": "reports",
	}[status]
}

func monitoringMatches(row domain.MonitoringProject, query domain.ProjectMonitoringQuery) bool {
	p := row.Project
	if query.Status != "" && p.Status != query.Status {
		return false
	}
	if query.Customer != "" && !strings.Contains(strings.ToLower(p.Customer), strings.ToLower(strings.TrimSpace(query.Customer))) {
		return false
	}
	if query.Team != "" && p.Team != query.Team {
		return false
	}
	if query.RiskOnly && !p.Risk && len(row.SLAItems) == 0 && row.Resources.ConflictItems == 0 {
		return false
	}
	if query.SLAOnly && len(row.SLAItems) == 0 {
		return false
	}
	if query.ConflictOnly && row.Resources.ConflictItems == 0 {
		return false
	}
	if query.Category != "" && !slicesContains(row.Categories, query.Category) {
		return false
	}
	if query.ProjectManagerID != "" && !slicesContains(row.ProjectManagerIDs, query.ProjectManagerID) {
		return false
	}
	if query.DueFrom != "" && (row.PlannedEnd == "" || row.PlannedEnd[:min(10, len(row.PlannedEnd))] < query.DueFrom) {
		return false
	}
	if query.DueTo != "" && (row.PlannedEnd == "" || row.PlannedEnd[:min(10, len(row.PlannedEnd))] > query.DueTo) {
		return false
	}
	return true
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func slicesContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
