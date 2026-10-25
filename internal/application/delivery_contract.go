package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	EventContractActivated          = "CONTRACT_ACTIVATED"
	EventContractStampStatus        = "CONTRACT_STAMP_STATUS_SYNCED"
	EventDecompositionAdjusted      = "DECOMPOSITION_ADJUSTED"
	EventDecompositionReturned      = "DECOMPOSITION_RETURNED"
	EventTeamAssigned               = "TEAM_ASSIGNED"
	EventTeamAssignmentRevoked      = "TEAM_ASSIGNMENT_REVOKED"
	EventExecutionTeamAssigned      = "EXECUTION_TEAM_ASSIGNED"
	EventExecutionAssignmentRevoked = "EXECUTION_ASSIGNMENT_REVOKED"
	EventImplementationPlanned      = "IMPLEMENTATION_PLANNED"
	EventImplementationPlanRevoked  = "IMPLEMENTATION_PLAN_REVOKED"
	EventPreparationStarted         = "PREPARATION_STARTED"
	EventPreparationRevoked         = "PREPARATION_REVOKED"
	EventFieldStarted               = "FIELD_STARTED"
	EventFieldCheckedIn             = "FIELD_CHECKED_IN"
	EventFieldRecordSubmitted       = "FIELD_RECORD_SUBMITTED"
	EventFieldSignatureCaptured     = "FIELD_SIGNATURE_CAPTURED"
	EventRollbackRequested          = "ROLLBACK_REQUESTED"
	EventRollbackApproved           = "ROLLBACK_APPROVED"
	EventRollbackRejected           = "ROLLBACK_REJECTED"
	EventRollbackWithdrawn          = "ROLLBACK_WITHDRAWN"
	EventReportCorrectionRequested  = "REPORT_CORRECTION_REQUESTED"
	EventReportCorrectionApproved   = "REPORT_CORRECTION_APPROVED"
	EventReportCorrectionRejected   = "REPORT_CORRECTION_REJECTED"
	EventDeviationReported          = "DEVIATION_REPORTED"
	EventDeviationReviewed          = "DEVIATION_REVIEWED"
	// EventFieldCompleted 是单服务项的现场完成事件：服务项进入报告编制，
	// 全部服务项完成后项目状态由派生规则自动推进，不再有项目级一刀切完成。
	EventFieldCompleted        = "FIELD_COMPLETED"
	EventSpecialMethodReviewed = "SPECIAL_METHOD_REVIEWED"
	EventReportStatusUpdated   = "REPORT_STATUS_UPDATED"
	// EventEquipmentReturned 记录设备归还：写回设备行的归还时间，释放占用。
	EventEquipmentReturned = "EQUIPMENT_RETURNED"
	// EventScopeChangeDetected 由「范围变更检测」在确认拆解时发出：拆解结果与合同清单
	// 的范围不一致，项目进入补充协议处理中等待合同回写。原型拆解流程的「是否范围变更」分支。
	EventScopeChangeDetected = "SCOPE_CHANGE_DETECTED"
	// EventAutomationTriggered 是配置驱动的派生事件：事件落库后有启用的
	//「自动化触发」规则命中时才追加，保证配置表真正参与运行时行为。
	EventAutomationTriggered = "AUTOMATION_TRIGGERED"
	// EventWarningTriggered 是配置驱动的派生事件：任务分配/执行团队指派产生
	// 能力冲突且有启用的「冲突预警规则」时才追加。
	EventWarningTriggered = "WARNING_TRIGGERED"

	assignmentRoleTeamLead       = "team_lead"
	assignmentRoleProjectManager = "project_manager"
	assignmentRoleEngineer       = "engineer"
	assignmentRoleDirectorySize  = 50
)

