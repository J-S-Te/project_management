package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

const penetrationCapabilityCode = "PENETRATION_TEST"

type PenetrationWorkPackageRepository interface {
	EnsurePenetrationWorkPackage(context.Context, string, domain.ServiceItem, string, string, time.Time) (domain.PenetrationWorkPackage, error)
	GetPenetrationWorkPackage(context.Context, string, string) (domain.PenetrationWorkPackage, error)
	SavePenetrationDecision(context.Context, string, string, domain.PenetrationDecisionInput, string, time.Time) (domain.PenetrationWorkPackage, error)
	SavePenetrationPlan(context.Context, string, string, domain.PenetrationPlanInput, string, time.Time) (domain.PenetrationWorkPackage, error)
	AdvancePenetrationExecution(context.Context, string, string, domain.PenetrationExecutionInput, string, time.Time) (domain.PenetrationWorkPackage, error)
	AdvancePenetrationReport(context.Context, string, string, domain.PenetrationReportStatusInput, string, time.Time) (domain.PenetrationWorkPackage, error)
	RegisterPenetrationReportArtifact(context.Context, string, string, domain.PenetrationReportArtifactInput, string, time.Time) (domain.PenetrationWorkPackage, error)
	ListPenetrationReportRevisions(context.Context, string, string) ([]domain.ReportRevision, error)
	ListPenetrationPersonnelConflicts(context.Context, string, string, []string, time.Time, time.Time) ([]string, error)
	PenetrationWorkPackageStats(context.Context, platform.ScopeFilter) (domain.PenetrationWorkPackageStats, error)
}

func (s *Service) penetrationRepo() (PenetrationWorkPackageRepository, error) {
	repo, ok := s.Repo.(PenetrationWorkPackageRepository)
	if !ok {
		return nil, errors.New("penetration work package repository unavailable")
	}
	return repo, nil
}

func isEqualProtectionServiceItem(item domain.ServiceItem) bool {
	if strings.EqualFold(strings.TrimSpace(item.TestMode), "PENETRATION") {
		return false
	}
	category := strings.ReplaceAll(strings.TrimSpace(item.Category), " ", "")
	return strings.Contains(category, "等保") || strings.Contains(category, "等级保护")
}

func requireIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return "", ValidationError("写操作必须提供长度不超过 128 字符的 Idempotency-Key")
	}
	return value, nil
}

func (s *Service) penetrationParent(ctx context.Context, p platform.Principal, permission, itemID string) (domain.ServiceItem, error) {
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return domain.ServiceItem{}, err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return domain.ServiceItem{}, err
	}
	if !isEqualProtectionServiceItem(item) {
		return domain.ServiceItem{}, PreconditionError("只有等保测评服务项可以建立内嵌渗透测试专项；独立渗透测试请沿用服务项流程")
	}
	return item, nil
}

func requireProjectManager(p platform.Principal, item domain.ServiceItem) error {
	if strings.TrimSpace(item.ProjectManagerID) == "" {
		return PreconditionError("请先为等保服务项指派项目经理")
	}
	if strings.TrimSpace(item.ProjectManagerID) != strings.TrimSpace(p.UserID) {
		return ErrForbidden
	}
	return nil
}

func (s *Service) EnsurePenetrationWorkPackage(ctx context.Context, p platform.Principal, itemID string, input domain.EnsurePenetrationWorkPackageInput) (domain.PenetrationWorkPackage, error) {
	item, err := s.penetrationParent(ctx, p, "project.implementation.plan", itemID)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if err = requireProjectManager(p, item); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if input.ExpectedVersion == 0 {
		return domain.PenetrationWorkPackage{}, ValidationError("expected_version 必须为当前服务项版本")
	}
	if input.ExpectedVersion != item.Version {
		return domain.PenetrationWorkPackage{}, ErrConflict
	}
	key, err := requireIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.IdempotencyKey = key
	repo, err := s.penetrationRepo()
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return repo.EnsurePenetrationWorkPackage(ctx, p.TenantID, item, p.UserID, key, time.Now().UTC())
}

