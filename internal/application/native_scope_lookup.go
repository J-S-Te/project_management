package application

import (
	"context"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type nativeScopeChangeReader interface {
	ReadNativeScopeChange(context.Context, platform.ScopeFilter, string, string) (domain.NativeScopeState, error)
}

// ReadNativeScopeChange 读取原生项目的指定提案并附上当前账号的既有业务能力。
// 参数：ctx/p 为请求主体，projectID/changeID 为项目和要查看的提案坐标。
// 返回值：当前范围及指定提案；未启用、无权或无法读取时返回原业务错误。
func (s *Service) ReadNativeScopeChange(
	ctx context.Context, p platform.Principal, projectID, changeID string,
) (domain.NativeScopeState, error) {
	if !nativeReferencePattern.MatchString(changeID) {
		return domain.NativeScopeState{}, ValidationError("范围变更提案编号无效")
	}
	filter, repository, err := s.nativeScopeAccess(p, "project.read")
	if err != nil {
		return domain.NativeScopeState{}, err
	}
	reader, ok := repository.(nativeScopeChangeReader)
	if !ok {
		return domain.NativeScopeState{}, PreconditionError("原生范围提案读取不可用")
	}
	state, err := reader.ReadNativeScopeChange(ctx, filter, projectID, changeID)
	if err != nil {
		return domain.NativeScopeState{}, err
	}
	state.CanRequest = mayRequestNativeScope(p)
	state.CanApprove = mayApproveNativeScope(p)
	state.ActorID = p.IdentityID
	return state, nil
}
