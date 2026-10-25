package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
	"time"
)

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
	if err := validateImplementationPlanTexts(input); err != nil {
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
		"expected_version":      input.ExpectedVersion,
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

// planResourceTypes 是人员/设备清单允许的资源类型；两者分别对应能力档案的两类记录。
var planResourceTypes = map[string]string{"PERSON": "人员", "EQUIPMENT": "设备"}

// resolvePlanPersonnel 把实施计划提交的人员行解析成带快照的清单行。
// 规则：至少一名人员；每一行都必须命中本租户有效的能力档案且类型为人员；
// 同一资源不得重复；使用时段必须落在计划起止内。人员资质不受日期有效期限制。

// buildPlanResource 把一行清单输入解析成带快照的资源行：校验类型、命中有效能力档案、
// 去重、使用时段落在计划内；只有设备要求检定有效期覆盖使用时段。
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
	// 校验边界：留空时段表示“全程使用”，此时校验必须以计划开始日为准；
	// 用零值时间与设备检定开始日比较会把“检定从计划开始日生效”的有效设备误判为越界。
	validationStart := effectiveWindowStart(start, planStart)
	validUntil := ""
	if resourceType == "EQUIPMENT" {
		// 设备检定时段以日期表达：覆盖到当天即算覆盖，不因时分量产生边界误判。
		if !capability.ValidFrom.IsZero() && dayOf(capability.ValidFrom).After(dayOf(validationStart)) {
			return domain.PlanResource{}, fmt.Errorf("%s「%s」的检定有效期从 %s 开始，不覆盖使用时段开始日 %s",
				label, capability.ResourceName, capability.ValidFrom.Format("2006-01-02"), start.Format("2006-01-02"))
		}
		if !capability.ValidUntil.IsZero() {
			if dayOf(capability.ValidUntil).Before(dayOf(end)) {
				return domain.PlanResource{}, fmt.Errorf("%s「%s」的检定有效期至 %s，不覆盖使用时段截止日 %s",
					label, capability.ResourceName, capability.ValidUntil.Format("2006-01-02"), end.Format("2006-01-02"))
			}
			validUntil = capability.ValidUntil.Format("2006-01-02")
		}
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

// dayOf 把时间截断到 UTC 日期：计划窗口与设备检定有效期都以"天"为业务粒度，
// 直接比较时间戳会把同一天判成越界或超期。

// dayOf 把时间截断到 UTC 日期：计划窗口与设备检定有效期都以"天"为业务粒度，
// 直接比较时间戳会把同一天判成越界或超期。
func dayOf(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

// planResourceWindow 解析行级使用时段：两端要么都留空（表示全程），要么都填写且不早于计划开始、
// 不晚于计划结束；返回值同时作为设备检定有效期校验的边界。

// planResourceWindow 解析行级使用时段：两端要么都留空（表示全程），要么都填写且不早于计划开始、
// 不晚于计划结束；返回值同时作为设备检定有效期校验的边界。
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

// RevokeImplementationPlan 仅在尚未开始实施准备时允许撤销计划；责任分配和能力结论仍保留，
// 以便项目经理修订排期后重新发布。计划快照由撤销事件保留，不以删除审计证据实现回退。
func (s *Service) RevokeImplementationPlan(ctx context.Context, p platform.Principal, itemID string, input domain.PhaseRevokeInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.implementation.revoke", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.implementation.revoke", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" {
		return ValidationError("请填写撤销实施计划的原因")
	}
	filter, err := authorizeProjectScope(p, "project.implementation.revoke")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Status != "待实施" {
		return PreconditionError("仅可撤销尚未发起实施准备的实施计划")
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventImplementationPlanRevoked, map[string]any{"reason": strings.TrimSpace(input.Reason), "expected_version": input.ExpectedVersion}))
}

// RevokePreparation 在尚未提交现场记录时释放设备预约并回到待实施。设备使用历史在事件中保留，
// 清空的是活动计划行，因此设备可立即被其他项目重新预约。

// planWindowOf 取服务项已发布实施计划的起止；没有计划时无法确定设备占用区间。
func planWindowOf(item domain.ServiceItem) (time.Time, time.Time, error) {
	start, startErr := time.Parse(time.RFC3339, strings.TrimSpace(item.PlannedStart))
	end, endErr := time.Parse(time.RFC3339, strings.TrimSpace(item.PlannedEnd))
	if startErr != nil || endErr != nil || !end.After(start) {
		return time.Time{}, time.Time{}, PreconditionError("请先发布实施计划，再进行实施准备")
	}
	return start, end, nil
}

// resolvePreparationEquipment 解析实施准备提交的可选设备清单。无需设备的服务项可以留空；
// 一旦选择设备，设备必须命中有效能力档案，使用时段落在计划内且被检定有效期覆盖，
// 并且在该时段内没有被其他服务项占用。