func (s *Service) ActivateContract(ctx context.Context, p platform.Principal, input domain.ContractActivation) (domain.Project, error) {
	filter, scopeErr := authorizeProjectScope(p, "project.contract.import")
	if scopeErr != nil {
		return domain.Project{}, scopeErr
	}
	if strings.TrimSpace(input.ContractID) == "" || strings.TrimSpace(input.ContractVersion) == "" || strings.TrimSpace(input.Customer) == "" || len(input.Services) == 0 || input.EffectiveAt.IsZero() {
		return domain.Project{}, ErrValidation
	}
	for i := range input.Services {
		var siteErr error
		input.Services[i].Site, input.Services[i].SiteCode, siteErr = s.resolveActiveSite(ctx, p.TenantID, input.Services[i].Site, input.Services[i].SiteCode)
		if siteErr != nil {
			return domain.Project{}, siteErr
		}
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return domain.Project{}, err
	}
	// 兼容投递可能只带稳定 ID，而历史项目只存旧编号。两者的字符串空间
	// 不能混为身份依据：必须在任何查重/盖章同步前受控回读权威编号。
	resolvedNumber := strings.TrimSpace(input.ContractNumber) == ""
	if resolvedNumber {
		if s.Contracts == nil {
			return domain.Project{}, ErrContractUnavailable
		}
		contract, err := s.Contracts.Get(ctx, strings.TrimSpace(input.ContractID))
		if err != nil {
			return domain.Project{}, fmt.Errorf("%w: %v", ErrContractUnavailable, err)
		}
		if strings.TrimSpace(contract.ID) != strings.TrimSpace(input.ContractID) || strings.TrimSpace(contract.Number) == "" || !contract.ApprovalPassed {
			return domain.Project{}, ErrContractUnavailable
		}
		input.ContractNumber = strings.TrimSpace(contract.Number)
	}
	if existing, findErr := repo.FindProjectByContract(ctx, filter, strings.TrimSpace(input.ContractID), strings.TrimSpace(input.ContractNumber)); findErr == nil {
		if resolvedNumber && strings.TrimSpace(existing.Contract) != input.ContractNumber {
			return domain.Project{}, PreconditionError("历史项目合同身份无法安全映射，请核对权威合同编号")
		}
		return syncExistingContract(ctx, repo, p, existing, input.StampedContractUploaded)
	} else if !errors.Is(findErr, ErrNotFound) {
		return domain.Project{}, findErr
	}
	now := time.Now().UTC()
	ownerIdentityID := firstNonEmpty(p.IdentityID, p.UserID)
	ownerOrgID := ""
	if len(filter.OrganizationIDs) == 1 {
		ownerOrgID = filter.OrganizationIDs[0]
	}
	if !filter.AllowAll && !filter.AllowSelf && ownerOrgID == "" {
		return domain.Project{}, ErrForbidden
	}
	contractNumber := firstNonEmpty(strings.TrimSpace(input.ContractNumber), strings.TrimSpace(input.ContractID))
	project := domain.Project{TenantID: p.TenantID, OwnerOrgID: ownerOrgID, OwnerIdentityID: ownerIdentityID, ID: projectID(now), Name: firstNonEmpty(input.ContractName, contractNumber, input.ContractID), Customer: strings.TrimSpace(input.Customer), CustomerID: strings.TrimSpace(input.CustomerID), Contract: contractNumber, ContractID: strings.TrimSpace(input.ContractID), ContractVersion: strings.TrimSpace(input.ContractVersion), Status: "待拆解确认", Team: "未分配", Manager: "—", SupplementStatus: "NONE", CreatedAt: now, UpdatedAt: now}
	// 分组与初始状态由配置决定（原型 PG-CFG-01）：先解析本次生效方案（覆盖规则优先），
	// 再按方案的分组维度生成服务项，并按检测类别域补齐体系要求与特殊方法口径。
	// 「未命中分组规则」不再自动放行——原型拆解流程规定：未命中 → 标记待人工确认并通知业务管理员。
	plan, planErr := s.resolveSplitPlan(ctx, p.TenantID, project.Customer, project.Contract, len(input.Services), contractServiceCategories(input.Services))
	if planErr != nil {
		return domain.Project{}, planErr
	}
	categoryDomain, domainErr := s.splitCategoryDomain(ctx, p.TenantID)
	if domainErr != nil {
		return domain.Project{}, domainErr
	}
	grouped, groupErr := groupContractServicesByPlan(plan, project.Customer, project.Contract, input.Services, categoryDomain)
	if groupErr != nil {
		return domain.Project{}, groupErr
	}
	items := make([]domain.ServiceItem, 0, len(grouped))
	missingCategories := make([]string, 0, 4)
	for index, group := range grouped {
		item := group.Item
		outcome := group.Outcome
		if outcome.MissingRule && !slices.Contains(missingCategories, item.Category) {
			missingCategories = append(missingCategories, item.Category)
		}
		// 技术要求摘要：关闭时留空人工填写，否则沿用合同条款抽取的文本。
		requirement := item.Requirement
		if !plan.GenerateRequirementSummary {
			requirement = ""
		}
		// 直接进入待分配（跳过人工确认）的特殊方法项必须同时进入技术总监复核窗口；
		// 停在待确认的项则由「确认拆解」在确认时置为 PENDING，两条路径都不跳过复核。
		techReview := ""
		if outcome.Special == "是" && outcome.Status == domain.ServiceItemStatusPendingAssign {
			techReview = "PENDING"
		}
		// 必检能力码来自检测类别域：分配工程师时按此校验，留空则只保留渗透测试的固定要求。
		items = append(items, domain.ServiceItem{TenantID: p.TenantID, ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(project.ID, "PJ-"), index+1), ProjectID: project.ID, SourceServiceID: item.SourceID, Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: outcome.SystemStandard, Special: outcome.Special, TestMode: outcome.TestMode, Status: outcome.Status, TechReviewStatus: techReview, RequiredCodes: outcome.RequiredCodes, ConflictStatus: "UNCHECKED"})
	}
	project.Services = len(items)
	event := deliveryEvent(p, project.ID, "", EventContractActivated, map[string]any{"contract_id": project.ContractID, "contract_number": project.Contract, "contract_version": project.ContractVersion, "effective_at": input.EffectiveAt, "service_count": len(items), "stamped_contract_uploaded": input.StampedContractUploaded, "split_rule": splitPlanSummary(plan), "scope_snapshot": splitScopeSnapshot(items), "scope_change_detection": plan.ScopeChangeDetection})
	if len(missingCategories) > 0 && plan.MissingRuleAction == domain.SplitMissingHumanConfirm {
		event.RoleNotification = &domain.RoleNotification{Roles: []string{"business_admin"}, Message: domain.NotificationMessage{
			EventID: ulid.Make().String(), EventType: "SPLIT_RULE_MISSING", Scope: platform.NotificationScopeCrossSystem, Priority: "HIGH",
			Title:         fmt.Sprintf("合同 %s 存在未配置的检测类别，已标记待人工确认", project.Contract),
			Content:       fmt.Sprintf("项目 %s（客户 %s）的检测类别 %s 缺少拆解规则，请补齐规则或人工核对后确认拆解。", project.ID, project.Customer, strings.Join(missingCategories, "、")),
			ReferenceType: "project", ReferenceID: project.ID, OccurredAt: event.CreatedAt, IdempotencyKey: project.ID + "-split-rule-missing",
		}}
	}
	if err := repo.ActivateContract(ctx, project, items, event); err != nil {
		if errors.Is(err, ErrDuplicateContract) {
			// 竞态窗口：find 阶段两请求都未命中，先到者已建好项目，后到者撞唯一键。
			// 回读已存在项目并同步盖章状态，按幂等成功返回，不再抛 500。
			existing, findErr := repo.FindProjectByContract(ctx, filter, project.ContractID, project.Contract)
			if findErr != nil {
				return domain.Project{}, findErr
			}
			if resolvedNumber && strings.TrimSpace(existing.Contract) != input.ContractNumber {
				return domain.Project{}, PreconditionError("历史项目合同身份无法安全映射，请核对权威合同编号")
			}
			return syncExistingContract(ctx, repo, p, existing, input.StampedContractUploaded)
		}
		return domain.Project{}, err
	}
	return project, nil
}

