package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

const (
	EventContractActivated     = "CONTRACT_ACTIVATED"
	EventContractStampStatus   = "CONTRACT_STAMP_STATUS_SYNCED"
	EventDecompositionAdjusted = "DECOMPOSITION_ADJUSTED"
	EventTeamAssigned          = "TEAM_ASSIGNED"
	EventExecutionTeamAssigned = "EXECUTION_TEAM_ASSIGNED"
	EventImplementationPlanned = "IMPLEMENTATION_PLANNED"
	EventPreparationStarted    = "PREPARATION_STARTED"
	EventFieldRecordSubmitted  = "FIELD_RECORD_SUBMITTED"
	EventDeviationReported     = "DEVIATION_REPORTED"
	EventDeviationReviewed     = "DEVIATION_REVIEWED"
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
)

type DeliveryRepository interface {
	FindProjectByContractVersion(context.Context, platform.ScopeFilter, string, string) (domain.Project, error)
	ActivateContract(context.Context, domain.Project, []domain.ServiceItem, domain.DeliveryEvent) error
	SyncContractStampStatus(context.Context, domain.Project, bool, domain.DeliveryEvent) error
	ApplyDeliveryEvent(context.Context, domain.DeliveryEvent) error
	ListSlaOverdue(context.Context, platform.ScopeFilter) ([]domain.SlaOverdueItem, error)
	ListDeliveryEvents(context.Context, platform.ScopeFilter, string) ([]domain.DeliveryEvent, error)
	FindProjectForDeviation(context.Context, platform.ScopeFilter, string) (string, string, error)
	UpsertCapability(context.Context, domain.Capability, string) (domain.Capability, error)
	ListCapabilities(context.Context, string, string) ([]domain.Capability, error)
	FindCapabilities(context.Context, string, string, []string) ([]domain.Capability, error)
	ListEquipmentReservations(context.Context, string, string) ([]domain.EquipmentReservation, error)
	// UpdateCapabilityIdentities 回写人员档案的身份复核结果。
	UpdateCapabilityIdentities(context.Context, string, map[string]string, time.Time) error
}

func (s *Service) deliveryRepo() (DeliveryRepository, error) {
	repo, ok := s.Repo.(DeliveryRepository)
	if !ok {
		return nil, errors.New("delivery repository unavailable")
	}
	return repo, nil
}

// ListSlaOverdue 返回两类超期/临近超期口径的合并列表：
//  1. 计划完成时间已过且尚未终结的服务项（与配置无关的固定口径）；
//  2. 启用的 pm_sla 规则判定：服务项停留在规则状态超过 deadline_hours（超期），
//     或剩余时间不足 remind_hours（临近提醒）。停留时长以服务项 updated_at 为准，
//     每次状态推进都会刷新该时间。
func (s *Service) ListSlaOverdue(ctx context.Context, p platform.Principal) ([]domain.SlaOverdueItem, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return nil, err
	}
	candidates, err := repo.ListSlaOverdue(ctx, filter)
	if err != nil {
		return nil, err
	}
	rules, err := s.Repo.ListRules(ctx, p.TenantID, "sla")
	if err != nil {
		return nil, err
	}
	computed, unparsablePlannedEnd := computeSlaItems(candidates, rules, time.Now().UTC())
	if unparsablePlannedEnd > 0 && s.Logger != nil {
		// 计划完成时间无法解析的服务项会被静默排除在 SLA 口径之外，必须可诊断。
		s.Logger.Warn("sla skipped items with unparsable planned_end",
			"tenant_id", p.TenantID, "skipped", unparsablePlannedEnd)
	}
	// 与列表/详情同一脱敏口径：该响应会带出 site/category。
	return s.applyFieldPermissionsToSlaItems(ctx, p, computed)
}

// computeSlaItems 把候选服务项展开成两类 SLA 口径：
//  1. 计划完成时间已过（与配置无关的固定口径），每项一条；
//  2. 命中启用规则的状态停留超期 / 临近提醒，各一条。
//
// 候选集由仓储给出（仅排除终态），两类口径的过滤在这里分别判定：
// 计划完成口径排除已交付（现场实施完成）的项，否则它们只要计划时间已过就会永久留在超期列表；
// 状态口径不按计划时间过滤，否则「计划时间未到但已临近超时」的项永远不会被提醒。
func computeSlaItems(candidates []domain.SlaOverdueItem, rules []domain.Rule, now time.Time) ([]domain.SlaOverdueItem, int) {
	items := make([]domain.SlaOverdueItem, 0, len(candidates))
	// 计划完成时间无法解析的服务项会被跳过：它们既进不了超期列表，也不会有任何提示，
	// 会静默消失在 SLA 口径之外。把数量返回给调用方，由调用方告警。
	unparsablePlannedEnd := 0
	for _, candidate := range candidates {
		if candidate.Status != domain.ProjectStatusFieldCompleted && candidate.PlannedEnd != "" {
			plannedEnd, err := time.Parse(time.RFC3339, candidate.PlannedEnd)
			if err != nil {
				unparsablePlannedEnd++
			} else if plannedEnd.Before(now) {
				overdue := candidate
				overdue.Kind = domain.SlaKindPlanEndOverdue
				overdue.OverdueHours = int64(now.Sub(plannedEnd).Hours())
				items = append(items, overdue)
			}
		}
		for _, rule := range rules {
			// 两侧都做 Trim：脏数据（状态带空格）不能让规则静默失效。
			if !rule.Enabled || strings.TrimSpace(rule.Status) != strings.TrimSpace(candidate.Status) {
				continue
			}
			// 计时基准是「进入当前状态的时刻」。没有基准时不判超期，
			// 避免把零值时间当成极早的起点而误报天文数字的停留时长。
			if candidate.StatusChangedAt.IsZero() {
				continue
			}
			elapsed := int64(now.Sub(candidate.StatusChangedAt).Hours())
			switch {
			case elapsed > int64(rule.DeadlineHours):
				breach := candidate
				breach.Kind = domain.SlaKindStatusOverdue
				breach.OverdueHours = elapsed - int64(rule.DeadlineHours)
				breach.RuleName = rule.Name
				breach.RuleStatus = rule.Status
				breach.DeadlineHours = rule.DeadlineHours
				items = append(items, breach)
			case rule.RemindHours > 0 && int64(rule.DeadlineHours)-elapsed <= int64(rule.RemindHours):
				breach := candidate
				breach.Kind = domain.SlaKindStatusApproaching
				breach.OverdueHours = int64(rule.DeadlineHours) - elapsed
				breach.RuleName = rule.Name
				breach.RuleStatus = rule.Status
				breach.DeadlineHours = rule.DeadlineHours
				items = append(items, breach)
			}
		}
	}
	return items, unparsablePlannedEnd
}

func (s *Service) ActivateContract(ctx context.Context, p platform.Principal, input domain.ContractActivation) (domain.Project, error) {
	filter, scopeErr := authorizeProjectScope(p, "project.contract.import")
	if scopeErr != nil {
		return domain.Project{}, scopeErr
	}
	if strings.TrimSpace(input.ContractID) == "" || strings.TrimSpace(input.ContractVersion) == "" || strings.TrimSpace(input.Customer) == "" || len(input.Services) == 0 || input.EffectiveAt.IsZero() {
		return domain.Project{}, ErrValidation
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return domain.Project{}, err
	}
	if existing, findErr := repo.FindProjectByContractVersion(ctx, filter, strings.TrimSpace(input.ContractID), strings.TrimSpace(input.ContractVersion)); findErr == nil {
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
	project := domain.Project{TenantID: p.TenantID, OwnerOrgID: ownerOrgID, OwnerIdentityID: ownerIdentityID, ID: projectID(now), Name: firstNonEmpty(input.ContractName, input.ContractID), Customer: strings.TrimSpace(input.Customer), CustomerID: strings.TrimSpace(input.CustomerID), Contract: strings.TrimSpace(input.ContractID), ContractVersion: strings.TrimSpace(input.ContractVersion), Status: "待拆解确认", Team: "未分配", Manager: "—", SupplementStatus: "NONE", CreatedAt: now, UpdatedAt: now}
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
		items = append(items, domain.ServiceItem{TenantID: p.TenantID, ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(project.ID, "PJ-"), index+1), ProjectID: project.ID, SourceServiceID: item.SourceID, Batch: item.Batch, Site: item.Site, Category: item.Category, Requirement: requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: outcome.SystemStandard, Special: outcome.Special, TestMode: outcome.TestMode, Status: outcome.Status, TechReviewStatus: techReview, ConflictStatus: "UNCHECKED"})
	}
	project.Services = len(items)
	event := deliveryEvent(p, project.ID, "", EventContractActivated, map[string]any{"contract_id": project.Contract, "contract_version": project.ContractVersion, "effective_at": input.EffectiveAt, "service_count": len(items), "stamped_contract_uploaded": input.StampedContractUploaded, "split_rule": splitPlanSummary(plan), "scope_snapshot": splitScopeSnapshot(items), "scope_change_detection": plan.ScopeChangeDetection})
	if err := repo.ActivateContract(ctx, project, items, event); err != nil {
		if errors.Is(err, ErrDuplicateContract) {
			// 竞态窗口：find 阶段两请求都未命中，先到者已建好项目，后到者撞唯一键。
			// 回读已存在项目并同步盖章状态，按幂等成功返回，不再抛 500。
			existing, findErr := repo.FindProjectByContractVersion(ctx, filter, project.Contract, project.ContractVersion)
			if findErr != nil {
				return domain.Project{}, findErr
			}
			return syncExistingContract(ctx, repo, p, existing, input.StampedContractUploaded)
		}
		return domain.Project{}, err
	}
	// 缺规则的告警是主流程的派生副作用：放在事务提交之后，绝不能影响已经落库的拆解结果。
	if len(missingCategories) > 0 && plan.MissingRuleAction == domain.SplitMissingHumanConfirm {
		s.notifyMissingSplitRule(ctx, p, project, missingCategories)
	}
	return project, nil
}

