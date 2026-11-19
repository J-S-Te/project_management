package application

import (
	"context"
	"strings"

	"github.com/j-s-te/project-management/internal/platform"
)

type workflowVersionRepository interface {
	WorkflowVersion(context.Context, platform.ScopeFilter, string) (string, error)
}

// WorkflowVersion 查询当前主体能读取的项目流程快照版本。
// 参数：ctx 为读取上下文，p 为认证主体，projectID 为空表示当前可见范围。
// 返回值：不透明快照版本，权限、校验或存储失败时返回错误。
func (s *Service) WorkflowVersion(ctx context.Context, p platform.Principal, projectID string) (string, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return "", err
	}
	if len(projectID) > 32 || strings.TrimSpace(projectID) != projectID {
		return "", ErrValidation
	}
	repo, ok := s.Repo.(workflowVersionRepository)
	if !ok {
		return "", ErrServiceTimeout
	}
	return repo.WorkflowVersion(ctx, filter, projectID)
}
