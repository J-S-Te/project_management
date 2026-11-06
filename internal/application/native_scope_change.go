package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type NativeScopeRepository interface {
	ReplayNativeScope(context.Context, platform.ScopeFilter, string, string, domain.NativeScopeInput) (domain.NativeScopeChange, bool, error)
	ReadNativeScope(context.Context, platform.ScopeFilter, string) (domain.NativeScopeState, error)
	RequestNativeScope(context.Context, platform.ScopeFilter, string, string, domain.NativeScopeInput, []domain.ServiceItem) (domain.NativeScopeChange, error)
	DecideNativeScope(context.Context, platform.ScopeFilter, string, string, string, domain.NativeScopeDecision) (domain.NativeScopeChange, error)
}

// mayRequestNativeScope 限业务管理员发起本地范围变更。
// 参数：p 为当前真实授权主体。
// 返回值：岗位与既有拆解管理权限同时满足时为真。
func mayRequestNativeScope(p platform.Principal) bool {
	return p.Has("project.decomposition.manage") && principalHasRole(p, "business_admin")
}

// mayApproveNativeScope 将技术审批与业务发起权限分离。
// 参数：p 为当前真实授权主体。
// 返回值：技术总监或项目管理员具备精确审批权限时为真。
func mayApproveNativeScope(p platform.Principal) bool {
	return p.Has("project.native_scope.approve") && (principalHasRole(p, "technical_director") || principalHasRole(p, "admin"))
}

// NativeScopeFingerprint 对完整范围业务字段计算稳定摘要。
// 参数：items 为不可变范围，不包含履约状态和数据库行号。
// 返回值：按稳定来源行排序的SHA256摘要。
func NativeScopeFingerprint(items []domain.ServiceItem) string {
	type line struct {
		ID, Batch, Site, SiteCode, Category, Requirement, System, SystemLevel, SystemStandard, TestMode, Special string
		Codes                                                                                                    []string
	}
	lines := make([]line, 0, len(items))
	for _, i := range items {
		codes := append([]string{}, i.RequiredCodes...)
		sort.Strings(codes)
		lines = append(lines, line{i.SourceServiceID, i.Batch, i.Site, i.SiteCode, i.Category, i.Requirement, i.System, i.SystemLevel, i.SystemStandard, i.TestMode, i.Special, codes})
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].ID < lines[j].ID })
	raw, _ := json.Marshal(lines)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// nativeScopeAccess 校验显式能力、租户、身份与受保护数据范围。
// 参数：p 为当前主体，permission 为本次操作权限。
// 返回值：已绑定身份的数据范围；依赖缺失时关闭能力。
func (s *Service) nativeScopeAccess(p platform.Principal, permission string) (platform.ScopeFilter, NativeScopeRepository, error) {
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return filter, nil, err
	}
	if !s.NativeEnabled {
		return filter, nil, PreconditionError("原生范围变更未启用")
	}
	if strings.TrimSpace(p.IdentityID) == "" || strings.TrimSpace(p.TenantID) == "" {
		return filter, nil, ErrForbidden
	}
	repo, ok := s.Repo.(NativeScopeRepository)
	if !ok {
		return filter, nil, PreconditionError("原生范围仓储不可用")
	}
	return filter, repo, nil
}

// ReadNativeScope 返回原生权威范围与有界审批历史。
// 参数：ctx/p 为当前请求身份，id 为项目主键。
// 返回值：真实范围、当前版本和岗位能力，合同来源拒绝。
func (s *Service) ReadNativeScope(ctx context.Context, p platform.Principal, id string) (domain.NativeScopeState, error) {
	filter, repo, err := s.nativeScopeAccess(p, "project.read")
	if err != nil {
		return domain.NativeScopeState{}, err
	}
	state, err := repo.ReadNativeScope(ctx, filter, id)
	state.CanRequest = mayRequestNativeScope(p)
	state.CanApprove = mayApproveNativeScope(p)
	state.ActorID = p.IdentityID
	return state, err
}

