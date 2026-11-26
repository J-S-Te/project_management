package mysql

import (
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// withPreparationProjection 为本次授权查询的准备期服务项附加最近一次节点标记。
// 参数：query 为已套用数据范围、以 pm_service_item 为基表的查询。
// 返回值：读取节点与状态同快照的查询；无相关事件或不在准备阶段时投影为 NULL。
func withPreparationProjection(query *gorm.DB) *gorm.DB {
	// 事件查询命中现有租户+服务项+时间索引。相同时间戳用事件 ID 固定先后顺序。
	return query.Select(`pm_service_item.*,
		CASE WHEN pm_service_item.status = ? THEN (
			SELECT history.event_type = ?
			FROM pm_delivery_event AS history
			WHERE history.tenant_id = pm_service_item.tenant_id
			  AND history.service_item_id = pm_service_item.id
			  AND (history.event_type IN (?, ?) OR
			     (history.event_type = ? AND history.payload ->> '$.kind' = ?))
			ORDER BY history.created_at DESC, history.id DESC
			LIMIT 1
		) END AS preparation_confirmed`,
		domain.ProjectStatusPreparing, application.EventPreparationStarted,
		application.EventPreparationStarted, application.EventFieldReturnedToPreparation,
		application.EventRollbackApproved, "FIELD_TO_PREPARATION")
}