// notifyMissingSplitRule 在检测类别不在检测类别域内、或分组规则无法确定时，
// 按原型「分组规则缺失时」的默认口径通知业务管理员：标记待人工确认并提醒核对。
func (s *Service) notifyMissingSplitRule(ctx context.Context, p platform.Principal, project domain.Project, categories []string) {
	if s.Notifications == nil || s.Personnel == nil || len(categories) == 0 {
		return
	}
	recipients := s.roleRecipients(ctx, p.TenantID, "business_admin")
	if len(recipients) == 0 {
		return
	}
	notification := platform.NotificationEvent{
		EventID:   ulid.Make().String(),
		EventType: "SPLIT_RULE_MISSING",
		// 必须是平台白名单取值，写错会让整条通知被判 400。
		Scope:    platform.NotificationScopeCrossSystem,
		Priority: "HIGH",
		Title:    fmt.Sprintf("合同 %s 存在未配置的检测类别，已标记待人工确认", project.Contract),
		Content: fmt.Sprintf("项目 %s（客户 %s）按分组规则无法确定以下检测类别的拆解口径：%s。"+
			"请在「系统配置 · 拆解规则」的检测类别域中补齐，或人工核对拆解结果后再确认拆解。",
			project.ID, project.Customer, strings.Join(categories, "、")),
		ReferenceType:  "project",
		ReferenceID:    project.ID,
		Recipients:     recipients,
		OccurredAt:     time.Now().UTC(),
		IdempotencyKey: project.ID + "-split-rule-missing",
	}
	if err := s.Notifications.Publish(ctx, notification); err != nil && s.Logger != nil {
		s.Logger.Warn("publish split rule missing notification failed", "project_id", project.ID, "error", err)
	}
}

// roleRecipients 按应用角色码解析站内信收件人；目录不可用或该角色下无人时返回空。
func (s *Service) roleRecipients(ctx context.Context, tenantID, roleCode string) []string {
	if s.Personnel == nil || strings.TrimSpace(roleCode) == "" {
		return nil
	}
	page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{RoleCodes: []string{strings.TrimSpace(roleCode)}, Page: 1, PageSize: maximumPersonnelNameLookups})
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("resolve role recipients failed", "role_code", roleCode, "error", err)
		}
		return nil
	}
	recipients := make([]string, 0, len(page.Items))
	seen := map[string]bool{}
	for _, person := range page.Items {
		userID := strings.TrimSpace(person.UserID)
		if userID == "" || seen[userID] {
			continue
		}
		seen[userID] = true
		recipients = append(recipients, userID)
	}
	return recipients
}

// syncExistingContract 对已经存在的合同版本做幂等收尾：同步盖章状态并返回既有项目。
func syncExistingContract(ctx context.Context, repo DeliveryRepository, p platform.Principal, existing domain.Project, stampedUploaded bool) (domain.Project, error) {
	event := deliveryEvent(p, existing.ID, "", EventContractStampStatus, map[string]any{"contract_id": existing.Contract, "contract_version": existing.ContractVersion, "stamped_contract_uploaded": stampedUploaded})
	if err := repo.SyncContractStampStatus(ctx, existing, stampedUploaded, event); err != nil {
		return domain.Project{}, err
	}
	return existing, nil
}

func (s *Service) AdjustDecomposition(ctx context.Context, p platform.Principal, projectID string, input domain.DecompositionAdjustmentInput) error {
	if err := s.authorizeProject(ctx, p, "project.decomposition.manage", projectID); err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" || strings.TrimSpace(input.SupplementContractID) == "" {
		return ErrValidation
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
	for _, source := range input.Items {
		if source.SourceID == "" || source.Site == "" || source.Batch == "" || source.Category == "" {
			return ErrValidation
		}
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if mode != "STANDARD" && mode != "PENETRATION" {
			return ErrValidation
		}
		outcome := resolveSplitOutcome(plan, source.Category, mode, "", categoryDomain)
		// 编号由仓储层在归档旧服务项后按既有最大序号顺延，避免与归档行冲突。
		items = append(items, domain.ServiceItem{ProjectID: projectID, SourceServiceID: source.SourceID, Batch: source.Batch, Site: source.Site, Category: source.Category, Requirement: source.Requirement, System: source.System, SystemLevel: source.SystemLevel, SystemStandard: outcome.SystemStandard, TestMode: outcome.TestMode, Special: outcome.Special, Status: domain.ServiceItemStatusPendingConfirm, ConflictStatus: "UNCHECKED"})
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, "", EventDecompositionAdjusted, map[string]any{"reason": strings.TrimSpace(input.Reason), "supplement_contract_id": strings.TrimSpace(input.SupplementContractID), "service_items": items, "split_rule": splitPlanSummary(plan)}))
}

// ScanSlaNotifications 扫描一个租户的 SLA 超期/临近项并投递站内提醒。
//
// 这是"主动提醒"的唯一入口：平台此前没有任何周期任务，SLA 只能在有人打开页面查询时可见，
// 超期不会主动告诉任何人。扫描按租户执行，使用租户边界而非某个登录用户的授权
// （系统侧任务，不代替用户的权限判断）。
//
// 幂等键按「服务项 + 口径 + UTC 日期」生成：同一天重复扫描不会重复打扰，
// 跨天仍会重新提醒（超期是持续状态，每天都值得提醒一次）。
func (s *Service) ScanSlaNotifications(ctx context.Context, tenantID string, now time.Time) (int, error) {
	if s.Notifications == nil || strings.TrimSpace(tenantID) == "" {
		return 0, nil
	}
	filter := platform.ScopeFilter{TenantID: strings.TrimSpace(tenantID), AllowAll: true}
	repo, err := s.deliveryRepo()
	if err != nil {
		return 0, err
	}
	candidates, err := repo.ListSlaOverdue(ctx, filter)
	if err != nil {
		return 0, err
	}
	rules, err := s.Repo.ListRules(ctx, filter.TenantID, "sla")
	if err != nil {
		return 0, err
	}
	items, unparsable := computeSlaItems(candidates, rules, now)
	if unparsable > 0 && s.Logger != nil {
		s.Logger.Warn("sla scan skipped items with unparsable planned_end", "tenant_id", tenantID, "skipped", unparsable)
	}
	day := now.UTC().Format("2006-01-02")
	published := 0
	for _, item := range items {
		recipients := s.itemAssignees(ctx, filter.TenantID, item.ID)
		if len(recipients) == 0 {
			continue
		}
		title, content := slaNotificationText(item)
		notification := platform.NotificationEvent{
			EventID:        ulid.Make().String(),
			EventType:      "SLA_" + item.Kind,
			Scope:          platform.NotificationScopeCrossSystem,
			Priority:       slaNotificationPriority(item.Kind),
			Title:          title,
			Content:        content,
			ReferenceType:  "service_item",
			ReferenceID:    item.ID,
			Recipients:     recipients,
			OccurredAt:     now.UTC(),
			IdempotencyKey: fmt.Sprintf("sla-%s-%s-%s", item.ID, item.Kind, day),
		}
		if err := s.Notifications.Publish(ctx, notification); err != nil {
			if s.Logger != nil {
				s.Logger.Warn("publish sla notification failed", "service_item_id", item.ID, "kind", item.Kind, "error", err)
			}
			continue
		}
		published++
	}
	return published, nil
}

// slaNotificationText 生成 SLA 提醒的标题与正文；超期按高优先级，临近按普通。
func slaNotificationText(item domain.SlaOverdueItem) (string, string) {
	switch item.Kind {
	case domain.SlaKindStatusApproaching:
		return "服务项临近 SLA 时限", fmt.Sprintf("服务项 %s 停留在「%s」的剩余时间不足，请及时推进。", item.ID, item.Status)
	case domain.SlaKindStatusOverdue:
		return "服务项状态停留超期", fmt.Sprintf("服务项 %s 停留在「%s」已超过规则时限 %d 小时（超期 %d 小时）。", item.ID, item.Status, item.DeadlineHours, item.OverdueHours)
	default:
		return "服务项计划完成超期", fmt.Sprintf("服务项 %s 的计划完成时间已过 %d 小时。", item.ID, item.OverdueHours)
	}
}

