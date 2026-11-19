package mysql

import (
	"context"
	"encoding/json"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
)

// approvalTaskQuery 以授权服务项和原申请归属构造待办查询，避免事件快照截断。
// 参数：ctx/filter 为可信请求范围，q 为用户身份、权限和分页条件。
// 返回值：同时限制租户、项目范围、审批归属和终态的数据库查询。
func (r *Repository) approvalTaskQuery(ctx context.Context, filter platform.ScopeFilter, q domain.ApprovalQuery) *gorm.DB {
	db := r.db.WithContext(ctx)
	query := applyServiceItemScope(db.Table("pm_service_item"), db, filter).
		Joins(`JOIN pm_delivery_event AS req ON req.tenant_id=pm_service_item.tenant_id
 AND req.service_item_id=pm_service_item.id`).
		Joins(`LEFT JOIN pm_delivery_event AS decided ON decided.id=(
 SELECT closed.id FROM pm_delivery_event AS closed
 WHERE closed.tenant_id=req.tenant_id AND closed.service_item_id=req.service_item_id
 AND JSON_UNQUOTE(JSON_EXTRACT(closed.payload,'$.request_id'))=req.id
 AND ((req.event_type='ROLLBACK_REQUESTED' AND closed.event_type IN
 ('ROLLBACK_APPROVED','ROLLBACK_REJECTED','ROLLBACK_WITHDRAWN'))
 OR (req.event_type='REPORT_CORRECTION_REQUESTED' AND closed.event_type IN
 ('REPORT_CORRECTION_APPROVED','REPORT_CORRECTION_REJECTED')))
 ORDER BY closed.created_at DESC,closed.id DESC LIMIT 1)`).
		Where("req.event_type IN ? AND pm_service_item.archived_at IS NULL",
			[]string{application.EventRollbackRequested, application.EventReportCorrectionRequested})
	assigned := `(req.actor_user_id<>? AND
 ((req.event_type='REPORT_CORRECTION_REQUESTED' AND ?)
 OR (req.event_type='ROLLBACK_REQUESTED' AND ?)))`
	args := []any{q.UserID, q.CanCorrection, q.CanRollback}
	if q.RequestID != "" {
		query = query.Where("req.id=?", q.RequestID)
		return query.Where("(req.actor_user_id=? OR "+assigned+")", append([]any{q.UserID}, args...)...)
	}
	query = query.Where("decided.id IS NULL")
	if q.View == "submitted" {
		return query.Where("req.actor_user_id=?", q.UserID)
	}
	return query.Where(assigned, args...)
}

// ListApprovalTasks 分页读取当前账号的审批待办或可访问的精确申请详情。
// 参数：ctx/filter 为可信数据范围，q 为已校验的查询和权限能力。
// 返回值：原申请、当前服务项版本及终决信息；查询失败返回错误。
func (r *Repository) ListApprovalTasks(ctx context.Context, filter platform.ScopeFilter, q domain.ApprovalQuery) ([]domain.ApprovalTask, int64, error) {
	rows := []domain.ApprovalTask{}
	query := r.approvalWorkItems(ctx, filter, q)
	if query == nil {
		return rows, 0, nil
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at DESC, id DESC, type ASC").
		Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&rows).Error
	return rows, total, err
}

// GetApprovalRequest 按原申请身份直接回源，避免最近五百条事件截断审批依据。
// 参数：ctx/filter 为可信范围，itemID/requestID/type 为服务端限定的申请身份。
// 返回值：原始申请；无权、不存在或类型不符时返回不存在。
func (r *Repository) GetApprovalRequest(ctx context.Context, filter platform.ScopeFilter, itemID, requestID, eventType string) (domain.DeliveryEvent, error) {
	db := r.db.WithContext(ctx)
	items := applyServiceItemScope(db.Table("pm_service_item").Select("pm_service_item.id"), db, filter)
	var row deliveryEventRecord
	err := db.Where("tenant_id=? AND id=? AND service_item_id=? AND event_type=? AND service_item_id IN (?)",
		filter.TenantID, requestID, itemID, eventType, items).Take(&row).Error
	if err != nil {
		return domain.DeliveryEvent{}, mapNotFound(err)
	}
	payload := map[string]any{}
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return domain.DeliveryEvent{}, application.ErrConflict
	}
	return domain.DeliveryEvent{ID: row.ID, TenantID: row.TenantID, ProjectID: row.ProjectID,
		ServiceItemID: row.ServiceItemID, ActorUserID: row.ActorUserID, Type: row.EventType,
		CreatedAt: row.CreatedAt, Payload: payload}, nil
}