// enqueueBusinessNotification 是未附着于同一交付事件事务的通知
// 的唯一生产路径。保持该边界明确，防止未来功能
// 静默重新引入同步平台调用，并在暂时
// 网络故障后丢失通知。

// syncExistingContract 对已经存在的合同做幂等收尾：同步盖章状态并返回既有项目。
func syncExistingContract(ctx context.Context, repo DeliveryRepository, p platform.Principal, existing domain.Project, stampedUploaded bool) (domain.Project, error) {
	contractID := firstNonEmpty(existing.ContractID, existing.Contract)
	event := deliveryEvent(p, existing.ID, "", EventContractStampStatus, map[string]any{"contract_id": contractID, "contract_number": existing.Contract, "contract_version": existing.ContractVersion, "stamped_contract_uploaded": stampedUploaded})
	if err := repo.SyncContractStampStatus(ctx, existing, stampedUploaded, event); err != nil {
		return domain.Project{}, err
	}
	return existing, nil
}

func (s *Service) AdjustDecomposition(ctx context.Context, p platform.Principal, projectID string, input domain.DecompositionAdjustmentInput) error {
	filter, err := authorizeProjectScope(p, "project.decomposition.manage")
	if err != nil {
		return err
	}
	project, err := s.Repo.GetProject(ctx, filter, projectID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" || strings.TrimSpace(input.SupplementContractID) == "" {
		return ErrValidation
	}
	if err := validateProjectTextLength(input.Reason, 1000, "调整原因"); err != nil {
		return err
	}
	if len(input.Items) == 0 {
		return ValidationError("调整后的服务项清单不能为空")
	}
	if err := validateContractServiceTexts(input.Items); err != nil {
		return err
	}
	if err := s.verifyApprovedSupplementContract(ctx, project, input.SupplementContractID); err != nil {
		return err
	}
	// 拆解调整是人填清单：按原型流程，调整后进入补充协议并重新确认，
	// 因此一律回到「待确认」，不套用默认进入状态；口径（特殊方法/体系要求）仍由检测类别域解析。
	categoryDomain, domainErr := s.splitCategoryDomain(ctx, p.TenantID)
	if domainErr != nil {
		return domainErr
	}
	plan, planErr := s.resolveSplitPlan(ctx, p.TenantID, "", "", len(input.Items), nil)
	if planErr != nil {
		return planErr
	}
	items := make([]domain.ServiceItem, 0, len(input.Items))
	categories := make([]string, 0, len(input.Items))
	for _, source := range input.Items {
		categories = append(categories, source.Category)
	}
	baselines, err := s.detectionCategoryBaselines(ctx, p.TenantID, categories)
	if err != nil {
		return err
	}
	for _, source := range input.Items {
		if source.SourceID == "" || source.Site == "" || source.Batch == "" || source.Category == "" {
			return ErrValidation
		}
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if mode != "STANDARD" && mode != "PENETRATION" {
			return ErrValidation
		}
		var siteErr error
		source.Site, source.SiteCode, siteErr = s.resolveActiveSite(ctx, p.TenantID, source.Site, source.SiteCode)
		if siteErr != nil {
			return siteErr
		}
		outcome := resolveSplitOutcome(plan, source.Category, mode, "", categoryDomain)
		outcome.RequiredCodes = normalizeCapabilityCodes(append(outcome.RequiredCodes, baselines[splitCategoryKey(source.Category)]...))
		// 编号由仓储层在归档旧服务项后按既有最大序号顺延，避免与归档行冲突。
		items = append(items, domain.ServiceItem{ProjectID: projectID, SourceServiceID: source.SourceID, Batch: source.Batch, Site: source.Site, SiteCode: source.SiteCode, Category: source.Category, Requirement: source.Requirement, System: source.System, SystemLevel: source.SystemLevel, SystemStandard: outcome.SystemStandard, TestMode: outcome.TestMode, Special: outcome.Special, RequiredCodes: outcome.RequiredCodes, Status: domain.ServiceItemStatusPendingConfirm, ConflictStatus: "UNCHECKED"})
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, "", EventDecompositionAdjusted, map[string]any{"reason": strings.TrimSpace(input.Reason), "supplement_contract_id": strings.TrimSpace(input.SupplementContractID), "service_items": items, "split_rule": splitPlanSummary(plan)}))
}

