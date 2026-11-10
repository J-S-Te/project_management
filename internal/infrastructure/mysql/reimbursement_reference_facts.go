package mysql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// expenseTravelQuery 限定真实租户项目及事件服务项归属，不依赖展示事件窗口。
// 参数：ctx控制查询，tenant/project为已验证费用引用所属边界。
// 返回值：只读取同项目真实服务项事件的查询，不授予浏览器交付权限。
func (r *Repository) expenseTravelQuery(ctx context.Context, tenant, project string) *gorm.DB {
	return r.db.WithContext(ctx).Table("pm_delivery_event AS expense_event").Select("expense_event.*").
		Joins("JOIN pm_service_item AS source_item ON source_item.id=expense_event.service_item_id AND source_item.tenant_id=expense_event.tenant_id AND source_item.project_id=expense_event.project_id").
		Where("expense_event.tenant_id=? AND expense_event.project_id=?", tenant, project)
}

// readExpenseTravelEvent 映射精确读取的不可变事件，恢复真实租户并拒绝损坏载荷。
// 参数：query为已限定边界及精确标识或顺序的单行查询。
// 返回值：原始事件；缺失返回nil，损坏或数据库失败返回错误。
func readExpenseTravelEvent(query *gorm.DB) (*domain.DeliveryEvent, error) {
	var row deliveryEventRecord
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	payload := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(row.Payload))
	decoder.UseNumber()
	if len(row.Payload) > 1<<20 || decoder.Decode(&payload) != nil {
		return nil, application.ErrServiceTimeout
	}
	return &domain.DeliveryEvent{ID: row.ID, TenantID: row.TenantID, ProjectID: row.ProjectID, ServiceItemID: row.ServiceItemID, Type: row.EventType, ActorUserID: row.ActorUserID, Payload: payload, CreatedAt: row.CreatedAt}, nil
}

// ReadExpensePreparation 精确读取指定服务项最近的准备或撤销事实，避免把旧准备当当前行程。
// 参数：ctx控制读取，tenant/project/item为已验证的费用引用边界。
// 返回值：最新事实或nil；不以排期或任意其他服务项替代缺失事实。
func (r *Repository) ReadExpensePreparation(ctx context.Context, tenant, project, item string) (*domain.DeliveryEvent, error) {
	if tenant == "" || project == "" || item == "" {
		return nil, application.ErrValidation
	}
	query := r.expenseTravelQuery(ctx, tenant, project).Where("expense_event.service_item_id=? AND source_item.archived_at IS NULL", item).
		Where("expense_event.event_type IN ?", []string{application.EventPreparationStarted, application.EventPreparationRevoked}).Order("expense_event.created_at DESC,expense_event.id DESC")
	return readExpenseTravelEvent(query)
}

// ReadExpenseTravelEvent 仅按显式来源ID读取同租户同项目的合法准备事件。
// 参数：ctx为读取上下文，tenant/project为可信边界，eventID为不可变来源。
// 返回值：精确来源或nil；不会回退任意第一条行程，不限制为当前服务项以保留已有合法引用链。
func (r *Repository) ReadExpenseTravelEvent(ctx context.Context, tenant, project, eventID string) (*domain.DeliveryEvent, error) {
	if tenant == "" || project == "" || eventID == "" || len(eventID) > 128 {
		return nil, application.ErrValidation
	}
	return readExpenseTravelEvent(r.expenseTravelQuery(ctx, tenant, project).Where("expense_event.id=? AND expense_event.event_type=?", eventID, application.EventPreparationStarted))
}