// slaNotificationPriority 让超期类提醒不被淹没在普通通知里。
func slaNotificationPriority(kind string) string {
	if kind == domain.SlaKindStatusApproaching {
		return "NORMAL"
	}
	return "HIGH"
}

// verifyExpectedVersion 在写入前校验客户端声明的服务项版本。
//
// 这是"先能检测、能提示"的那一半：实际写入已在事务内加行锁并做条件更新，不会产生脏写；
// 但客户端此前拿不到任何版本信号，两人同时改同一对象时后者会静默覆盖前者的意图。
// 期望版本为 0 表示调用方未声明版本（内部调用或旧客户端），跳过校验以保持兼容。
func (s *Service) verifyExpectedVersion(ctx context.Context, p platform.Principal, permission, itemID string, expected uint64) error {
	if expected == 0 {
		return nil
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Version != expected {
		return ErrConflict
	}
	return nil
}

func (s *Service) AssignTeam(ctx context.Context, p platform.Principal, itemID string, input domain.TeamAssignmentInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.team.assign", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.team.assign", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	if strings.TrimSpace(input.TeamLeadID) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventTeamAssigned, map[string]any{"team_lead_id": input.TeamLeadID}))
}
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
	if input.ProjectManagerID == "" || len(input.EngineerIDs) == 0 {
		return domain.ConflictCheckResult{}, ErrValidation
	}
	required := append([]string{}, input.RequiredCodes...)
	items, err := s.Repo.ListServiceItems(ctx, filter, "")
	if err != nil {
		return domain.ConflictCheckResult{}, err
	}
	found := false
	for _, item := range items {
		if item.ID == itemID {
			found = true
			if item.TestMode == "PENETRATION" {
				required = append(required, "PENETRATION_TEST")
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
	result := checkCapabilities(required, ids, caps)
	payload := map[string]any{"project_manager_id": input.ProjectManagerID, "engineer_ids": input.EngineerIDs, "required_codes": required, "conflict_status": map[bool]string{true: "PASSED", false: "CONFLICT"}[result.Passed], "conflicts": result.Conflicts}
	if err := s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventExecutionTeamAssigned, payload)); err != nil {
		return domain.ConflictCheckResult{}, err
	}
	if !result.Passed {
		s.fireConflictWarning(ctx, p, itemID, result.Conflicts)
	}
	return result, nil
}
func (s *Service) PlanImplementation(ctx context.Context, p platform.Principal, itemID string, input domain.ImplementationPlanInput) error {
	filter, err := authorizeProjectScope(p, "project.implementation.plan")
	if err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.implementation.plan", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	// 先判前置状态再判字段：两者返回的错误语义不同，前端提示也不同。
	// 前置状态不满足时用户改表单也没用，必须先去完成缺的那一步。
	if err := CheckImplementationPlanPrecondition(PlanPrecondition{
		Status: item.Status, ProjectManagerID: item.ProjectManagerID,
		ConflictStatus: item.ConflictStatus, Special: item.Special, TechReviewStatus: item.TechReviewStatus,
	}); err != nil {
		return err
	}
	start, e1 := time.Parse(time.RFC3339, input.PlannedStart)
	end, e2 := time.Parse(time.RFC3339, input.PlannedEnd)
	if e1 != nil || e2 != nil {
		return ValidationError("计划开始与计划结束必须是有效的日期时间")
	}
	if !end.After(start) {
		return ValidationError("计划结束时间必须晚于计划开始时间")
	}
	if strings.TrimSpace(input.SitePlan) == "" {
		return ValidationError("请填写现场计划")
	}
	if item.TestMode == "PENETRATION" {
		if err := validatePenetrationCompliance(input); err != nil {
			return err
		}
	}
	personnel, err := s.resolvePlanPersonnel(ctx, repo, p.TenantID, input.Personnel, start, end)
	if err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventImplementationPlanned, map[string]any{
		"planned_start":         input.PlannedStart,
		"planned_end":           input.PlannedEnd,
		"site_plan":             input.SitePlan,
		"penetration_test_plan": input.PenetrationTestPlan,
		"auth_doc_no":           strings.TrimSpace(input.AuthDocNo),
		"auth_start":            strings.TrimSpace(input.AuthStart),
		"auth_end":              strings.TrimSpace(input.AuthEnd),
		"auth_scope":            strings.TrimSpace(input.AuthScope),
		"test_scope":            strings.TrimSpace(input.TestScope),
		"test_window":           strings.TrimSpace(input.TestWindow),
		"emergency_contact":     strings.TrimSpace(input.EmergencyContact),
		"rollback_plan":         strings.TrimSpace(input.RollbackPlan),
		"personnel":             personnel,
	}))
}

// planResourceTypes 是人员/设备清单允许的资源类型；两者分别对应能力档案的两类记录。
var planResourceTypes = map[string]string{"PERSON": "人员", "EQUIPMENT": "设备"}

// resolvePlanPersonnel 把实施计划提交的人员行解析成带快照的清单行。
// 规则：至少一名人员；每一行都必须命中本租户有效的能力档案且类型为人员；
// 同一资源不得重复；使用时段必须落在计划起止内，且资质有效期要覆盖使用时段。
func (s *Service) resolvePlanPersonnel(ctx context.Context, repo DeliveryRepository, tenantID string, inputs []domain.PlanResourceInput, planStart, planEnd time.Time) ([]domain.PlanResource, error) {
	if len(inputs) == 0 {
		return nil, ValidationError("请至少添加一名实施人员")
	}
	known, err := repo.ListCapabilities(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	byResourceID := make(map[string]domain.Capability, len(known))
	for _, capability := range known {
		byResourceID[capability.ResourceID] = capability
	}
	resources := make([]domain.PlanResource, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		line := fmt.Sprintf("第 %d 行", index+1)
		if strings.EqualFold(strings.TrimSpace(input.ResourceType), "EQUIPMENT") {
			return nil, ValidationError(line + "：设备清单在「实施准备」中维护，实施计划只提交人员")
		}
		row, err := s.buildPlanResource(byResourceID, input, "PERSON", planStart, planEnd, seen)
		if err != nil {
			return nil, ValidationError(line + "：" + err.Error())
		}
		resources = append(resources, row)
	}
	return resources, nil
}

// buildPlanResource 把一行清单输入解析成带快照的资源行：校验类型、命中有效能力档案、
// 去重、使用时段落在计划内，并要求资质/检定有效期覆盖使用时段。
func (s *Service) buildPlanResource(byResourceID map[string]domain.Capability, input domain.PlanResourceInput, wantType string, planStart, planEnd time.Time, seen map[string]struct{}) (domain.PlanResource, error) {
	resourceType := strings.ToUpper(strings.TrimSpace(input.ResourceType))
	label, ok := planResourceTypes[resourceType]
	if !ok {
		return domain.PlanResource{}, errors.New("资源类型必须是人员或设备")
	}
	if resourceType != wantType {
		return domain.PlanResource{}, errors.New("资源类型与当前阶段不匹配")
	}
	resourceID := strings.TrimSpace(input.ResourceID)
	if resourceID == "" {
		return domain.PlanResource{}, errors.New("请选择" + label)
	}
	dedupeKey := resourceType + "\x00" + resourceID
	if _, exists := seen[dedupeKey]; exists {
		return domain.PlanResource{}, errors.New("同一" + label + "不能重复添加")
	}
	seen[dedupeKey] = struct{}{}
	capability, exists := byResourceID[resourceID]
	if !exists || capability.ResourceType != resourceType || capability.Status != "ACTIVE" {
		return domain.PlanResource{}, errors.New("该" + label + "不在有效的能力档案中，请先在「资质与能力」中维护")
	}
	start, end, err := planResourceWindow(input, planStart, planEnd)
	if err != nil {
		return domain.PlanResource{}, err
	}
	validUntil := ""
	if !capability.ValidUntil.IsZero() {
		// 使用时段以日期表达：有效期覆盖到当天即算覆盖，不因时分量产生边界误判。
		if dayOf(capability.ValidUntil).Before(dayOf(end)) {
			return domain.PlanResource{}, fmt.Errorf("%s「%s」的有效期至 %s，不覆盖使用时段截止日 %s",
				label, capability.ResourceName, capability.ValidUntil.Format("2006-01-02"), end.Format("2006-01-02"))
		}
		validUntil = capability.ValidUntil.Format("2006-01-02")
	}
	row := domain.PlanResource{
		ResourceType: resourceType, ResourceID: capability.ResourceID, ResourceName: capability.ResourceName,
		Codes: append([]string{}, capability.Codes...), ValidUntil: validUntil, Note: strings.TrimSpace(input.Note),
	}
	if !start.IsZero() {
		row.WindowStart = start.Format("2006-01-02")
		row.WindowEnd = end.Format("2006-01-02")
	}
	return row, nil
}

// dayOf 把时间截断到 UTC 日期：计划窗口与资质有效期都以"天"为业务粒度，
// 直接比较时间戳会把同一天判成越界或超期。
func dayOf(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

// planResourceWindow 解析行级使用时段：两端要么都留空（表示全程），要么都填写且不早于计划开始、
// 不晚于计划结束；返回值同时作为有效期校验的截止时间。
func planResourceWindow(input domain.PlanResourceInput, planStart, planEnd time.Time) (time.Time, time.Time, error) {
	rawStart, rawEnd := strings.TrimSpace(input.WindowStart), strings.TrimSpace(input.WindowEnd)
	if rawStart == "" && rawEnd == "" {
		return time.Time{}, planEnd, nil
	}
	if rawStart == "" || rawEnd == "" {
		return time.Time{}, time.Time{}, errors.New("使用时段需要同时填写开始与结束")
	}
	start, err := time.Parse("2006-01-02", rawStart)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("使用时段开始必须是 YYYY-MM-DD")
	}
	end, err := time.Parse("2006-01-02", rawEnd)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("使用时段结束必须是 YYYY-MM-DD")
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, errors.New("使用时段结束不能早于开始")
	}
	if dayOf(start).Before(dayOf(planStart)) || dayOf(end).After(dayOf(planEnd)) {
		return time.Time{}, time.Time{}, fmt.Errorf("使用时段必须落在计划起止 %s ~ %s 之内",
			planStart.Format("2006-01-02"), planEnd.Format("2006-01-02"))
	}
	return start, end, nil
}