func (s *Service) GetPenetrationWorkPackage(ctx context.Context, p platform.Principal, itemID string) (domain.PenetrationWorkPackage, error) {
	if _, err := s.penetrationParent(ctx, p, "project.read", itemID); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	repo, err := s.penetrationRepo()
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return repo.GetPenetrationWorkPackage(ctx, p.TenantID, itemID)
}

func (s *Service) SavePenetrationDecision(ctx context.Context, p platform.Principal, itemID string, input domain.PenetrationDecisionInput) (domain.PenetrationWorkPackage, error) {
	item, err := s.penetrationParent(ctx, p, "project.implementation.plan", itemID)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if err = requireProjectManager(p, item); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.DecisionStatus = strings.ToUpper(strings.TrimSpace(input.DecisionStatus))
	if input.DecisionStatus != "REQUIRED" && input.DecisionStatus != "NOT_REQUIRED" {
		return domain.PenetrationWorkPackage{}, ValidationError("开展结论必须是 REQUIRED 或 NOT_REQUIRED")
	}
	input.CustomerContact = strings.TrimSpace(input.CustomerContact)
	input.CommunicationSummary = strings.TrimSpace(input.CommunicationSummary)
	if input.CustomerContact == "" || input.CommunicationSummary == "" {
		return domain.PenetrationWorkPackage{}, ValidationError("请填写客户联系人和沟通结论")
	}
	if _, err = time.Parse(time.RFC3339, input.CommunicatedAt); err != nil {
		return domain.PenetrationWorkPackage{}, ValidationError("沟通时间必须是有效的日期时间")
	}
	key, err := requireIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.IdempotencyKey = key
	repo, err := s.penetrationRepo()
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	current, err := repo.GetPenetrationWorkPackage(ctx, p.TenantID, itemID)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if current.DecisionStatus != "PENDING" && current.DecisionStatus != input.DecisionStatus && strings.TrimSpace(input.ChangeReason) == "" {
		return domain.PenetrationWorkPackage{}, ValidationError("变更开展结论必须填写变更原因")
	}
	if current.ExecutionStatus == "IN_PROGRESS" || current.ExecutionStatus == "COMPLETED" || current.ReportStatus != "NONE" {
		return domain.PenetrationWorkPackage{}, PreconditionError("专项已经开始执行或进入报告流程，不能直接变更开展结论；取消请使用专项取消操作")
	}
	return repo.SavePenetrationDecision(ctx, p.TenantID, itemID, input, p.UserID, time.Now().UTC())
}

func (s *Service) SavePenetrationPlan(ctx context.Context, p platform.Principal, itemID string, input domain.PenetrationPlanInput) (domain.PenetrationWorkPackage, error) {
	item, err := s.penetrationParent(ctx, p, "project.implementation.plan", itemID)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if err = requireProjectManager(p, item); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	start, end, authStart, authEnd, err := validateEmbeddedPenetrationPlan(input)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.EngineerIDs = normalizePersonnelIDs(input.EngineerIDs)
	if len(input.EngineerIDs) == 0 {
		return domain.PenetrationWorkPackage{}, ValidationError("请至少指派一名渗透测试工程师")
	}
	repo, err := s.penetrationRepo()
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if err = s.validatePenetrationEngineers(ctx, p.TenantID, input.EngineerIDs); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	conflicts, err := repo.ListPenetrationPersonnelConflicts(ctx, p.TenantID, itemID, input.EngineerIDs, start, end)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if len(conflicts) > 0 {
		return domain.PenetrationWorkPackage{}, ConflictError("渗透测试工程师在计划时间内存在任务冲突：" + strings.Join(conflicts, "；"))
	}
	if start.Before(authStart) || end.After(authEnd) {
		return domain.PenetrationWorkPackage{}, ValidationError("计划开展时间必须完整落在客户授权有效期内")
	}
	key, err := requireIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.IdempotencyKey = key
	return repo.SavePenetrationPlan(ctx, p.TenantID, itemID, input, p.UserID, time.Now().UTC())
}

