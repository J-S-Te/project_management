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
	"strconv"
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
	// EventSupplementClosed 是补充协议分支的受控关账动作（BIZ-PROJECT-04）：
	// 携带已审批补充协议、原因与关账时接受的范围指纹，只有它能显式清零
	// supplement_status（确认拆解不再静默清除）。
	EventSupplementClosed = "SUPPLEMENT_CLOSED"
	// EventAutomationTriggered 是配置驱动的派生事件：事件落库后有启用的
	//「自动化触发」规则命中时才追加，保证配置表真正参与运行时行为。
	EventAutomationTriggered = "AUTOMATION_TRIGGERED"
	// EventWarningTriggered 是配置驱动的派生事件：任务分配/执行团队指派产生
	// 能力冲突且有启用的「冲突预警规则」时才追加。
	EventWarningTriggered = "WARNING_TRIGGERED"

	assignmentRoleTeamLead       = "team_lead"
	assignmentRoleProjectManager = "project_manager"
	assignmentRoleEngineer       = "engineer"
	// assignmentRolePenetrationEngineer 与权限清单里的渗透测试工程师角色码一致：
	// 渗透类服务项的专属人员按该角色指派，实施工程师角色不再是唯一入口。
	assignmentRolePenetrationEngineer = "penetration_engineer"
	assignmentRoleDirectorySize       = 50
)

// ActivateContract 执行合同生效事件驱动的投递（机器入口）。
//
// 该路径只在合同管理系统完成审批并投递生效事件时使用：调用方是经机器令牌验证的
// 合同服务，载荷本身即合同服务的权威结果，因此保留“可信投递”语义，按载荷落库。
// 用户会话入口绝不能复用本方法——浏览器可以伪造同样形状的载荷，必须改用
// ActivateContractFromUser 由合同服务权威回读合同与 Service 目录。
//
// 参数：ctx 为请求，p 为已认证主体，input 为普通激活信息。
// 返回值：项目；来源非法或业务校验失败时返回错误。
func (s *Service) ActivateContract(ctx context.Context, p platform.Principal, input domain.ContractActivation) (domain.Project, error) {
	if !platform.NormalProjectContract(platform.ApprovedContract{OriginKind: input.OriginKind, ImportMode: input.ImportMode, OriginImportItemID: input.OriginImportItemID}) {
		return domain.Project{}, ErrImportPolicy
	}
	return s.activateContract(ctx, p, input, false, nil)
}