// PlanPrecondition 是"发布实施计划"所需的最小服务项状态快照。
// 服务层用领域对象填充它，仓储层在行锁内用记录填充它，保证两处判定同一套规则。
type PlanPrecondition struct {
	Status           string
	ProjectManagerID string
	ConflictStatus   string
	Special          string
	TechReviewStatus string
}

// CheckImplementationPlanPrecondition 判定服务项是否已走到"发布实施计划"这一步。
// 应用层与仓储层共用它，避免两处守卫各自演化；同时把模糊的 422 变成可执行的指引。
func CheckImplementationPlanPrecondition(item PlanPrecondition) error {
	switch strings.TrimSpace(item.Status) {
	case "待分配":
	default:
		return PreconditionError(fmt.Sprintf("服务项当前状态为「%s」，不能发布实施计划。", strings.TrimSpace(item.Status)))
	}
	if strings.TrimSpace(item.ProjectManagerID) == "" {
		return PreconditionError("请先在「任务分配」中指派项目经理。")
	}
	if item.ConflictStatus != "PASSED" {
		return PreconditionError(fmt.Sprintf("请先在「任务分配」中完成能力校验（当前状态：%s）。", conflictStatusLabel(item.ConflictStatus)))
	}
	if item.Special == "是" && item.TechReviewStatus != "APPROVED" {
		return PreconditionError("该服务项为特殊方法，需先通过技术总监复核后才能发布实施计划。")
	}
	return nil
}

// conflictStatusLabel 把排期与能力校验状态翻译成用户能理解的说明。
func conflictStatusLabel(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PASSED":
		return "校验通过"
	case "CONFLICT":
		return "存在冲突"
	case "", "UNCHECKED":
		return "尚未校验"
	default:
		return status
	}
}

// validatePenetrationCompliance 渗透测试专项合规要素：必须提供授权书编号、授权范围、
// 计划测试范围、测试时间窗、应急联系人与回滚方案，且授权有效期内才能发布计划。
// 逐项指出缺失字段，避免用户只看到笼统的"请求参数不合法"。
func validatePenetrationCompliance(input domain.ImplementationPlanInput) error {
	required := []struct {
		value string
		label string
	}{
		{input.PenetrationTestPlan, "渗透测试专项计划"},
		{input.AuthDocNo, "授权书编号"},
		{input.AuthScope, "授权范围"},
		{input.TestScope, "计划测试范围"},
		{input.TestWindow, "测试时间窗"},
		{input.EmergencyContact, "应急联系人"},
		{input.RollbackPlan, "回滚方案"},
	}
	missing := make([]string, 0, len(required))
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.label)
		}
	}
	if len(missing) > 0 {
		return ValidationError("请补充渗透测试专项合规要素：" + strings.Join(missing, "、"))
	}
	authStart, e1 := time.Parse(time.RFC3339, input.AuthStart)
	authEnd, e2 := time.Parse(time.RFC3339, input.AuthEnd)
	if e1 != nil || e2 != nil {
		return ValidationError("授权生效与授权截止必须是有效的日期时间")
	}
	if !authEnd.After(authStart) {
		return ValidationError("授权截止时间必须晚于授权生效时间")
	}
	return nil
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
func (s *Service) UpdateReportStatus(ctx context.Context, p platform.Principal, itemID string, input domain.ReportStatusInput) error {
	// 报告推进拆成两级职责：编制/审核/签发属于报告编制（project.report.manage），
	// 归档会把项目推向"已完成"，属于独立治理动作（project.report.archive）。
	// 现场执行角色不再顺带拥有归档权。
	phase := strings.ToUpper(strings.TrimSpace(input.Phase))
	if err := s.authorizeServiceItem(ctx, p, reportPhasePermission(phase), itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, reportPhasePermission(phase), itemID, input.ExpectedVersion); err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventReportStatusUpdated, map[string]any{"phase": phase}))
}

// reportPhasePermission 返回推进到目标报告阶段所需的权限码。
// 归档（ARCHIVED）会把项目推向"已完成"，属于独立治理动作；编制/审核/签发属于报告编制。
func reportPhasePermission(phase string) string {
	if strings.EqualFold(strings.TrimSpace(phase), "ARCHIVED") {
		return "project.report.archive"
	}
	return "project.report.manage"
}

func (s *Service) StartPreparation(ctx context.Context, p platform.Principal, itemID string, input domain.PreparationInput) error {
	filter, err := authorizeProjectScope(p, "project.implementation.plan")
	if err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.implementation.plan", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	if strings.TrimSpace(input.TravelRequestID) == "" {
		return ErrValidation
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	// 设备清单在实施准备阶段确定：以计划的计划起止为占用区间边界，占用重叠直接拒绝，
	// 避免同一台设备被两个服务项在同一时间段内同时占用。
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	planStart, planEnd, err := planWindowOf(item)
	if err != nil {
		return err
	}
	equipment, err := s.resolvePreparationEquipment(ctx, repo, p.TenantID, itemID, input.Equipment, planStart, planEnd)
	if err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventPreparationStarted, map[string]any{"travel_request_id": input.TravelRequestID, "notes": input.Notes, "equipment": equipment}))
}

// ReturnEquipment 把某台设备从服务项的实施准备清单中归还：清单行保留（保留借出历史），
// 但标记归还时间后不再占用设备，也不再算「不在公司」。设备维护人员与项目经理都可发起归还，
// 因为设备可能由现场提前寄回。
func (s *Service) ReturnEquipment(ctx context.Context, p platform.Principal, itemID, resourceID string) error {
	filter, err := authorizeProjectScope(p, "project.implementation.plan")
	if err != nil {
		return err
	}
	if strings.TrimSpace(resourceID) == "" {
		return ErrValidation
	}
	if _, err := s.Repo.GetServiceItem(ctx, filter, itemID); err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventEquipmentReturned, map[string]any{"resource_id": strings.TrimSpace(resourceID)}))
}

// ListEquipmentReservations 返回与该项目计划窗口重叠的其他服务项设备占用，
// 供「实施准备」的选择器把已占用设备置灰并解释占用方。计划尚未发布时返回全部未过滤占用。
func (s *Service) ListEquipmentReservations(ctx context.Context, p platform.Principal, itemID string) ([]domain.EquipmentReservation, error) {
	filter, err := authorizeProjectScope(p, "project.implementation.plan")
	if err != nil {
		return nil, err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return nil, err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return nil, err
	}
	reservations, err := repo.ListEquipmentReservations(ctx, p.TenantID, itemID)
	if err != nil {
		return nil, err
	}
	planStart, planEnd, windowErr := planWindowOf(item)
	if windowErr != nil {
		return reservations, nil
	}
	overlapping := make([]domain.EquipmentReservation, 0, len(reservations))
	for _, reservation := range reservations {
		start, startErr := time.Parse("2006-01-02", reservation.WindowStart)
		end, endErr := time.Parse("2006-01-02", reservation.WindowEnd)
		if startErr != nil || endErr != nil {
			continue
		}
		if planStart.Before(end) && start.Before(planEnd) {
			overlapping = append(overlapping, reservation)
		}
	}
	// 与项目读路径同一脱敏口径：该响应会带出 customer。
	return s.applyFieldPermissionsToReservations(ctx, p, overlapping)
}

// planWindowOf 取服务项已发布实施计划的起止；没有计划时无法确定设备占用区间。
func planWindowOf(item domain.ServiceItem) (time.Time, time.Time, error) {
	start, startErr := time.Parse(time.RFC3339, strings.TrimSpace(item.PlannedStart))
	end, endErr := time.Parse(time.RFC3339, strings.TrimSpace(item.PlannedEnd))
	if startErr != nil || endErr != nil || !end.After(start) {
		return time.Time{}, time.Time{}, PreconditionError("请先发布实施计划，再进行实施准备")
	}
	return start, end, nil
}

