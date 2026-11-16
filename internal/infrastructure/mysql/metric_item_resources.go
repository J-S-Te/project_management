package mysql

import (
	"context"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// ListItemMetricCapacities 用途：按当前计划、准备或已登记资源读取同一份容量，不按项目复制。
// 参数：ctx/filter 为可信范围，item 为已授权父项及计划，page 为页码。
// 返回值：最多50个容量版本；既有组织和本人范围继续生效。
func (r *Repository) ListItemMetricCapacities(ctx context.Context, filter platform.ScopeFilter, item domain.ServiceItem, page int) ([]domain.ResourceCapacity, error) {
	people, equipment := []string{}, []string{}
	if item.ImplementationPlan != nil {
		for _, row := range item.ImplementationPlan.Personnel {
			people = append(people, row.ResourceID)
		}
		for _, row := range item.ImplementationPlan.Equipment {
			equipment = append(equipment, row.ResourceID)
		}
	}
	query := r.db.WithContext(ctx).Model(&metricCapacityRecord{}).Where("tenant_id=?", filter.TenantID)
	query = metricResourceReadScope(query, filter, false)
	readableUsage := r.db.WithContext(ctx).Model(&metricUsageRecord{}).
		Select("resource_type,resource_id").Where("tenant_id=? AND service_item_id=?", filter.TenantID, item.ID)
	readableUsage = metricResourceReadScope(readableUsage, filter, true)
	query = query.Where(`((resource_type='PERSON' AND resource_id IN ?) OR
		(resource_type='EQUIPMENT' AND resource_id IN ?) OR
		(resource_type,resource_id) IN (?))`, people, equipment, readableUsage)
	var rows []metricCapacityRecord
	err := query.Order("business_date DESC,created_at DESC,id DESC").Offset((page - 1) * 50).Limit(50).Find(&rows).Error
	out := make([]domain.ResourceCapacity, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ResourceCapacity)
	}
	return out, err
}

// ListItemMetricUsages 用途：在原有资源读取范围内精确读取一个服务项的实际台账。
// 参数：ctx/filter 为可信范围，itemID 为已授权父项，page 为页码。
// 返回值：最多50条用量；不会混入其他项目或扩大成员私有明细。
func (r *Repository) ListItemMetricUsages(ctx context.Context, filter platform.ScopeFilter, itemID string, page int) ([]domain.ResourceUsage, error) {
	query := r.db.WithContext(ctx).Model(&metricUsageRecord{}).
		Where("tenant_id=? AND service_item_id=?", filter.TenantID, itemID)
	query = metricResourceReadScope(query, filter, true)
	var rows []metricUsageRecord
	err := query.Order("business_date DESC,created_at DESC,id DESC").Offset((page - 1) * 50).Limit(50).Find(&rows).Error
	out := make([]domain.ResourceUsage, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ResourceUsage)
	}
	return out, err
}