// ActivateContractFromUser 是用户会话建项入口的权威回读路径。
//
// 请求体只被允许提供 contract_id、所选 source_id、服务项的现场补充字段（场所等）、
// effective_at 与盖章标记；合同编号、客户、客户标识、合同版本、服务项类别/批次/体系
// 要求/方法类型一律来自合同服务：先按 contract_id 读取已审批合同并比对版本，再读取
// 合同服务项目录重建服务范围。任何回读失败都按依赖不可用失败关闭，不降级使用请求体。
//
// 参数：ctx 为请求，p 为用户主体，input 为浏览器提交的激活信息。
// 返回值：创建/同步后的项目，或权限、政策、权威回读与校验错误。
func (s *Service) ActivateContractFromUser(ctx context.Context, p platform.Principal, input domain.ContractActivation) (domain.Project, error) {
	contractID := strings.TrimSpace(input.ContractID)
	if contractID == "" {
		// 只有合同号的载荷无法定位权威合同：编号在历史项目里可能被当身份使用，
		// 不能凭请求体字符串推出合同与客户。
		return domain.Project{}, ValidationError("请选择已通过审批的合同后建项；仅凭合同编号不能建立项目")
	}
	if s.Contracts == nil {
		return domain.Project{}, ErrContractUnavailable
	}
	contract, err := s.Contracts.Get(ctx, contractID)
	if err != nil {
		return domain.Project{}, fmt.Errorf("%w: %v", ErrContractUnavailable, err)
	}
	if strings.TrimSpace(contract.ID) != contractID || !contract.ApprovalPassed || !platform.NormalProjectContract(contract) || !contractApprovedStatus(contract.Status) {
		return domain.Project{}, PreconditionError("所选合同尚未通过审批，不能建立项目")
	}
	if strings.TrimSpace(contract.Number) == "" || strings.TrimSpace(contract.CustomerName) == "" {
		return domain.Project{}, PreconditionError("所选合同缺少合同编号或客户信息，请先在合同管理系统中补全")
	}
	contractVersion := strconv.FormatUint(contract.Version, 10)
	// 客户端若带上合同版本，必须与权威版本一致：版本不同意味着所选范围可能已过期，
	// 静默按最新版本落库会让用户以为自己提交的是旧清单。
	if requestedVersion := strings.TrimSpace(input.ContractVersion); requestedVersion != "" && requestedVersion != contractVersion {
		return domain.Project{}, PreconditionError("合同版本已更新，请刷新合同后重新建项")
	}
	if len(input.Services) == 0 {
		return domain.Project{}, ValidationError("项目至少需要一个服务项")
	}
	catalog, catalogErr := s.Contracts.GetServiceItems(ctx, contractID)
	if catalogErr != nil {
		return domain.Project{}, fmt.Errorf("%w: %v", ErrContractUnavailable, catalogErr)
	}
	if strings.TrimSpace(catalog.ContractID) != contractID || catalog.ContractVersion != contract.Version {
		return domain.Project{}, PreconditionError("合同服务范围已发生变化，请刷新合同后重新建项")
	}
	services, servicesErr := approvedContractServices(input.Services, catalog.ServiceItems)
	if servicesErr != nil {
		return domain.Project{}, servicesErr
	}
	// 请求体中的合同号/客户/客户 ID/合同名称/来源标记全部丢弃，只保留不可伪造字段。
	input.ContractID = contractID
	input.ContractNumber = strings.TrimSpace(contract.Number)
	input.ContractVersion = contractVersion
	input.ContractName = strings.TrimSpace(contract.Title)
	input.Customer = strings.TrimSpace(contract.CustomerName)
	input.CustomerID = strings.TrimSpace(contract.CustomerID)
	input.OriginKind = contract.OriginKind
	input.ImportMode = contract.ImportMode
	input.OriginImportItemID = contract.OriginImportItemID
	input.Services = services
	return s.activateContract(ctx, p, input, false, nil)
}