// resolvePreparationEquipment 解析实施准备提交的设备清单：设备必须命中有效能力档案，
// 使用时段落在计划内且被检定有效期覆盖，并且在该时段内没有被其他服务项占用。
func (s *Service) resolvePreparationEquipment(ctx context.Context, repo DeliveryRepository, tenantID, serviceItemID string, inputs []domain.PlanResourceInput, planStart, planEnd time.Time) ([]domain.PlanResource, error) {
	if len(inputs) == 0 {
		return nil, ValidationError("请至少选择一台实施设备")
	}
	known, err := repo.ListCapabilities(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	byResourceID := make(map[string]domain.Capability, len(known))
	for _, capability := range known {
		byResourceID[capability.ResourceID] = capability
	}
	reserved, err := repo.ListEquipmentReservations(ctx, tenantID, serviceItemID)
	if err != nil {
		return nil, err
	}
	resources := make([]domain.PlanResource, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		line := fmt.Sprintf("第 %d 行", index+1)
		row, err := s.buildPlanResource(byResourceID, input, "EQUIPMENT", planStart, planEnd, seen)
		if err != nil {
			return nil, ValidationError(line + "：" + err.Error())
		}
		// 行内时段留空表示全程，占用区间即计划起止。
		windowStart, windowEnd := dayOf(planStart), dayOf(planEnd)
		if row.WindowStart != "" {
			windowStart, _ = time.Parse("2006-01-02", row.WindowStart)
			windowEnd, _ = time.Parse("2006-01-02", row.WindowEnd)
		}
		if capability, exists := byResourceID[row.ResourceID]; exists && capability.UsageScope == domain.EquipmentUsageCompanyOnly {
			return nil, ConflictError(fmt.Sprintf("设备「%s」仅在公司使用，不可借出", row.ResourceName))
		}
		if inUse, borrowed := equipmentInUseAt(reserved, time.Now().UTC())[row.ResourceID]; borrowed {
			return nil, ConflictError(fmt.Sprintf("设备「%s」当前不在公司（%s 借出中 %s ~ %s）", row.ResourceName,
				firstNonEmpty(inUse.ProjectID, inUse.ServiceItemID), inUse.WindowStart, inUse.WindowEnd))
		}
		if conflicts := equipmentUsageConflicts(row, windowStart, windowEnd, reserved); len(conflicts) > 0 {
			return nil, ConflictError(fmt.Sprintf("设备「%s」%s 已被占用：%s", row.ResourceName, windowLabel(row, planStart, planEnd), strings.Join(conflicts, "；")))
		}
		resources = append(resources, row)
	}
	return resources, nil
}

// equipmentUsageConflicts 返回与该设备行使用时段重叠的其他服务项占用描述。
// 区间按左闭右开比较：结束日当天不算占用下一天。
// equipmentUsageConflicts 返回与目标时段重叠的其他设备占用。
//
// 时段口径：两端都含当日，与资质有效期保持同一种日期语义。
// 因此 [1 日..5 日] 与 [5 日..10 日] 在 5 日当天重叠，算冲突（此前用半开区间会漏判）。
// 占用记录的使用时段解析失败时按「占用」处理：无法证明设备空闲时不得放行，
// 同时把数据异常显式报出来，避免整段占用检查静默失效。
func equipmentUsageConflicts(row domain.PlanResource, windowStart, windowEnd time.Time, reserved []domain.EquipmentReservation) []string {
	conflicts := []string{}
	for _, reservation := range reserved {
		if reservation.ResourceID != row.ResourceID {
			continue
		}
		otherStart, startErr := time.Parse("2006-01-02", reservation.WindowStart)
		otherEnd, endErr := time.Parse("2006-01-02", reservation.WindowEnd)
		if startErr != nil || endErr != nil {
			conflicts = append(conflicts, fmt.Sprintf("%s（%s）的占用时段数据异常（%s ~ %s），请先修正该服务项的设备时段",
				firstNonEmpty(reservation.ProjectID, reservation.ServiceItemID), reservation.ServiceItemID,
				reservation.WindowStart, reservation.WindowEnd))
			continue
		}
		// 闭区间重叠：任一端晚于对方另一端才算不重叠。
		if windowStart.After(otherEnd) || otherStart.After(windowEnd) {
			continue
		}
		holder := reservation.ProjectID
		if holder == "" {
			holder = reservation.ServiceItemID
		}
		conflicts = append(conflicts, fmt.Sprintf("%s（%s）在 %s ~ %s 占用",
			holder, reservation.ServiceItemID, reservation.WindowStart, reservation.WindowEnd))
	}
	return conflicts
}

// windowLabel 用人和日期描述设备行的占用区间，让冲突提示可直接照着核对。
func windowLabel(row domain.PlanResource, planStart, planEnd time.Time) string {
	if row.WindowStart == "" {
		return fmt.Sprintf("%s ~ %s（全程）", planStart.Format("2006-01-02"), planEnd.Format("2006-01-02"))
	}
	return row.WindowStart + " ~ " + row.WindowEnd
}

func (s *Service) SubmitFieldRecord(ctx context.Context, p platform.Principal, itemID string, input domain.FieldRecordInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.execute", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.field.execute", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	if strings.TrimSpace(input.RawData) == "" || strings.TrimSpace(input.Environment) == "" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldRecordSubmitted, map[string]any{"raw_data": input.RawData, "environment": input.Environment, "evidence_urls": input.EvidenceURLs}))
}

func (s *Service) ReportDeviation(ctx context.Context, p platform.Principal, itemID string, input domain.DeviationInput) (string, error) {
	if err := s.authorizeServiceItem(ctx, p, "project.deviation.report", itemID); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Description) == "" {
		return "", ErrValidation
	}
	id := "DV-" + ulid.Make().String()
	return id, s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventDeviationReported, map[string]any{"deviation_id": id, "description": input.Description, "severity": input.Severity, "evidence_url": input.EvidenceURL, "decision": "PENDING"}))
}

func (s *Service) ReviewDeviation(ctx context.Context, p platform.Principal, deviationID string, input domain.DeviationReviewInput) error {
	filter, err := authorizeProjectScope(p, "project.deviation.review")
	if err != nil {
		return err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	projectID, itemID, err := repo.FindProjectForDeviation(ctx, filter, deviationID)
	if err != nil {
		return err
	}
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "RELEASE" && decision != "TERMINATE" && decision != "RETEST" {
		return ErrValidation
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, itemID, EventDeviationReviewed, map[string]any{"deviation_id": deviationID, "decision": decision, "comment": input.Comment}))
}

// CompleteServiceItemField 确认单个服务项现场实施完成：服务项进入「现场实施完成」
// 并开启报告编制；全部服务项完成后项目状态由派生规则自动推进，不再有项目级一刀切
// 完成入口——多服务项项目里各服务项按自己的节奏收口。
func (s *Service) CompleteServiceItemField(ctx context.Context, p platform.Principal, itemID string) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.complete", itemID); err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldCompleted, map[string]any{"confirmed_by": p.UserID}))
}

func (s *Service) ListDeliveryEvents(ctx context.Context, p platform.Principal, projectID string) ([]domain.DeliveryEvent, error) {
	filter, scopeErr := authorizeProjectScope(p, "project.read")
	if scopeErr != nil {
		return nil, scopeErr
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return nil, e
	}
	events, err := repo.ListDeliveryEvents(ctx, filter, projectID)
	if err != nil {
		return nil, err
	}
	// 事件流是审计视图，必须与项目/服务项读路径共用同一套字段级脱敏：
	// payload 里的指派快照与拆解快照否则会绕开隐藏配置。
	return s.applyFieldPermissionsToEvents(ctx, p, events)
}

// CapabilityImportResult 汇总一次能力记录导出的导入结果。
type CapabilityImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

func validateCapability(item domain.Capability) error {
	if (item.ResourceType != "PERSON" && item.ResourceType != "EQUIPMENT") || strings.TrimSpace(item.ResourceID) == "" || strings.TrimSpace(item.ResourceName) == "" || len(item.Codes) == 0 {
		return ErrValidation
	}
	return nil
}

// resourceIDPrefix 区分人员与设备的资源编号前缀，避免两类编号混用。
// existingUsageScope 在既有能力目录里查同编号设备的使用范围；查不到时返回默认的可借出，
// 保证新增设备与历史数据都落在同一个默认值上。
func existingUsageScope(items []domain.Capability, resourceID string) string {
	for _, item := range items {
		if item.ResourceID == resourceID && strings.TrimSpace(item.UsageScope) != "" {
			return item.UsageScope
		}
	}
	return domain.EquipmentUsageAny
}

func resourceIDPrefix(resourceType string) string {
	if resourceType == "EQUIPMENT" {
		return "EQ-"
	}
	return "P-"
}

// nextResourceID 依据现有记录为人员/设备生成下一个可用编号（P-0001 / EQ-0001）。
// 已占用的编号会被跳过，兼容历史手工编号与同批次导入。
func nextResourceID(existing []domain.Capability, resourceType string) string {
	prefix := resourceIDPrefix(resourceType)
	used := map[string]bool{}
	maxSequence := 0
	for _, item := range existing {
		if item.ResourceType != resourceType {
			continue
		}
		used[item.ResourceID] = true
		if !strings.HasPrefix(item.ResourceID, prefix) {
			continue
		}
		if sequence, err := strconv.Atoi(strings.TrimPrefix(item.ResourceID, prefix)); err == nil && sequence > maxSequence {
			maxSequence = sequence
		}
	}
	for {
		maxSequence++
		candidate := fmt.Sprintf("%s%04d", prefix, maxSequence)
		if !used[candidate] {
			return candidate
		}
	}
}