func validateEmbeddedPenetrationPlan(input domain.PenetrationPlanInput) (time.Time, time.Time, time.Time, time.Time, error) {
	required := map[string]string{
		"授权书编号": input.AuthDocNo, "授权范围": input.AuthScope, "计划测试范围": input.TestScope,
		"测试时间窗": input.TestWindow, "应急联系人": input.EmergencyContact, "回滚方案": input.RollbackPlan,
	}
	missing := make([]string, 0)
	for label, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, label)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, ValidationError("请补充渗透测试专项要素：" + strings.Join(missing, "、"))
	}
	start, e1 := time.Parse(time.RFC3339, input.PlannedStart)
	end, e2 := time.Parse(time.RFC3339, input.PlannedEnd)
	authStart, e3 := time.Parse(time.RFC3339, input.AuthStart)
	authEnd, e4 := time.Parse(time.RFC3339, input.AuthEnd)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || !end.After(start) || !authEnd.After(authStart) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, ValidationError("计划时间和授权时间必须完整且结束时间晚于开始时间")
	}
	if !scopeContained(input.TestScope, input.AuthScope) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, ValidationError("计划测试范围不得超出客户授权范围")
	}
	return start, end, authStart, authEnd, nil
}

func scopeContained(testScope, authScope string) bool {
	authorized := strings.ToLower(strings.Join(strings.Fields(authScope), ""))
	parts := strings.FieldsFunc(strings.ToLower(testScope), func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n'
	})
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		part = strings.Join(strings.Fields(part), "")
		if part == "" || !strings.Contains(authorized, part) {
			return false
		}
	}
	return true
}

func (s *Service) validatePenetrationEngineers(ctx context.Context, tenantID string, ids []string) error {
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	capabilities, err := repo.FindCapabilities(ctx, tenantID, time.Now().UTC().Format(time.RFC3339), ids)
	if err != nil {
		return err
	}
	for _, candidate := range ids {
		qualified := false
		for _, capability := range capabilities {
			canonical, ok := qualifiedUserID(candidate, []domain.Capability{capability})
			if !ok || canonical == "" {
				continue
			}
			for _, code := range capability.Codes {
				if strings.EqualFold(strings.TrimSpace(code), penetrationCapabilityCode) {
					qualified = true
					break
				}
			}
		}
		if !qualified {
			return PreconditionError(fmt.Sprintf("人员 %s 缺少有效的 PENETRATION_TEST 能力", candidate))
		}
	}
	return nil
}