// activateContract 复用合同拆解与项目事务，导入时强制人工确认服务范围。
// 参数：ctx 为请求，p 为机器或用户主体，input 为激活信息，imported 表示导入，targetOwner 为受控负责人。
// 返回值：已持久化项目或业务校验错误。
func (s *Service) activateContract(ctx context.Context, p platform.Principal, input domain.ContractActivation, imported bool, targetOwner *platform.OwnerDirectoryUser) (domain.Project, error) {
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
		if strings.TrimSpace(contract.ID) != strings.TrimSpace(input.ContractID) || strings.TrimSpace(contract.Number) == "" || !contract.ApprovalPassed || !platform.NormalProjectContract(contract) {
			return domain.Project{}, ErrContractUnavailable
		}
		input.ContractNumber = strings.TrimSpace(contract.Number)
	}
	if existing, findErr := repo.FindProjectByContract(ctx, filter, strings.TrimSpace(input.ContractID), strings.TrimSpace(input.ContractNumber)); findErr == nil {
		if resolvedNumber && strings.TrimSpace(existing.Contract) != input.ContractNumber {
			return domain.Project{}, PreconditionError("历史项目合同身份无法安全映射，请核对权威合同编号")
		}
		if imported {
			return domain.Project{}, ErrImportConflict
		}
		primaryIdentity := existing.ContractID == strings.TrimSpace(input.ContractID) ||
			existing.Contract == strings.TrimSpace(input.ContractNumber)
		if !primaryIdentity {
			return domain.Project{}, ErrImportPolicy
		}
		return syncExistingContract(ctx, repo, p, existing, input.StampedContractUploaded)
	} else if !errors.Is(findErr, ErrNotFound) {
		return domain.Project{}, findErr
	}
	now := time.Now().UTC()
	ownerIdentityID := firstNonEmpty(p.IdentityID, p.UserID)
	ownerOrgID := ""
	if targetOwner != nil {
		ownerIdentityID = targetOwner.UserID
		for _, org := range targetOwner.Organizations {
			if org.IsPrimary {
				ownerOrgID = org.OrganizationID
				break
			}
		}
	}
	if targetOwner == nil && len(filter.OrganizationIDs) == 1 {
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
	if imported {
		for i := range items {
			items[i].Status = domain.ServiceItemStatusPendingConfirm
			items[i].TechReviewStatus = ""
		}
	}
	project.Services = len(items)
	event := deliveryEvent(p, project.ID, "", EventContractActivated, map[string]any{"contract_id": project.ContractID, "contract_number": project.Contract, "contract_version": project.ContractVersion, "effective_at": input.EffectiveAt, "service_count": len(items), "stamped_contract_uploaded": input.StampedContractUploaded, "split_rule": splitPlanSummary(plan), "scope_snapshot": splitScopeSnapshot(items), "scope_change_detection": plan.ScopeChangeDetection, "origin_kind": input.OriginKind, "import_mode": input.ImportMode, "origin_import_item_id": input.OriginImportItemID})
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
			if imported {
				return domain.Project{}, ErrImportConflict
			}
			primaryIdentity := existing.ContractID == strings.TrimSpace(input.ContractID) ||
				existing.Contract == strings.TrimSpace(input.ContractNumber)
			if !primaryIdentity {
				return domain.Project{}, ErrImportPolicy
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

// AdjustDecomposition 依据权威补充合同调整合同来源项目的服务范围。
// 参数：ctx 为请求，p 为真实主体，projectID 为目标项目，input 为补充合同调整。
// 返回值：权限、来源、审批、目录或事务错误；原生范围变更未启用时拒绝。
func (s *Service) AdjustDecomposition(ctx context.Context, p platform.Principal, projectID string, input domain.DecompositionAdjustmentInput) error {
	filter, err := authorizeProjectScope(p, "project.decomposition.manage")
	if err != nil {
		return err
	}
	project, err := s.Repo.GetProject(ctx, filter, projectID)
	if err != nil {
		return err
	}
	if project.SourceKind == "NATIVE" {
		return PreconditionError("原生项目范围变更尚未启用受控审批流程")
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
	supplement, err := s.verifyApprovedSupplementContract(ctx, project, input.SupplementContractID)
	if err != nil {
		return err
	}
	// 调整后的范围必须落在补充协议的服务项目录内：客户端只选 source_id 并补现场字段
	// （场所等），类别/批次/体系要求/方法类型由权威目录重建。目录不可用即失败关闭，
	// 不能降级成客户端清单，否则范围变更指纹会失去权威基准。
	catalog, catalogErr := s.Contracts.GetServiceItems(ctx, strings.TrimSpace(supplement.ID))
	if catalogErr != nil {
		return fmt.Errorf("%w: %v", ErrContractUnavailable, catalogErr)
	}
	if strings.TrimSpace(catalog.ContractID) != strings.TrimSpace(supplement.ID) || catalog.ContractVersion != supplement.Version {
		return PreconditionError("补充协议服务范围已发生变化，请重新选择后再调整")
	}
	selected, err := approvedContractServices(input.Items, catalog.ServiceItems)
	if err != nil {
		return err
	}
	if err := validateContractServiceTexts(selected); err != nil {
		return err
	}
	// 拆解调整是人填清单：按原型流程，调整后进入补充协议并重新确认，
	// 因此一律回到「待确认」，不套用默认进入状态；口径（特殊方法/体系要求）仍由检测类别域解析。
	categoryDomain, domainErr := s.splitCategoryDomain(ctx, p.TenantID)
	if domainErr != nil {
		return domainErr
	}
	plan, planErr := s.resolveSplitPlan(ctx, p.TenantID, "", "", len(selected), nil)
	if planErr != nil {
		return planErr
	}
	items := make([]domain.ServiceItem, 0, len(selected))
	categories := make([]string, 0, len(selected))
	for _, source := range selected {
		if source.Batch == "" || source.Category == "" {
			return fmt.Errorf("%w: supplement contract service %s lacks batch or category", ErrContractUnavailable, source.SourceID)
		}
		categories = append(categories, source.Category)
	}
	baselines, err := s.detectionCategoryBaselines(ctx, p.TenantID, categories)
	if err != nil {
		return err
	}
	for _, source := range selected {
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if mode != "STANDARD" && mode != "PENETRATION" {
			return fmt.Errorf("%w: supplement contract service %s has invalid test mode", ErrContractUnavailable, source.SourceID)
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
	return s.applyEvent(ctx, deliveryEvent(p, projectID, "", EventDecompositionAdjusted, map[string]any{"reason": strings.TrimSpace(input.Reason), "supplement_contract_id": strings.TrimSpace(supplement.ID), "service_items": items, "split_rule": splitPlanSummary(plan)}))
}

// verifyApprovedSupplementContract 复核合同来源项目的同客户已审批补充协议。
// 参数：ctx 为请求，project 为目标项目，supplementContractID 为待回读合同。
// 返回值：权威补充合同；原生来源、审批不足或依赖失败时返回错误。
func (s *Service) verifyApprovedSupplementContract(ctx context.Context, project domain.Project, supplementContractID string) (platform.ApprovedContract, error) {
	if project.SourceKind == "NATIVE" {
		return platform.ApprovedContract{}, PreconditionError("原生项目不适用合同补充协议")
	}
	supplementContractID = strings.TrimSpace(supplementContractID)
	if supplementContractID == "" {
		return platform.ApprovedContract{}, ValidationError("请选择已审批的补充协议")
	}
	projectContractID, projectContractNumber := strings.TrimSpace(project.ContractID), strings.TrimSpace(project.Contract)
	if supplementContractID == projectContractID ||
		// 历史项目的 contract_id 可能为空而只保留合同号：同样不能把原合同当成补充协议。
		(projectContractID == "" && projectContractNumber != "" && supplementContractID == projectContractNumber) {
		return platform.ApprovedContract{}, ValidationError("补充协议不能与原合同相同")
	}
	if s.Contracts == nil {
		return platform.ApprovedContract{}, ErrContractUnavailable
	}
	supplement, err := s.Contracts.Get(ctx, supplementContractID)
	if err != nil {
		return platform.ApprovedContract{}, fmt.Errorf("%w: %v", ErrContractUnavailable, err)
	}
	if err := validateSupplementContract(project, supplementContractID, supplement); err != nil {
		return platform.ApprovedContract{}, err
	}
	return supplement, nil
}

// validateSupplementContract 核对同客户普通补充合同身份与审批状态。
// 参数：project 为目标项目，supplementContractID 为请求身份，supplement 为权威合同。
// 返回值：合法时为空，否则返回业务前置错误。
func validateSupplementContract(project domain.Project, supplementContractID string, supplement platform.ApprovedContract) error {
	// 权威回读后统一比较稳定 ID 与历史编号；旧 contract_id 也可能保存编号。
	for _, original := range []string{strings.TrimSpace(project.ContractID), strings.TrimSpace(project.Contract)} {
		if original != "" && (original == strings.TrimSpace(supplement.ID) || original == strings.TrimSpace(supplement.Number)) {
			return ValidationError("补充协议不能与原合同相同")
		}
	}
	if strings.TrimSpace(supplement.ID) != supplementContractID || !supplement.ApprovalPassed || !platform.NormalProjectContract(supplement) || !contractApprovedStatus(supplement.Status) {
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

// contractApprovedStatus 是合同的业务资格状态集合（补充协议与用户会话建项共用）：
// 正常审批在同一事务内完成 pending -> approved -> active，因此 approved（历史停留）
// 与 active（正常完成）都属于审批通过；draft/pending/terminated/archived 不构成
// 建项或调整依据。
func contractApprovedStatus(status string) bool {
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