// verifyApprovedSupplementContract 防止拆解表单将任意字符串
// 作为补充协议证据。所选记录必须是同一客户下的另一份
// 已完成审批的合同；合同服务仍是
// 权威来源。同时接受 `approved` 和 `active`，因为合同服务
// 在同一事务内执行正常审批副作用（pending -> approved -> active），
// 并原样返回持久化状态。

// verifyApprovedSupplementContract 防止拆解表单将任意字符串
// 作为补充协议证据。所选记录必须是同一客户下的另一份
// 已完成审批的合同；合同服务仍是
// 权威来源。同时接受 `approved` 和 `active`，因为合同服务
// 在同一事务内执行正常审批副作用（pending -> approved -> active），
// 并原样返回持久化状态。
func (s *Service) verifyApprovedSupplementContract(ctx context.Context, project domain.Project, supplementContractID string) error {
	supplementContractID = strings.TrimSpace(supplementContractID)
	if supplementContractID == "" {
		return ValidationError("请选择已审批的补充协议")
	}
	projectContractID, projectContractNumber := strings.TrimSpace(project.ContractID), strings.TrimSpace(project.Contract)
	if supplementContractID == projectContractID ||
		// 历史项目的 contract_id 可能为空而只保留合同号：同样不能把原合同当成补充协议。
		(projectContractID == "" && projectContractNumber != "" && supplementContractID == projectContractNumber) {
		return ValidationError("补充协议不能与原合同相同")
	}
	if s.Contracts == nil {
		return ErrContractUnavailable
	}
	supplement, err := s.Contracts.Get(ctx, supplementContractID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrContractUnavailable, err)
	}
	return validateSupplementContract(project, supplementContractID, supplement)
}

