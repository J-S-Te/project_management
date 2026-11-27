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
const penetrationQualificationCode = "渗透测试工程师"

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

// FindPenetrationEngineerByUsername 按登录用户名精确查找当前专项可选人员。
// 参数：ctx 为请求上下文，p 为当前会话，itemID 为等保服务项，username 为登录用户名。
// 返回值：合格人员的平台用户 ID；无匹配时为空，目录或权限异常时返回错误。
func (s *Service) FindPenetrationEngineerByUsername(ctx context.Context, p platform.Principal, itemID, username string) (string, error) {
	item, err := s.penetrationParent(ctx, p, "project.implementation.plan", itemID)
	if err != nil {
		return "", err
	}
	if err := requireProjectManager(p, item); err != nil {
		return "", err
	}
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 128 ||
		strings.IndexFunc(username, func(r rune) bool { return r < 33 || r > 126 }) != -1 {
		return "", ErrValidation
	}
	if s.Personnel == nil {
		return "", ErrPersonnelUnavailable
	}
	page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{Username: username, Page: 1, PageSize: 2})
	if err != nil || !page.UsernameMatch || page.Total > 1 || len(page.Items) > 1 {
		return "", ErrPersonnelUnavailable
	}
	if len(page.Items) == 0 || page.Items[0].UserID == "" {
		return "", nil
	}
	userID := page.Items[0].UserID
	if err := s.validatePenetrationEngineers(ctx, p.TenantID, []string{userID}); err != nil {
		if errors.Is(err, ErrPrecondition) {
			return "", nil
		}
		return "", err
	}
	return userID, nil
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
	if err := validatePenetrationDecisionTexts(input); err != nil {
		return domain.PenetrationWorkPackage{}, err
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

// 用途：保存附属专项的实施安排，允许后续补齐授权信息。
// 参数：ctx 为请求上下文，p 为操作者，itemID 为父服务项，input 为计划表单。
// 返回值：更新后的专项，或权限、计划校验、版本冲突错误。
func (s *Service) SavePenetrationPlan(ctx context.Context, p platform.Principal, itemID string, input domain.PenetrationPlanInput) (domain.PenetrationWorkPackage, error) {
	item, err := s.penetrationParent(ctx, p, "project.implementation.plan", itemID)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if err = requireProjectManager(p, item); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	if err := validatePenetrationPlanTexts(input); err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	start, end, _, _, err := validateEmbeddedPenetrationPlan(input)
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
	key, err := requireIdempotencyKey(input.IdempotencyKey)
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	input.IdempotencyKey = key
	return repo.SavePenetrationPlan(ctx, p.TenantID, itemID, input, p.UserID, time.Now().UTC())
}

// 用途：校验计划日期及当前已填写的授权范围和日期。
// 参数：input 为计划表单，授权信息可留空。
// 返回值：计划起止、授权起止及校验错误，未填的授权日期为零值。
func validateEmbeddedPenetrationPlan(input domain.PenetrationPlanInput) (time.Time, time.Time, time.Time, time.Time, error) {
	start, startErr := time.Parse(time.RFC3339, input.PlannedStart)
	end, endErr := time.Parse(time.RFC3339, input.PlannedEnd)
	if startErr != nil || endErr != nil || !end.After(start) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, ValidationError("计划时间必须完整且结束时间晚于开始时间")
	}
	authStartText := strings.TrimSpace(input.AuthStart)
	authEndText := strings.TrimSpace(input.AuthEnd)
	authStart, authStartErr := time.Parse(time.RFC3339, authStartText)
	authEnd, authEndErr := time.Parse(time.RFC3339, authEndText)
	if (authStartText != "" && authStartErr != nil) || (authEndText != "" && authEndErr != nil) ||
		(!authStart.IsZero() && !authEnd.IsZero() && !authEnd.After(authStart)) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, ValidationError("授权日期必须有效，授权截止必须晚于授权生效")
	}
	if (!authStart.IsZero() && start.Before(authStart)) || (!authEnd.IsZero() && end.After(authEnd)) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, ValidationError("计划开展时间必须完整落在客户授权有效期内")
	}
	if strings.TrimSpace(input.AuthScope) != "" && strings.TrimSpace(input.TestScope) != "" &&
		!scopeContained(input.TestScope, input.AuthScope) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, ValidationError("计划测试范围不得超出客户授权范围")
	}
	return start, end, authStart, authEnd, nil
}