// assignResourceID 在调用方未提供编号时自动生成，保证人员与设备编号各自独立且可读。
func assignResourceID(existing []domain.Capability, item *domain.Capability) {
	if strings.TrimSpace(item.ResourceID) != "" {
		return
	}
	if item.ResourceType != "PERSON" && item.ResourceType != "EQUIPMENT" {
		return
	}
	item.ResourceID = nextResourceID(existing, item.ResourceType)
}

func (s *Service) UpsertCapability(ctx context.Context, p platform.Principal, item domain.Capability) (domain.Capability, error) {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return item, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return item, e
	}
	item.TenantID = p.TenantID
	if strings.TrimSpace(item.ResourceID) == "" {
		existing, err := repo.ListCapabilities(ctx, p.TenantID, item.ResourceType)
		if err != nil {
			return item, err
		}
		assignResourceID(existing, &item)
	}
	if err := validateCapability(item); err != nil {
		return item, err
	}
	item.Status = firstNonEmpty(item.Status, "ACTIVE")
	// 使用范围只对设备有意义：调用方没有提交时必须沿用该设备的既有设置，
	// 否则从"资质与能力管理"改一个名称就会把「仅在公司使用」静默改回可借出。
	if item.ResourceType == "EQUIPMENT" && strings.TrimSpace(item.UsageScope) == "" {
		existing, err := repo.ListCapabilities(ctx, p.TenantID, "EQUIPMENT")
		if err != nil {
			return item, err
		}
		item.UsageScope = existingUsageScope(existing, item.ResourceID)
	}
	return repo.UpsertCapability(ctx, item, p.UserID)
}

// ImportCapabilities 批量写入能力记录（人员资质或设备能力）。逐行校验并独立写入，
// 单行失败只累计跳过原因，不影响其余行，避免一条脏数据阻塞整批导入。
func (s *Service) ImportCapabilities(ctx context.Context, p platform.Principal, rows []domain.Capability) (CapabilityImportResult, error) {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return CapabilityImportResult{}, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return CapabilityImportResult{}, e
	}
	result := CapabilityImportResult{}
	// 导入同样支持编号留空：按类型顺延生成，且本批次内逐行消耗，避免整批手工编号。
	known, err := repo.ListCapabilities(ctx, p.TenantID, "")
	if err != nil {
		return CapabilityImportResult{}, err
	}
	for i := range rows {
		line := fmt.Sprintf("数据行 %d", i+1)
		assignResourceID(known, &rows[i])
		if err := validateCapability(rows[i]); err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, line+": 资源类型、编号、名称或能力码不完整")
			continue
		}
		known = append(known, rows[i])
		rows[i].TenantID = p.TenantID
		rows[i].Status = firstNonEmpty(rows[i].Status, "ACTIVE")
		// CSV 不携带使用范围；导入既有设备时必须保留原设置，不能被批量改回可借出。
		rows[i].UsageScope = firstNonEmpty(rows[i].UsageScope, existingUsageScope(known, rows[i].ResourceID))
		if _, err := repo.UpsertCapability(ctx, rows[i], p.UserID); err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, line+": "+err.Error())
			continue
		}
		result.Imported++
	}
	return result, nil
}
func (s *Service) ListCapabilities(ctx context.Context, p platform.Principal, typ string) ([]domain.Capability, error) {
	if err := requireDirectoryRead(p, "project.read", "project.resource.read"); err != nil {
		return nil, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return nil, e
	}
	return repo.ListCapabilities(ctx, p.TenantID, typ)
}

// PersonnelIdentitySyncResult 汇总一次人员资质身份复核的结果。
type PersonnelIdentitySyncResult struct {
	// Total 是本次扫描到的人员档案数（含未关联平台账号的历史档案）。
	Total int `json:"total"`
	// Active / Missing / Unlinked 是复核后的分布；Unverified 是目录本次未能回答、
	// 因而保持原状的档案数——平台抖动绝不能被写成"离职"。
	Active     int    `json:"active"`
	Missing    int    `json:"missing"`
	Unlinked   int    `json:"unlinked"`
	Unverified int    `json:"unverified"`
	CheckedAt  string `json:"checked_at"`
}

// SyncPersonnelIdentities 把人员资质档案回基础平台负责人目录复核一次。
// 资质与能力在项目管理系统内维护，但"这个人是否真实存在（在职）"只能由基础平台回答：
// 目录中查得到的标记 ACTIVE，查无此人的标记 MISSING，未关联 user_id 的历史档案保持
// UNLINKED。目录报错的 ID 记为 Unverified 且不改写，避免把平台故障误判成离职。
func (s *Service) SyncPersonnelIdentities(ctx context.Context, p platform.Principal) (PersonnelIdentitySyncResult, error) {
	result := PersonnelIdentitySyncResult{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return result, err
	}
	if s.Personnel == nil {
		return result, ErrPersonnelUnavailable
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return result, err
	}
	items, err := repo.ListCapabilities(ctx, p.TenantID, "PERSON")
	if err != nil {
		return result, err
	}
	result.Total = len(items)
	linked := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		userID := strings.TrimSpace(item.UserID)
		if userID == "" {
			continue
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		linked = append(linked, userID)
	}
	if len(linked) == 0 {
		result.Unlinked = len(items)
		return result, nil
	}
	statuses := make(map[string]string, len(linked))
	for start := 0; start < len(linked); start += maximumPersonnelNameLookups {
		end := start + maximumPersonnelNameLookups
		if end > len(linked) {
			end = len(linked)
		}
		lookup := s.lookupPersonnel(ctx, linked[start:end])
		// 这里只负责把目录给出的结论整理成 statuses；Unverified 在下面的按档案
		// 计数里统一统计，避免同一份档案被记两次。
		for _, userID := range linked[start:end] {
			if _, failed := lookup.failures[userID]; failed {
				continue
			}
			if _, exists := lookup.names[userID]; exists {
				statuses[userID] = domain.IdentityStatusActive
				continue
			}
			statuses[userID] = domain.IdentityStatusMissing
		}
	}
	// 按"档案"计数，保证 Total = Active + Missing + Unlinked + Unverified。
	for _, item := range items {
		switch {
		case strings.TrimSpace(item.UserID) == "":
			result.Unlinked++
		default:
			status, resolved := statuses[strings.TrimSpace(item.UserID)]
			switch {
			case !resolved:
				result.Unverified++
			case status == domain.IdentityStatusActive:
				result.Active++
			default:
				result.Missing++
			}
		}
	}
	if err := repo.UpdateCapabilityIdentities(ctx, p.TenantID, statuses, time.Now().UTC()); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) ListEquipment(ctx context.Context, p platform.Principal) ([]domain.Capability, error) {
	if err := requireDirectoryRead(p, "project.read", "project.device.read"); err != nil {
		return nil, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return nil, e
	}
	items, err := repo.ListCapabilities(ctx, p.TenantID, "EQUIPMENT")
	if err != nil {
		return nil, err
	}
	// 「在公司 / 不在公司」不落库：当前时间落在某条未归还的占用时段内即为借出中。
	reservations, err := repo.ListEquipmentReservations(ctx, p.TenantID, "")
	if err != nil {
		return nil, err
	}
	active := equipmentInUseAt(reservations, time.Now().UTC())
	for index := range items {
		items[index].Presence = domain.EquipmentPresenceInCompany
		if reservation, borrowed := active[items[index].ResourceID]; borrowed {
			items[index].Presence = domain.EquipmentPresenceOutOfCompany
			items[index].BorrowedBy = firstNonEmpty(reservation.ProjectID, reservation.ServiceItemID)
			items[index].BorrowedServiceItemID = reservation.ServiceItemID
			items[index].BorrowedWindow = reservation.WindowStart + " ~ " + reservation.WindowEnd
		}
	}
	return items, nil
}

// equipmentInUseAt 找出当前处于占用中的设备，并把占用记录按设备编号索引，供在位状态与冲突提示复用。
func equipmentInUseAt(reservations []domain.EquipmentReservation, now time.Time) map[string]domain.EquipmentReservation {
	today := now.Format("2006-01-02")
	active := map[string]domain.EquipmentReservation{}
	for _, reservation := range reservations {
		if reservation.WindowStart <= today && today <= reservation.WindowEnd {
			if _, exists := active[reservation.ResourceID]; !exists {
				active[reservation.ResourceID] = reservation
			}
		}
	}
	return active
}

