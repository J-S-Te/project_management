package application

import (
	"context"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// MetricItemResourcesRepository 保留父项与既有资源范围的节点台账读取边界。
type MetricItemResourcesRepository interface {
	ListItemMetricCapacities(context.Context, platform.ScopeFilter, domain.ServiceItem, int) ([]domain.ResourceCapacity, error)
	ListItemMetricUsages(context.Context, platform.ScopeFilter, string, int) ([]domain.ResourceUsage, error)
}

// MetricItemResources 用途：读取当前服务项相关容量或用量，不扩大原有资源可见范围。
// 参数：ctx/p 为可信请求，itemID 为父服务项，kind 为台账类型，page 为从1开始页码。
// 返回值：最多50条事实；无权、父不可见、非法参数或未接线时返回错误。
func (s *Service) MetricItemResources(ctx context.Context, p platform.Principal, itemID, kind string, page int) (any, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	if itemID == "" || len(itemID) > 64 || page < 1 || page > 10000 ||
		(kind != "capacities" && kind != "usages") {
		return nil, ErrValidation
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return nil, err
	}
	repo, ok := s.Repo.(MetricItemResourcesRepository)
	if !ok {
		return nil, ErrProjectSnapshotUnavailable
	}
	if kind == "capacities" {
		return repo.ListItemMetricCapacities(ctx, filter, item, page)
	}
	return repo.ListItemMetricUsages(ctx, filter, itemID, page)
}
