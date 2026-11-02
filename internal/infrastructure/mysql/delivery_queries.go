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
)

func (r *Repository) ListSlaOverdue(ctx context.Context, filter platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	var rows []struct {
		ID              string
		ProjectID       string
		Site            string
		Category        string
		Status          string
		PlannedEnd      *time.Time
		StatusChangedAt *time.Time
		OverdueHours    int64
	}
	query := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).
		Select("id, project_id, site, category, status, planned_end, status_changed_at, COALESCE(TIMESTAMPDIFF(HOUR, planned_end, UTC_TIMESTAMP()), 0) AS overdue_hours").
		// 候选集必须同时覆盖两类口径：①计划完成时间已过（固定口径）②按规则的状态停留超时/临近。
		// 后者恰恰包含「计划时间未到但已临近超时」的项，因此不能在此按 planned_end 过滤，
		// 否则提前提醒永远不会触发；计划完成口径的状态过滤下沉到 computeSlaItems。
		// 唯一在此排除的是终态「已终止」：它没有后续动作，不属于任何 SLA 口径。
		Where("status <> ?", domain.ProjectStatusTerminated)
	if err := query.Order("planned_end").Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domain.SlaOverdueItem, 0, len(rows))
	for _, row := range rows {
		plannedEnd := ""
		if row.PlannedEnd != nil {
			plannedEnd = row.PlannedEnd.Format(time.RFC3339)
		}
		item := domain.SlaOverdueItem{ID: row.ID, ProjectID: row.ProjectID, Site: row.Site, Category: row.Category, Status: row.Status, PlannedEnd: plannedEnd, OverdueHours: row.OverdueHours}
		if row.StatusChangedAt != nil {
			item.StatusChangedAt = *row.StatusChangedAt
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *Repository) ListDeliveryEvents(ctx context.Context, filter platform.ScopeFilter, projectID string) ([]domain.DeliveryEvent, error) {
	projects := applyProjectScope(r.db.WithContext(ctx).Table("pm_project AS scope_project").Select("scope_project.id"), filter, "scope_project")
	query := r.db.WithContext(ctx).Where("tenant_id = ? AND project_id IN (?)", filter.TenantID, projects)
	if filter.AssignedItemsOnly {
		items := applyServiceItemScope(r.db.WithContext(ctx).Table("pm_service_item").Select("pm_service_item.id"), r.db.WithContext(ctx), filter)
		query = query.Where("service_item_id IN (?)", items)
	}
	if projectID != "" {
		query = query.Where("project_id=?", projectID)
	}
	var records []deliveryEventRecord
	if err := query.Order("created_at DESC").Limit(500).Find(&records).Error; err != nil {
		return nil, err
	}
	out := make([]domain.DeliveryEvent, 0, len(records))
	for _, v := range records {
		payload := map[string]any{}
		_ = json.Unmarshal(v.Payload, &payload)
		event := domain.DeliveryEvent{ID: v.ID, ProjectID: v.ProjectID, ServiceItemID: v.ServiceItemID, Type: v.EventType, ActorUserID: v.ActorUserID, Payload: payload, CreatedAt: v.CreatedAt}
		if v.ClientOperationID != nil {
			event.ClientOperationID = *v.ClientOperationID
		}
		if v.CapturedAt != nil {
			value := *v.CapturedAt
			event.CapturedAt = &value
		}
		out = append(out, event)
	}
	return out, nil
}

// ListServiceItemDeliveryEvents 按 tenant + 数据范围 + service_item_id 精确读取该服务项的
// 完整事件流（id 升序）。BIZ-PROJECT-06：现场完成的缺项判定不能用"租户最近 500 条事件"
// 的窗口近似——服务项的证据一旦被其它项目的新事件挤出窗口，就会被误判成缺项。
//
// 这里刻意不提供分页：判定必须看到全量证据。为防异常数据拖死请求，读取上限为
// application.MaxServiceItemDeliveryEvents + 1 条，超过上限直接报错（fail-closed），
// 绝不静默截断——截断会让判定重新退化成窗口近似。
func (r *Repository) ListServiceItemDeliveryEvents(ctx context.Context, filter platform.ScopeFilter, itemID string) ([]domain.DeliveryEvent, error) {
	if strings.TrimSpace(itemID) == "" {
		return nil, application.ErrValidation
	}
	projects := applyProjectScope(r.db.WithContext(ctx).Table("pm_project AS scope_project").Select("scope_project.id"), filter, "scope_project")
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id IN (?) AND service_item_id = ?", filter.TenantID, projects, itemID).
		Model(&deliveryEventRecord{})
	if filter.AssignedItemsOnly {
		items := applyServiceItemScope(r.db.WithContext(ctx).Table("pm_service_item").Select("pm_service_item.id"), r.db.WithContext(ctx), filter)
		query = query.Where("service_item_id IN (?)", items)
	}
	var records []deliveryEventRecord
	if err := query.Order("id ASC").Limit(application.MaxServiceItemDeliveryEvents + 1).Find(&records).Error; err != nil {
		return nil, err
	}
	if err := assertDeliveryEventWindow(len(records)); err != nil {
		return nil, err
	}
	out := make([]domain.DeliveryEvent, 0, len(records))
	for _, v := range records {
		payload := map[string]any{}
		_ = json.Unmarshal(v.Payload, &payload)
		event := domain.DeliveryEvent{ID: v.ID, ProjectID: v.ProjectID, ServiceItemID: v.ServiceItemID, Type: v.EventType, ActorUserID: v.ActorUserID, Payload: payload, CreatedAt: v.CreatedAt}
		if v.ClientOperationID != nil {
			event.ClientOperationID = *v.ClientOperationID
		}
		if v.CapturedAt != nil {
			value := *v.CapturedAt
			event.CapturedAt = &value
		}
		out = append(out, event)
	}
	return out, nil
}

// assertDeliveryEventWindow 在读取条数超过上限时报错而不是截断。
// 走 id 升序 + 上限+1 的读法：只要越界就说明该服务项事件过多，判定不可信。
func assertDeliveryEventWindow(count int) error {
	if count > application.MaxServiceItemDeliveryEvents {
		return application.ErrFieldEvidenceWindowExceeded
	}
	return nil
}

func (r *Repository) FindProjectForDeviation(ctx context.Context, filter platform.ScopeFilter, deviationID string) (string, string, error) {
	projects := applyProjectScope(r.db.WithContext(ctx).Table("pm_project AS scope_project").Select("scope_project.id"), filter, "scope_project")
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id IN (?) AND event_type = ? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.deviation_id')) = ?", filter.TenantID, projects, application.EventDeviationReported, deviationID).
		Model(&deliveryEventRecord{})
	if filter.AssignedItemsOnly {
		items := applyServiceItemScope(r.db.WithContext(ctx).Table("pm_service_item").Select("pm_service_item.id"), r.db.WithContext(ctx), filter)
		query = query.Where("service_item_id IN (?)", items)
	}
	var record deliveryEventRecord
	err := query.Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", application.ErrNotFound
	}
	return record.ProjectID, record.ServiceItemID, err
}