func (s *Service) UpsertEquipment(ctx context.Context, p platform.Principal, item domain.Capability) (domain.Capability, error) {
	if err := requireApplicationAuthorization(p, "project.device.manage"); err != nil {
		return item, err
	}
	if item.ResourceType != "EQUIPMENT" || strings.TrimSpace(item.ResourceID) == "" || strings.TrimSpace(item.ResourceName) == "" || len(item.Codes) == 0 {
		return item, ErrValidation
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return item, e
	}
	item.TenantID = p.TenantID
	// 未提交使用范围时沿用既有设置，避免"编辑设备"顺手把「仅在公司使用」改回可借出。
	if strings.TrimSpace(item.UsageScope) == "" {
		existing, err := repo.ListCapabilities(ctx, p.TenantID, "EQUIPMENT")
		if err != nil {
			return item, err
		}
		item.UsageScope = existingUsageScope(existing, item.ResourceID)
	}
	item.UsageScope = strings.ToUpper(strings.TrimSpace(firstNonEmpty(item.UsageScope, domain.EquipmentUsageAny)))
	if item.UsageScope != domain.EquipmentUsageAny && item.UsageScope != domain.EquipmentUsageCompanyOnly {
		return item, ValidationError("使用范围只能是「可借出」或「仅在公司使用」")
	}
	item.Status = firstNonEmpty(item.Status, "ACTIVE")
	return repo.UpsertCapability(ctx, item, p.UserID)
}
func (s *Service) applyEvent(ctx context.Context, event domain.DeliveryEvent) error {
	repo, e := s.deliveryRepo()
	if e != nil {
		return e
	}
	if err := repo.ApplyDeliveryEvent(ctx, event); err != nil {
		return err
	}
	s.notifyAssigned(ctx, event)
	s.fireAutomations(ctx, event)
	return nil
}

// itemAssignees 读取服务项当前的被指派人（团队负责人、项目经理、实施工程师）。
// 供通知这类系统侧派生副作用使用：只按租户边界读取，不叠加调用者授权
// （调用者已经通过各自的操作权限鉴权，这里只是为了把提醒发给"需要行动的人"）。
func (s *Service) itemAssignees(ctx context.Context, tenantID, itemID string) []string {
	if strings.TrimSpace(itemID) == "" {
		return nil
	}
	item, err := s.Repo.GetServiceItem(ctx, platform.ScopeFilter{TenantID: tenantID, AllowAll: true}, itemID)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("resolve notification assignees failed", "service_item_id", itemID, "error", err)
		}
		return nil
	}
	recipients := make([]string, 0, 3)
	if teamLead := strings.TrimSpace(item.TeamLeadID); teamLead != "" {
		recipients = append(recipients, teamLead)
	}
	if manager := strings.TrimSpace(item.ProjectManagerID); manager != "" {
		recipients = append(recipients, manager)
	}
	for _, engineer := range item.EngineerIDs {
		if trimmed := strings.TrimSpace(engineer); trimmed != "" {
			recipients = append(recipients, trimmed)
		}
	}
	return recipients
}

// assignmentNotification 描述一条"指派类"事件对应的站内提醒文案。
type assignmentNotification struct {
	Title   string
	Content string
}

// notifyAssigned 在事件成功落库后，向**被指派人**投递站内提醒。
//
// 集中在这里而不是散落到各个业务方法：所有业务节点都经过 applyEvent，通知规则只需维护一处。
// 收件人取"需要行动的人"（被指派人），而不是操作者本人——这正是提醒的意义。
// 通知是派生副作用：失败不回滚已落库的事件，但必须留下可诊断的警告，不能静默吞掉。
func (s *Service) notifyAssigned(ctx context.Context, event domain.DeliveryEvent) {
	if s.Notifications == nil || strings.TrimSpace(event.ServiceItemID) == "" {
		return
	}
	spec, ok := assignmentNotificationFor(event.Type)
	if !ok {
		return
	}
	recipients := assignmentRecipients(event)
	if len(recipients) == 0 {
		// 载荷里没有收件人时回到服务项的当前被指派人：通知是系统侧的派生副作用，
		// 只按租户边界读取（调用者已经通过各自的操作鉴权），不叠加用户授权。
		recipients = s.itemAssignees(ctx, event.TenantID, event.ServiceItemID)
	}
	if len(recipients) == 0 {
		return
	}
	notification := platform.NotificationEvent{
		EventID:   ulid.Make().String(),
		EventType: event.Type,
		// 必须是平台白名单取值；写错会让整条通知被判 400。
		Scope:          platform.NotificationScopeCrossSystem,
		Priority:       "NORMAL",
		Title:          spec.Title,
		Content:        spec.Content,
		ReferenceType:  "service_item",
		ReferenceID:    event.ServiceItemID,
		Recipients:     recipients,
		OccurredAt:     time.Now().UTC(),
		IdempotencyKey: event.ID + "-assigned",
	}
	if err := s.Notifications.Publish(ctx, notification); err != nil && s.Logger != nil {
		s.Logger.Warn("publish assignment notification failed",
			"event_id", event.ID, "event_type", event.Type, "error", err)
	}
}

// assignmentNotificationFor 给出指派类事件的提醒文案；非指派事件返回 false。
func assignmentNotificationFor(eventType string) (assignmentNotification, bool) {
	switch eventType {
	case EventTeamAssigned:
		return assignmentNotification{
			Title:   "已指派团队负责人",
			Content: "你被指派为服务项的团队负责人，请在「任务分配」中指派项目经理与实施工程师。",
		}, true
	case EventExecutionTeamAssigned:
		return assignmentNotification{
			Title:   "已指派项目经理与实施工程师",
			Content: "你被指派到该服务项，请在「实施计划」中确认排期并推进交付。",
		}, true
	case EventImplementationPlanned:
		return assignmentNotification{
			Title:   "实施计划已发布",
			Content: "该服务项的实施计划已发布，请按排期推进现场实施。",
		}, true
	case EventPreparationStarted:
		return assignmentNotification{
			Title:   "实施准备已发起",
			Content: "该服务项已进入实施准备，请确认设备已就位并按计划开展现场实施。",
		}, true
	case EventDeviationReported:
		return assignmentNotification{
			Title:   "有偏离待评审",
			Content: "该服务项上报了实施偏离，请及时安排评审。",
		}, true
	case EventReportStatusUpdated:
		return assignmentNotification{
			Title:   "报告阶段已推进",
			Content: "该服务项的报告阶段已推进，请按当前阶段继续处理。",
		}, true
	default:
		return assignmentNotification{}, false
	}
}

// assignmentRecipients 从事件载荷取被指派人：团队负责人、项目经理、实施工程师。
func assignmentRecipients(event domain.DeliveryEvent) []string {
	recipients := make([]string, 0, 3)
	if teamLead := payloadText(event.Payload, "team_lead_id"); teamLead != "" {
		recipients = append(recipients, teamLead)
	}
	if manager := payloadText(event.Payload, "project_manager_id"); manager != "" {
		recipients = append(recipients, manager)
	}
	recipients = append(recipients, payloadTextList(event.Payload, "engineer_ids")...)
	// 实施计划的人员清单就是需要行动的现场实施人员：只取人员行，设备行不作收件人。
	if resources, ok := event.Payload["personnel"].([]domain.PlanResource); ok {
		for _, resource := range resources {
			if !strings.EqualFold(strings.TrimSpace(resource.ResourceType), "PERSON") {
				continue
			}
			if resourceID := strings.TrimSpace(resource.ResourceID); resourceID != "" {
				recipients = append(recipients, resourceID)
			}
		}
	}
	return recipients
}

// payloadText 读取事件载荷里的字符串字段，缺失或类型不符时返回空串。
func payloadText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

