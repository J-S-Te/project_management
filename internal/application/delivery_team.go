package application

import (
	"context"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
	"time"
)

func (s *Service) AssignTeam(ctx context.Context, p platform.Principal, itemID string, input domain.TeamAssignmentInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.team.assign", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.team.assign", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	input.TeamLeadID = strings.TrimSpace(input.TeamLeadID)
	if err := s.ensurePersonnelNotFrozen(ctx, p.TenantID, input.TeamLeadID); err != nil {
		return err
	}
	if input.TeamLeadID == "" {
		return ErrValidation
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	people, err := repo.FindCapabilities(ctx, p.TenantID, time.Now().UTC().Format(time.RFC3339), []string{input.TeamLeadID})
	if err != nil {
		return err
	}
	canonical, ok := qualifiedUserID(input.TeamLeadID, people)
	if !ok {
		return PreconditionError("所选团队负责人不在有效人员资质库中，请先维护人员资质")
	}
	allowed, err := s.assignmentRoleUserIDs(ctx, assignmentRoleTeamLead)
	if err != nil {
		return err
	}
	if _, ok := allowed[canonical]; !ok {
		return PreconditionError("所选人员未被授予团队负责人角色，请先维护角色权限")
	}
	input.TeamLeadID = canonical
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventTeamAssigned, map[string]any{"team_lead_id": input.TeamLeadID, "expected_version": input.ExpectedVersion}))
}

// RevokeTeamAssignment 只允许在尚未发布计划的待分配阶段撤销团队负责人。撤销团队负责人
// 必须同时失效下游执行团队与能力校验，避免新负责人继承旧负责人的指派结果。

// RevokeTeamAssignment 只允许在尚未发布计划的待分配阶段撤销团队负责人。撤销团队负责人
// 必须同时失效下游执行团队与能力校验，避免新负责人继承旧负责人的指派结果。
func (s *Service) RevokeTeamAssignment(ctx context.Context, p platform.Principal, itemID string, input domain.AssignmentRevokeInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.team.revoke", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.team.revoke", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" {
		return ValidationError("请填写撤销团队负责人分配的原因")
	}
	filter, err := authorizeProjectScope(p, "project.team.revoke")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Status != "待分配" || strings.TrimSpace(item.TeamLeadID) == "" {
		return PreconditionError("仅可撤销待分配服务项的已分配团队负责人")
	}
	previous := append([]string{item.TeamLeadID, item.ProjectManagerID}, item.EngineerIDs...)
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventTeamAssignmentRevoked, map[string]any{"reason": strings.TrimSpace(input.Reason), "expected_version": input.ExpectedVersion, "previous_team_lead_id": item.TeamLeadID, "previous_project_manager_id": item.ProjectManagerID, "previous_engineer_ids": item.EngineerIDs, "revoked_user_ids": previous}))
}

// ReturnToDecomposition 把尚未进入实施阶段的服务项退回拆解确认。该动作与撤销分配不同：
// 它会改变服务项阶段，因此必须使用拆解管理权限并留下原因、原责任链和版本快照。

func (s *Service) AssignExecutionTeam(ctx context.Context, p platform.Principal, itemID string, input domain.ExecutionAssignmentInput) (domain.ConflictCheckResult, error) {
	filter, err := authorizeProjectScope(p, "project.execution.assign")
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	existing, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	// 客户端声明的版本与服务端不一致即 409：避免后者静默覆盖前者刚提交的指派。
	if input.ExpectedVersion != 0 && existing.Version != input.ExpectedVersion {
		return domain.ConflictCheckResult{}, ErrConflict
	}
	input.ProjectManagerID = strings.TrimSpace(input.ProjectManagerID)
	input.EngineerIDs = normalizePersonnelIDs(input.EngineerIDs)
	if err := s.ensurePersonnelNotFrozen(ctx, p.TenantID, append([]string{input.ProjectManagerID}, input.EngineerIDs...)...); err != nil {
		return domain.ConflictCheckResult{}, err
	}
	if input.ProjectManagerID == "" || len(input.EngineerIDs) == 0 {
		return domain.ConflictCheckResult{}, ErrValidation
	}
	// 服务项自身携带的必检能力码来自检测类别配置，是服务端可信的最低要求。
	// 客户端可以追加本次分配的额外要求，但不能通过少传/改传 required_codes
	// 缩减服务项要求，否则可绕过资质校验把任务分给不具备必备资质的工程师。
	required := append([]string{}, existing.RequiredCodes...)
	required = append(required, input.RequiredCodes...)
	required = normalizeCapabilityCodes(required)
	items, _, err := s.Repo.ListServiceItems(ctx, filter, "", 0, 0)
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	found := false
	for _, item := range items {
		if item.ID == itemID {
			found = true
			if item.TestMode == "PENETRATION" {
				required = append(required, "PENETRATION_TEST")
				required = normalizeCapabilityCodes(required)
			}
			break
		}
	}
	if !found {
		return domain.ConflictCheckResult{}, ErrNotFound
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	ids := append([]string{}, input.EngineerIDs...)
	caps, err := repo.FindCapabilities(ctx, p.TenantID, time.Now().UTC().Format(time.RFC3339), ids)
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	managerCaps, err := repo.FindCapabilities(ctx, p.TenantID, time.Now().UTC().Format(time.RFC3339), []string{input.ProjectManagerID})
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	managerID, ok := qualifiedUserID(input.ProjectManagerID, managerCaps)
	if !ok {
		return domain.ConflictCheckResult{}, PreconditionError("所选项目经理不在有效人员资质库中，请先维护人员资质")
	}
	input.ProjectManagerID = managerID
	canonicalEngineers := make([]string, 0, len(input.EngineerIDs))
	for _, engineerID := range input.EngineerIDs {
		canonical, exists := qualifiedUserID(engineerID, caps)
		if !exists {
			return domain.ConflictCheckResult{}, PreconditionError("所选工程师不在有效人员资质库中，请先维护人员资质")
		}
		canonicalEngineers = append(canonicalEngineers, canonical)
	}
	input.EngineerIDs = canonicalEngineers
	managerIDs, err := s.assignmentRoleUserIDs(ctx, assignmentRoleProjectManager)
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	if _, ok := managerIDs[input.ProjectManagerID]; !ok {
		return domain.ConflictCheckResult{}, PreconditionError("所选人员未被授予项目经理角色，请先维护角色权限")
	}
	engineerIDs, err := s.assignmentRoleUserIDs(ctx, assignmentRoleEngineer)
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	for _, engineerID := range input.EngineerIDs {
		if _, ok := engineerIDs[engineerID]; !ok {
			return domain.ConflictCheckResult{}, PreconditionError("所选人员未被授予工程师角色，请先维护角色权限")
		}
	}
	result := checkCapabilities(required, ids, caps)
	payload := map[string]any{"project_manager_id": input.ProjectManagerID, "engineer_ids": input.EngineerIDs, "required_codes": required, "conflict_status": map[bool]string{true: "PASSED", false: "CONFLICT"}[result.Passed], "conflicts": result.Conflicts, "expected_version": input.ExpectedVersion}
	if err := s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventExecutionTeamAssigned, payload)); err != nil {
		return domain.ConflictCheckResult{}, err
	}
	return result, nil
}

// RevokeExecutionAssignment 让团队负责人纠正项目经理或工程师分配，但不会撤销团队负责人。
