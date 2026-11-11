package application

import (
	"context"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
	"time"
)

type ReportDashboardRepository interface {
	ReadReportDashboard(context.Context, string) (domain.ReportDashboard, error)
}

// ReportDashboard 读取可信机器主体的租户报告聚合。
// 参数：ctx为取消上下文，p为机器鉴权产生的应用全量读取主体。
// 返回值：报告快照；无全量权限、未配置仓储或源查询失败时返回错误。
func (s *Service) ReportDashboard(ctx context.Context, p platform.Principal) (domain.ReportDashboard, error) {
	filter, err := p.ProjectScopeFilter()
	if !p.Has("project.read") || err != nil || !filter.AllowAll || p.TenantID == "" || strings.TrimSpace(p.TenantID) != p.TenantID {
		return domain.ReportDashboard{}, ErrForbidden
	}
	if s == nil {
		return domain.ReportDashboard{}, ErrProjectSnapshotUnavailable
	}
	repo, ok := s.Repo.(ReportDashboardRepository)
	if !ok {
		return domain.ReportDashboard{}, ErrProjectSnapshotUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return repo.ReadReportDashboard(ctx, p.TenantID)
}