func validateSupplementContract(project domain.Project, supplementContractID string, supplement platform.ApprovedContract) error {
	// 权威回读后统一比较稳定 ID 与历史编号；旧 contract_id 也可能保存编号。
	for _, original := range []string{strings.TrimSpace(project.ContractID), strings.TrimSpace(project.Contract)} {
		if original != "" && (original == strings.TrimSpace(supplement.ID) || original == strings.TrimSpace(supplement.Number)) {
			return ValidationError("补充协议不能与原合同相同")
		}
	}
	if strings.TrimSpace(supplement.ID) != supplementContractID || !supplement.ApprovalPassed || !supplementContractApprovedStatus(supplement.Status) {
		return PreconditionError("所选补充协议尚未审批通过")
	}
	projectCustomerID := strings.TrimSpace(project.CustomerID)
	supplementCustomerID := strings.TrimSpace(supplement.CustomerID)
	if projectCustomerID != "" {
		if supplementCustomerID == "" || projectCustomerID != supplementCustomerID {
			return PreconditionError("补充协议与当前项目不属于同一客户")
		}
		return nil
	}
	projectCustomerName := strings.TrimSpace(project.Customer)
	supplementCustomerName := strings.TrimSpace(supplement.CustomerName)
	if projectCustomerName == "" || supplementCustomerName == "" || !strings.EqualFold(projectCustomerName, supplementCustomerName) {
		return PreconditionError("补充协议与当前项目不属于同一客户")
	}
	return nil
}

// supplementContractApprovedStatus 是补充协议的业务资格状态集合：
// 正常审批在同一事务内完成 pending -> approved -> active，因此 approved（历史停留）
// 与 active（正常完成）都属于审批通过；draft/pending/terminated/archived 不构成调整依据。

// supplementContractApprovedStatus 是补充协议的业务资格状态集合：
// 正常审批在同一事务内完成 pending -> approved -> active，因此 approved（历史停留）
// 与 active（正常完成）都属于审批通过；draft/pending/terminated/archived 不构成调整依据。
func supplementContractApprovedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "approved", "active":
		return true
	default:
		return false
	}
}

// ScanSlaNotifications 扫描一个租户的 SLA 超期/临近项并投递站内提醒。
//
// 这是"主动提醒"的唯一入口：平台此前没有任何周期任务，SLA 只能在有人打开页面查询时可见，
// 超期不会主动告诉任何人。扫描按租户执行，使用租户边界而非某个登录用户的授权
// （系统侧任务，不代替用户的权限判断）。
//
// 幂等键按「服务项 + 口径 + UTC 日期」生成：同一天重复扫描不会重复打扰，
// 跨天仍会重新提醒（超期是持续状态，每天都值得提醒一次）。

// ReturnToDecomposition 把尚未进入实施阶段的服务项退回拆解确认。该动作与撤销分配不同：
// 它会改变服务项阶段，因此必须使用拆解管理权限并留下原因、原责任链和版本快照。
func (s *Service) ReturnToDecomposition(ctx context.Context, p platform.Principal, itemID string, input domain.AssignmentRevokeInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.decomposition.manage", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.decomposition.manage", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return ValidationError("请填写退回拆解确认的原因")
	}
	filter, err := authorizeProjectScope(p, "project.decomposition.manage")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Status != "待分配" {
		return PreconditionError("仅可将待分配服务项退回拆解确认")
	}
	previous := append([]string{item.TeamLeadID, item.ProjectManagerID}, item.EngineerIDs...)
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventDecompositionReturned, map[string]any{
		"reason":                      reason,
		"expected_version":            input.ExpectedVersion,
		"previous_team_lead_id":       item.TeamLeadID,
		"previous_project_manager_id": item.ProjectManagerID,
		"previous_engineer_ids":       item.EngineerIDs,
		"revoked_user_ids":            previous,
	}))
}

