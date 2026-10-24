package application

import (
	"context"
	"errors"
	"strings"
)

// FreezeChecker 报告某用户是否处于人员交接冻结；由组合根接线到交接仓储。
type FreezeChecker interface {
	ActiveFreezeUser(ctx context.Context, tenantID, userID string) (bool, error)
}

// ErrManagerFrozenByHandover 表示目标项目经理正处于离职交接冻结中，不得承接新项目责任。
var ErrManagerFrozenByHandover = errors.New("project manager is frozen by an in-flight handover")

// UseFreezeChecker 注入冻结检查器（组合根调用）。
func (s *Service) UseFreezeChecker(checker FreezeChecker) *Service {
	s.freeze = checker
	return s
}

// ensureManagerNotFrozen 在写路径上阻止把新项目责任分配给交接中的用户；
// 未接线检查器时保持既有行为，查询失败则 失败时安全拒绝 上报。
func (s *Service) ensureManagerNotFrozen(ctx context.Context, tenantID, managerID string) error {
	if s == nil || s.freeze == nil || strings.TrimSpace(managerID) == "" {
		return nil
	}
	frozen, err := s.freeze.ActiveFreezeUser(ctx, tenantID, strings.TrimSpace(managerID))
	if err != nil {
		return err
	}
	if frozen {
		return ErrManagerFrozenByHandover
	}
	return nil
}

// ensurePersonnelNotFrozen 在写路径上阻止把新责任分配给交接中的用户。
// 团队负责人、项目经理与工程师都会承接实际实施责任，必须同一口径校验；
// 未接线检查器时保持既有行为，查询失败按 失败时安全拒绝 上报。
func (s *Service) ensurePersonnelNotFrozen(ctx context.Context, tenantID string, ids ...string) error {
	for _, id := range ids {
		if err := s.ensureManagerNotFrozen(ctx, tenantID, id); err != nil {
			return err
		}
	}
	return nil
}