// RequestNativeScope 提交完整本地范围但不改变生效版本。
// 参数：ctx/p 为真实身份，id 为项目，in 为版本、幂等号及范围。
// 返回值：持久待审批提案或可解释的权限、输入及并发错误。
func (s *Service) RequestNativeScope(ctx context.Context, p platform.Principal, id string, in domain.NativeScopeInput) (domain.NativeScopeChange, error) {
	if !mayRequestNativeScope(p) {
		return domain.NativeScopeChange{}, ErrForbidden
	}
	filter, repo, err := s.nativeScopeAccess(p, "project.decomposition.manage")
	if err != nil {
		return domain.NativeScopeChange{}, err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !nativeReferencePattern.MatchString(in.OperationID) || in.BaseScopeVersion < 1 || in.ExpectedProjectVersion < 1 || in.Reason == "" || strings.ContainsRune(in.Reason, '\x00') || len([]rune(in.Reason)) > 500 {
		return domain.NativeScopeChange{}, ValidationError("请填写有效操作号、当前版本和变更原因")
	}
	if replay, found, replayErr := repo.ReplayNativeScope(ctx, filter, id, p.IdentityID, in); replayErr != nil || found {
		return replay, replayErr
	}
	if err = s.ensureManagerNotFrozen(ctx, p.TenantID, p.IdentityID); err != nil {
		return domain.NativeScopeChange{}, err
	}
	items, err := s.nativeServiceItems(ctx, p.TenantID, in.ServiceItems)
	if err != nil {
		return domain.NativeScopeChange{}, err
	}
	seen := map[string]bool{}
	for index, requested := range in.ServiceItems {
		line := strings.TrimSpace(requested.SourceID)
		if line != "" && (!nativeReferencePattern.MatchString(line) || seen[line]) {
			return domain.NativeScopeChange{}, ValidationError("范围行标识无效或重复")
		}
		if line != "" {
			seen[line] = true
		}
		items[index].SourceServiceID = line
	}
	return repo.RequestNativeScope(ctx, filter, id, p.IdentityID, in, items)
}

// DecideNativeScope 以异人审批或申请人撤回关闭提案。
// 参数：ctx/p 为当前身份，project/change 为坐标，in 为原操作号和明确决定。
// 返回值：真实最终提案；自批、撤权、版本漂移或未知决定均拒绝。
func (s *Service) DecideNativeScope(ctx context.Context, p platform.Principal, project, change string, in domain.NativeScopeDecision) (domain.NativeScopeChange, error) {
	in.Decision = strings.ToUpper(strings.TrimSpace(in.Decision))
	in.Comment = strings.TrimSpace(in.Comment)
	permission := "project.native_scope.approve"
	if in.Decision == "WITHDRAW" {
		permission = "project.decomposition.manage"
		if !mayRequestNativeScope(p) {
			return domain.NativeScopeChange{}, ErrForbidden
		}
	} else if !mayApproveNativeScope(p) {
		return domain.NativeScopeChange{}, ErrForbidden
	}
	filter, repo, err := s.nativeScopeAccess(p, permission)
	if err != nil {
		return domain.NativeScopeChange{}, err
	}
	if !nativeReferencePattern.MatchString(in.OperationID) || in.ExpectedVersion < 1 || in.Comment == "" || strings.ContainsRune(in.Comment, '\x00') || len([]rune(in.Comment)) > 500 || (in.Decision != "APPROVE" && in.Decision != "REJECT" && in.Decision != "WITHDRAW") {
		return domain.NativeScopeChange{}, ValidationError("请填写当前版本、操作号和审批意见")
	}
	if err = s.ensureManagerNotFrozen(ctx, p.TenantID, p.IdentityID); err != nil {
		return domain.NativeScopeChange{}, err
	}
	return repo.DecideNativeScope(ctx, filter, project, change, p.IdentityID, in)
}