// ValidatePenetrationExecutionPlan 用途：校验开始测试所需的完整计划及当前授权有效期。
// 参数：plan 为已经保存的专项计划，now 为实际开始测试的时刻。
// 返回值：用于冲突检查的计划起止时间，或禁止开始测试的原因。
func ValidatePenetrationExecutionPlan(plan domain.PenetrationWorkPackage, now time.Time) (time.Time, time.Time, error) {
	required := map[string]string{
		"授权书编号": plan.AuthDocNo, "授权范围": plan.AuthScope, "计划测试范围": plan.TestScope,
		"测试时间窗": plan.TestWindow, "应急联系人": plan.EmergencyContact, "回滚方案": plan.RollbackPlan,
		"授权生效": plan.AuthStart, "授权截止": plan.AuthEnd,
	}
	missing := make([]string, 0)
	for label, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, label)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return time.Time{}, time.Time{}, PreconditionError("开始测试前请补齐专项要素：" + strings.Join(missing, "、"))
	}
	start, end, authStart, authEnd, err := validateEmbeddedPenetrationPlan(domain.PenetrationPlanInput{
		PlannedStart: plan.PlannedStart, PlannedEnd: plan.PlannedEnd,
		AuthStart: plan.AuthStart, AuthEnd: plan.AuthEnd, AuthScope: plan.AuthScope, TestScope: plan.TestScope,
	})
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if now.Before(authStart) || now.After(authEnd) {
		return time.Time{}, time.Time{}, PreconditionError("当前时间不在客户授权有效期内，不能开始渗透测试")
	}
	return start, end, nil
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

// validatePenetrationEngineers 核对每位专项工程师的有效身份和渗透测试资质。
// 参数：ctx 为请求上下文，tenantID 为认证租户，ids 为申请指派的人员标识。
// 返回值：全部合格时为 nil，资质缺失时返回前置条件错误。
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
				normalized := normalizeCapabilityCode(code)
				if normalized == penetrationCapabilityCode || normalized == penetrationQualificationCode {
					qualified = true
					break
				}
			}
		}
		if !qualified {
			return PreconditionError(fmt.Sprintf("人员 %s 缺少有效的渗透测试资质", candidate))
		}
	}
	return nil
}

// 用途：由具备资格的人员开始、完成或取消附属专项。
// 参数：ctx 为请求上下文，p 为操作者，itemID 为父服务项，input 为当前版本及执行动作。
// 返回值：更新后的专项，或权限、授权、状态及版本错误。
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
		if err := validateProjectTextLength(input.Reason, 1000, "取消原因"); err != nil {
			return domain.PenetrationWorkPackage{}, err
		}
		if err := validateProjectTextLength(input.CustomerContact, 128, "客户联系人"); err != nil {
			return domain.PenetrationWorkPackage{}, err
		}
		if err := validateProjectTextLength(input.CommunicationSummary, 2000, "沟通结论"); err != nil {
			return domain.PenetrationWorkPackage{}, err
		}
		if _, err = time.Parse(time.RFC3339, input.CommunicatedAt); err != nil {
			return domain.PenetrationWorkPackage{}, ValidationError("沟通时间必须是有效的日期时间")
		}
	} else if !containsString(current.EngineerIDs, p.UserID) {
		return domain.PenetrationWorkPackage{}, ErrForbidden
	}
	if input.Action == "START" {
		plannedStart, plannedEnd, planErr := ValidatePenetrationExecutionPlan(current, time.Now().UTC())
		if planErr != nil {
			return domain.PenetrationWorkPackage{}, planErr
		}
		if err = s.validatePenetrationEngineers(ctx, p.TenantID, current.EngineerIDs); err != nil {
			return domain.PenetrationWorkPackage{}, err
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
	// 专项报告同样必须回源文件网关核验（存在性/租户/应用/SHA/绑定），
	// 不能只凭客户端回执形状登记证据（BIZ-PROJECT-03）。
	if err := s.verifyFileEvidence(ctx, p, itemID, input.ReportArtifactInput, "渗透测试专项报告"); err != nil {
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
