package application

import (
	"context"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type metricCandidateReplayRepository interface {
	ReplayMetricUsageCandidate(context.Context, platform.ScopeFilter, string, domain.ServiceItem, domain.MetricUsageCandidateApplyInput) (domain.ResourceUsage, bool, error)
}

// replayMetricUsageCandidate 用途：先复核当前权限和父项，再核对原候选命令的不可变回执。
// 参数：ctx/p 为可信请求，itemID 为父项，in 为资源、业务日及原操作号。
// 返回值：原回执及是否找到；权限、父项、操作者或原命令不匹配时拒绝。
func (s *Service) replayMetricUsageCandidate(ctx context.Context, p platform.Principal, itemID string, in domain.MetricUsageCandidateApplyInput) (domain.ResourceUsage, bool, error) {
	if _, err := authorizeProjectScope(p, "project.read"); err != nil {
		return domain.ResourceUsage{}, false, err
	}
	filter, err := authorizeProjectScope(p, "project.resource_usage.record")
	if err != nil {
		return domain.ResourceUsage{}, false, err
	}
	if !p.Has("project.implementation.plan") && !p.Has("project.field.execute") && !p.Has("project.person_capacity.manage") {
		return domain.ResourceUsage{}, false, ErrForbidden
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return domain.ResourceUsage{}, false, err
	}
	repo, ok := s.Repo.(metricCandidateReplayRepository)
	if !ok {
		return domain.ResourceUsage{}, false, ErrProjectSnapshotUnavailable
	}
	return repo.ReplayMetricUsageCandidate(ctx, filter, p.UserID, item, in)
}
