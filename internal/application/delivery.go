package application

import (
	"context"
	"fmt"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type DeliveryRepository interface {
	// FindProjectByContract 按稳定的合同身份（合同 ID；历史空 ID 回退合同号）查找项目。
	// 普通行版本只用于并发与审计，不参与建项唯一性。
	FindProjectByContract(context.Context, platform.ScopeFilter, string, string) (domain.Project, error)
	ActivateContract(context.Context, domain.Project, []domain.ServiceItem, domain.DeliveryEvent) error
	SyncContractStampStatus(context.Context, domain.Project, bool, domain.DeliveryEvent) error
	ApplyDeliveryEvent(context.Context, domain.DeliveryEvent) error
	ListSlaOverdue(context.Context, platform.ScopeFilter) ([]domain.SlaOverdueItem, error)
	ListDeliveryEvents(context.Context, platform.ScopeFilter, string) ([]domain.DeliveryEvent, error)
	FindProjectForDeviation(context.Context, platform.ScopeFilter, string) (string, string, error)
	UpsertCapability(context.Context, domain.Capability, string) (domain.Capability, error)
	DeleteEquipment(context.Context, string, string) error
	ListCapabilities(context.Context, string, string) ([]domain.Capability, error)
	FindCapabilities(context.Context, string, string, []string) ([]domain.Capability, error)
	ListEquipmentReservations(context.Context, string, string) ([]domain.EquipmentReservation, error)
	// UpdateCapabilityIdentities 回写人员档案的身份复核结果。
	UpdateCapabilityIdentities(context.Context, string, map[string]string, time.Time) error
}

// RevokeExecutionAssignment 让团队负责人纠正项目经理或工程师分配，但不会撤销团队负责人。
func (s *Service) RevokeExecutionAssignment(ctx context.Context, p platform.Principal, itemID string, input domain.AssignmentRevokeInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.execution.revoke", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.execution.revoke", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" {
		return ValidationError("请填写撤销执行团队分配的原因")
	}
	filter, err := authorizeProjectScope(p, "project.execution.revoke")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Status != "待分配" || strings.TrimSpace(item.TeamLeadID) == "" || strings.TrimSpace(item.ProjectManagerID) == "" {
		return PreconditionError("仅可撤销待分配服务项的已分配执行团队")
	}
	previous := append([]string{item.ProjectManagerID}, item.EngineerIDs...)
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventExecutionAssignmentRevoked, map[string]any{"reason": strings.TrimSpace(input.Reason), "expected_version": input.ExpectedVersion, "previous_project_manager_id": item.ProjectManagerID, "previous_engineer_ids": item.EngineerIDs, "revoked_user_ids": previous}))
}

func capturedTimePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

// StartFieldExecution 是“实施准备中”到“实施中”的显式业务动作。现场记录不再隐式推进
// 状态，避免用户尚未确认开始测评时，仅因保存一条记录就进入实施阶段。

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

// ImportCapabilities 批量写入人员资质记录。设备生命周期只能通过设备能力接口维护。
// 逐行校验并独立写入，
// 单行失败只累计跳过原因，不影响其余行，避免一条脏数据阻塞整批导入。
func (s *Service) ImportCapabilities(ctx context.Context, p platform.Principal, rows []domain.Capability) (CapabilityImportResult, error) {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return CapabilityImportResult{}, err
	}
	result := CapabilityImportResult{}
	for i, row := range rows {
		if _, err := s.UpsertCapability(ctx, p, row); err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("数据行 %d: %s", i+1, capabilityImportSafeError(err)))
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
	items, err := repo.ListCapabilities(ctx, p.TenantID, typ)
	if err != nil {
		return nil, err
	}
	return deriveCapabilityEffectiveStatuses(items, time.Now().UTC()), nil
}

// assignmentRoleUserIDs 从基础平台读取指定项目角色的有效授权用户。候选列表与提交校验
// 共用该口径，避免调用方绕过前端直接提交“有资质但没有对应职责权限”的人员。

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

func checkCapabilities(required, resources []string, items []domain.Capability) domain.ConflictCheckResult {
	covered := map[string]bool{}
	active := map[string]bool{}
	for _, c := range items {
		isActive := c.Status == "ACTIVE"
		active[c.ResourceID] = isActive
		if userID := strings.TrimSpace(c.UserID); userID != "" {
			active[userID] = isActive
		}
		for _, code := range c.Codes {
			if isActive {
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

// qualifiedUserID 接受资质资源编号或其关联的
// 平台用户 ID，但始终返回平台用户 ID，用于存入
// 工作流责任字段及后续当前用户范围限定。