func (s *Service) AdvancePenetrationExecution(ctx context.Context, p platform.Principal, itemID string, input domain.PenetrationExecutionInput) (domain.PenetrationWorkPackage, error) {
	input.Action = strings.ToUpper(strings.TrimSpace(input.Action))
	if input.Action != "START" && input.Action != "COMPLETE" && input.Action != "CANCEL" {
		return domain.PenetrationWorkPackage{}, ValidationError("专项执行动作不正确")
	}
	permission := "project.field.execute"
	if input.Action == "CANCEL" {
		permission = "project.implementation.plan"
	}
	item, err := s.penetrationParent(ctx, p, permission, itemID)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	repo, err := s.penetrationRepo()
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	current, err := repo.GetPenetrationWorkPackage(ctx, p.TenantID, itemID)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if input.Action == "CANCEL" {
		if err = requireProjectManager(p, item); err != nil {
			return domain.PenetrationWorkPackage{}, err
		}
		if strings.TrimSpace(input.Reason) == "" || strings.TrimSpace(input.CustomerContact) == "" || strings.TrimSpace(input.CommunicationSummary) == "" {
			return domain.PenetrationWorkPackage{}, ValidationError("取消专项必须填写原因、客户联系人和最新沟通结论")
		}
		if _, err = time.Parse(time.RFC3339, input.CommunicatedAt); err != nil {
			return domain.PenetrationWorkPackage{}, ValidationError("沟通时间必须是有效的日期时间")
		}
	} else if !containsString(current.EngineerIDs, p.UserID) {
		return domain.PenetrationWorkPackage{}, ErrForbidden
	}
	if input.Action == "START" {
		now := time.Now().UTC()
		authStart, e1 := time.Parse(time.RFC3339, current.AuthStart)
		authEnd, e2 := time.Parse(time.RFC3339, current.AuthEnd)
		if e1 != nil || e2 != nil || now.Before(authStart) || now.After(authEnd) {
			return domain.PenetrationWorkPackage{}, PreconditionError("当前时间不在客户授权有效期内，不能开始渗透测试")
		}
		if err = s.validatePenetrationEngineers(ctx, p.TenantID, current.EngineerIDs); err != nil {
			return domain.PenetrationWorkPackage{}, err
		}
		plannedStart, e3 := time.Parse(time.RFC3339, current.PlannedStart)
		plannedEnd, e4 := time.Parse(time.RFC3339, current.PlannedEnd)
		if e3 != nil || e4 != nil {
			return domain.PenetrationWorkPackage{}, PreconditionError("专项计划时间不完整，不能开始渗透测试")
		}
		conflicts, conflictErr := repo.ListPenetrationPersonnelConflicts(ctx, p.TenantID, itemID, current.EngineerIDs, plannedStart, plannedEnd)
		if conflictErr != nil {
			return domain.PenetrationWorkPackage{}, conflictErr
		}
		if len(conflicts) > 0 {
			return domain.PenetrationWorkPackage{}, ConflictError("渗透测试工程师在计划时间内存在任务冲突：" + strings.Join(conflicts, "；"))
		}
	}
	key, err := requireIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.IdempotencyKey = key
	return repo.AdvancePenetrationExecution(ctx, p.TenantID, itemID, input, p.UserID, time.Now().UTC())
}

func (s *Service) AdvancePenetrationReport(ctx context.Context, p platform.Principal, itemID string, input domain.PenetrationReportStatusInput) (domain.PenetrationWorkPackage, error) {
	input.Phase = strings.ToUpper(strings.TrimSpace(input.Phase))
	permission := penetrationReportPermission(input.Phase)
	if _, err := s.penetrationParent(ctx, p, permission, itemID); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	key, err := requireIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.IdempotencyKey = key
	repo, err := s.penetrationRepo()
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return repo.AdvancePenetrationReport(ctx, p.TenantID, itemID, input, p.UserID, time.Now().UTC())
}

func penetrationReportPermission(phase string) string {
	switch phase {
	case "DRAFTING", "SUBMITTED":
		return "project.report.prepare"
	case "APPROVED":
		return "project.report.review"
	case "ISSUED":
		return "project.report.issue"
	case "ARCHIVED":
		return "project.report.archive"
	default:
		return "project.report.prepare"
	}
}

func (s *Service) RegisterPenetrationReportArtifact(ctx context.Context, p platform.Principal, itemID string, input domain.PenetrationReportArtifactInput) (domain.PenetrationWorkPackage, error) {
	if _, err := s.penetrationParent(ctx, p, "project.report.prepare", itemID); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if err := normalizeFileEvidence(&input.ReportArtifactInput, "渗透测试专项报告"); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	key, err := requireIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.IdempotencyKey = key
	repo, err := s.penetrationRepo()
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return repo.RegisterPenetrationReportArtifact(ctx, p.TenantID, itemID, input, p.UserID, time.Now().UTC())
}

func (s *Service) ListPenetrationReportRevisions(ctx context.Context, p platform.Principal, itemID string) ([]domain.ReportRevision, error) {
	if _, err := s.penetrationParent(ctx, p, "project.read", itemID); err != nil {
		return nil, err
	}
	repo, err := s.penetrationRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListPenetrationReportRevisions(ctx, p.TenantID, itemID)
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(expected) {
			return true
		}
	}
	return false
}
