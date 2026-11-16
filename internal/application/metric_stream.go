package application

import (
	"context"
	"github.com/j-s-te/project-management/internal/platform"
	"io"
	"time"
)

type MetricStreamRepository interface {
	StreamMetricFacts(context.Context, string, io.Writer) error
}

// StreamMetricFacts 在原全租户机器授权下读取完整有界事实流。
// 参数：ctx/p 为真实请求与主体，output 为协议输出。
// 返回值：授权、仓储或读取失败返回错误。
func (s *Service) StreamMetricFacts(ctx context.Context, p platform.Principal, output io.Writer) error {
	filter, err := p.ProjectScopeFilter()
	if err != nil || !filter.AllowAll || !p.Has("project.read") || p.TenantID == "" {
		return ErrForbidden
	}
	repo, ok := s.Repo.(MetricStreamRepository)
	if !ok {
		return ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return repo.StreamMetricFacts(ctx, p.TenantID, output)
}