// payloadTextList 读取事件载荷里的字符串数组字段，逐项去掉空白并跳过空值。
func payloadTextList(payload map[string]any, key string) []string {
	values, _ := payload[key].([]string)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// fireAutomations 在事件落库后按启用的「自动化触发」规则追加 AUTOMATION_TRIGGERED
// 派生事件，让配置表中的 trigger/target 真正参与运行时行为。未命中任何规则时不产生
// 额外事件；派生事件不再递归触发下一次自动化，也绝不因配置读取失败回滚主事件。
func (s *Service) fireAutomations(ctx context.Context, event domain.DeliveryEvent) {
	if event.Type == EventAutomationTriggered || event.Type == EventWarningTriggered {
		return
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return
	}
	rules, err := s.Repo.ListRules(ctx, event.TenantID, "automations")
	if err != nil {
		return
	}
	targets := make([]string, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled || strings.TrimSpace(rule.Trigger) != event.Type {
			continue
		}
		targets = append(targets, strings.TrimSpace(rule.Target))
	}
	if len(targets) == 0 {
		return
	}
	triggered := event
	triggered.Type = EventAutomationTriggered
	triggered.CreatedAt = time.Now().UTC().Add(time.Millisecond)
	triggered.Payload = map[string]any{"trigger": event.Type, "targets": targets}
	s.persistDerivedEvent(ctx, "automation", event, triggered, repo)
	s.notifyAutomationTargets(ctx, event, targets)
}

// notifyAutomationTargets 把自动化规则的 target 解析成平台用户并投递站内信。
// target 约定为项目系统的应用角色码（例如 technical_director / quality_manager），
// 通过负责人目录按 role_code 解析成具体人员；不引入自由文本收件人，
// 避免"配了目标却没人收到"。未开通站内信集成、目录不可用或该角色下无人时静默跳过：
// 通知是派生副作用，绝不能影响已提交的主事件。
func (s *Service) notifyAutomationTargets(ctx context.Context, event domain.DeliveryEvent, targets []string) {
	if s.Notifications == nil || s.Personnel == nil || len(targets) == 0 {
		return
	}
	recipients := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		roleCode := strings.TrimSpace(target)
		if roleCode == "" {
			continue
		}
		page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{RoleCodes: []string{roleCode}, Page: 1, PageSize: maximumPersonnelNameLookups})
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("resolve automation notification recipients failed", "role_code", roleCode, "error", err)
			}
			continue
		}
		for _, person := range page.Items {
			userID := strings.TrimSpace(person.UserID)
			if userID == "" {
				continue
			}
			if _, exists := seen[userID]; exists {
				continue
			}
			seen[userID] = struct{}{}
			recipients = append(recipients, userID)
		}
	}
	if len(recipients) == 0 {
		return
	}
	notification := platform.NotificationEvent{
		EventID:   ulid.Make().String(),
		EventType: EventAutomationTriggered,
		// 必须用平台白名单取值：此前写死 "application"，平台只接受 CROSS_SYSTEM|PLATFORM，
		// 导致自动化通知必然被判 400，且失败只记 Warn 不易察觉。
		Scope:         platform.NotificationScopeCrossSystem,
		Priority:      "NORMAL",
		Title:         "项目自动化规则触发",
		Content:       fmt.Sprintf("规则命中的事件：%s。项目 %s，服务项 %s。", event.Type, event.ProjectID, event.ServiceItemID),
		ReferenceType: "service_item",
		ReferenceID:   event.ServiceItemID,
		Recipients:    recipients,
		OccurredAt:    time.Now().UTC(),
		// 同一源事件只投递一次，平台按幂等键去重。
		IdempotencyKey: event.ID + "-automation-notification",
	}
	if err := s.Notifications.Publish(ctx, notification); err != nil && s.Logger != nil {
		s.Logger.Warn("publish automation notification failed", "event_id", event.ID, "error", err)
	}
}

// persistDerivedEvent 写入派生事件。派生事件是主事件提交后的 best-effort 副作用，
// 不能回滚主流程，但"配了规则却没生效"必须可诊断：先重试几次（连接抖动通常是瞬时的），
// 仍然失败才放弃并记录，而不是静默吞掉。
func (s *Service) persistDerivedEvent(ctx context.Context, kind string, source, event domain.DeliveryEvent, repo DeliveryRepository) {
	const attempts = 3
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if lastErr = repo.ApplyDeliveryEvent(ctx, event); lastErr == nil {
			return
		}
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
		case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			continue
		}
		break
	}
	s.logDerivedEventFailure(kind, source, lastErr)
}

// logDerivedEventFailure 记录派生事件写入失败。派生事件不回滚主事件，
// 但静默吞掉会让"规则配了却没触发"变成无法诊断的问题。
func (s *Service) logDerivedEventFailure(kind string, source domain.DeliveryEvent, err error) {
	if s.Logger == nil {
		return
	}
	s.Logger.Warn("derived delivery event was not persisted",
		"kind", kind, "source_event", source.Type, "project_id", source.ProjectID,
		"service_item_id", source.ServiceItemID, "error", err)
}

// fireConflictWarning 在任务分配/执行团队指派产生能力冲突且有启用的「冲突预警规则」时，
// 追加 WARNING_TRIGGERED 派生事件，把冲突明细纳入项目事件流。非破坏性：失败不影响主流程。
func (s *Service) fireConflictWarning(ctx context.Context, p platform.Principal, itemID string, conflicts []string) {
	rules, err := s.Repo.ListRules(ctx, p.TenantID, "warning-rules")
	if err != nil {
		return
	}
	// check_type 决定规则覆盖哪一类冲突，threshold 决定至少要几项命中才告警。
	// 两个字段都必须参与判定，否则"配了规则就无差别触发"等于配置没有意义。
	matchedRules := make([]string, 0, len(rules))
	matchedConflicts := make([]string, 0, len(conflicts))
	threshold := 0
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		hits := make([]string, 0, len(conflicts))
		for _, conflict := range conflicts {
			if warningRuleMatches(rule, conflict) {
				hits = append(hits, conflict)
			}
		}
		required := warningThreshold(rule)
		if len(hits) < required {
			continue
		}
		matchedRules = append(matchedRules, rule.Name)
		matchedConflicts = append(matchedConflicts, hits...)
		if threshold == 0 || required < threshold {
			threshold = required
		}
	}
	if len(matchedRules) == 0 {
		return
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return
	}
	source := deliveryEvent(p, "", itemID, EventWarningTriggered, nil)
	event := deliveryEvent(p, "", itemID, EventWarningTriggered, map[string]any{
		"conflicts": uniqueStrings(matchedConflicts),
		"rules":     matchedRules,
		"threshold": threshold,
	})
	s.persistDerivedEvent(ctx, "warning", source, event, repo)
}

// conflictKind 把能力校验产生的冲突描述归类，供预警规则的 check_type 匹配。
func conflictKind(conflict string) string {
	switch {
	case strings.Contains(conflict, "资质"):
		return "资质能力冲突"
	case strings.Contains(conflict, "缺少能力"):
		return "能力缺失"
	default:
		return "其他冲突"
	}
}

// warningRuleMatches 判断预警规则是否覆盖某条冲突：check_type 为空表示不限类型，
// 否则按冲突类别名或冲突原文做子串匹配（配置里写"资质能力冲突"或"资质"都能命中）。
func warningRuleMatches(rule domain.Rule, conflict string) bool {
	checkType := strings.TrimSpace(rule.CheckType)
	if checkType == "" {
		return true
	}
	return strings.Contains(conflictKind(conflict), checkType) || strings.Contains(conflict, checkType)
}

// warningThreshold 解析规则的冲突数量阈值，允许 "3" 或 "连续 3 项冲突" 这类写法；
// 空值/非法值按 1 处理，保持"配了规则至少一条冲突即告警"的直觉语义。
func warningThreshold(rule domain.Rule) int {
	raw := strings.TrimSpace(rule.Threshold)
	if raw == "" {
		return 1
	}
	digits := strings.Builder{}
	for _, symbol := range raw {
		if symbol >= '0' && symbol <= '9' {
			digits.WriteRune(symbol)
			continue
		}
		if digits.Len() > 0 {
			break
		}
	}
	if digits.Len() == 0 {
		return 1
	}
	value, err := strconv.Atoi(digits.String())
	if err != nil || value < 1 {
		return 1
	}
	return value
}

// uniqueStrings 去重并保持首次出现顺序，避免多条规则命中同一条冲突时重复上报。
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Service) authorizeProject(ctx context.Context, p platform.Principal, permission, projectID string) error {
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	_, err = s.Repo.GetProject(ctx, filter, projectID)
	return err
}

func (s *Service) authorizeServiceItem(ctx context.Context, p platform.Principal, permission, itemID string) error {
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	_, err = s.Repo.GetServiceItem(ctx, filter, itemID)
	return err
}
func deliveryEvent(p platform.Principal, projectID, itemID, typ string, payload map[string]any) domain.DeliveryEvent {
	return domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: p.TenantID, ProjectID: projectID, ServiceItemID: itemID, Type: typ, ActorUserID: p.UserID, Payload: payload, CreatedAt: time.Now().UTC()}
}
func projectID(now time.Time) string {
	return "PJ-" + now.Format("2006") + "-" + strings.ToUpper(ulid.Make().String()[20:])
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

// splitGroup 是按生效分组方案合并后的一个服务项：清单行 + 已解析的拆解口径。
type splitGroup struct {
	Item    domain.ContractService
	Outcome SplitOutcome
}

// contractServiceCategories 取出合同清单里出现过的检测类别（供覆盖规则匹配服务项数/类别）。
func contractServiceCategories(sources []domain.ContractService) []string {
	categories := make([]string, 0, len(sources))
	for _, source := range sources {
		category := strings.TrimSpace(source.Category)
		if category != "" && !slices.Contains(categories, category) {
			categories = append(categories, category)
		}
	}
	return categories
}

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
func joinUnique(existing, next string) string {
	if next == "" {
		return existing
	}
	for _, part := range strings.Split(existing, "、") {
		if part == next {
			return existing
		}
	}
	if existing == "" {
		return next
	}
	return existing + "、" + next
}
func checkCapabilities(required, resources []string, items []domain.Capability) domain.ConflictCheckResult {
	covered := map[string]bool{}
	active := map[string]bool{}
	for _, c := range items {
		active[c.ResourceID] = c.Status == "ACTIVE"
		for _, code := range c.Codes {
			if active[c.ResourceID] {
				covered[code] = true
			}
		}
	}
	conflicts := []string{}
	for _, id := range resources {
		if !active[id] {
			conflicts = append(conflicts, "资源 "+id+" 缺少有效资质/能力记录")
		}
	}
	sort.Strings(required)
	for _, code := range required {
		if !covered[code] {
			conflicts = append(conflicts, "缺少能力："+code)
		}
	}
	return domain.ConflictCheckResult{Passed: len(conflicts) == 0, Conflicts: conflicts}
}