// ReviewSpecialMethod 技术总监对特殊方法（渗透测试专项）服务项的适用性与风险控制复核。
// 复核通过后才能发布实施计划；驳回后可在修正后再次提交复核。
func (s *Service) ReviewSpecialMethod(ctx context.Context, p platform.Principal, itemID string, input domain.SpecialMethodReviewInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.special_method.review", itemID); err != nil {
		return err
	}
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "APPROVED" && decision != "REJECTED" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventSpecialMethodReviewed, map[string]any{"decision": decision, "comment": strings.TrimSpace(input.Comment)}))
}

// UpdateReportStatus 服务项报告从编制中逐级推进：编制中→已审核→已签发→已归档。
// 报告推进把项目推向「已完成」，是交付收口动作，使用独立的报告管理权限，
// 不与现场执行权限（project.field.complete）混用。

// splitGroup 是按生效分组方案合并后的一个服务项：清单行 + 已解析的拆解口径。
type splitGroup struct {
	Item    domain.ContractService
	Outcome SplitOutcome
}

// contractServiceCategories 取出合同清单里出现过的检测类别（供覆盖规则匹配服务项数/类别）。

// groupContractServicesByPlan 按生效方案的分组维度把合同清单合并成服务项。
//
// 原型的默认规则是「同一批次 + 同一检测类别 = 1 个服务项」；维度由配置决定，因此这里
// 不能再硬编码「场所 + 批次 + 检测类别」。合并时保留可追溯信息（来源清单行、技术要求、
// 系统名称等按唯一值拼接），并逐组解析拆解口径（状态 / 方法类型 / 体系要求 / 缺规则）。
func groupContractServicesByPlan(plan SplitPlan, customer, contract string, sources []domain.ContractService, domainIndex map[string]domain.DetectionCategory) ([]splitGroup, error) {
	groups := map[string]splitGroup{}
	keys := make([]string, 0, len(sources))
	seen := map[string]bool{}
	for _, source := range sources {
		source.SourceID = strings.TrimSpace(source.SourceID)
		source.Site = strings.TrimSpace(source.Site)
		source.Batch = strings.TrimSpace(source.Batch)
		source.Category = strings.TrimSpace(source.Category)
		if source.SourceID == "" || source.Site == "" || source.Batch == "" || source.Category == "" || seen[source.SourceID] {
			return nil, ErrValidation
		}
		seen[source.SourceID] = true
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if mode != "STANDARD" && mode != "PENETRATION" {
			return nil, ErrValidation
		}
		// 合同清单不带「体系要求」，因此方法类型与体系要求都以域/清单解析结果参与分组：
		// 维度 2 选了体系要求时，同一批次的类别默认体系不同就会分成两条服务项。
		outcome := resolveSplitOutcome(plan, source.Category, mode, "", domainIndex)
		key := splitGroupKey(plan, customer, contract, source, outcome.TestMode, outcome.SystemStandard)
		if current, ok := groups[key]; ok {
			current.Item.SourceID = joinUnique(current.Item.SourceID, source.SourceID)
			current.Item.Requirement = joinUnique(current.Item.Requirement, strings.TrimSpace(source.Requirement))
			current.Item.System = joinUnique(current.Item.System, strings.TrimSpace(source.System))
			current.Item.SystemLevel = joinUnique(current.Item.SystemLevel, strings.TrimSpace(source.SystemLevel))
			current.Item.Name = joinUnique(current.Item.Name, strings.TrimSpace(source.Name))
			groups[key] = current
			continue
		}
		source.TestMode = mode
		groups[key] = splitGroup{Item: source, Outcome: outcome}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]splitGroup, 0, len(keys))
	for _, key := range keys {
		out = append(out, groups[key])
	}
	return out, nil
}
