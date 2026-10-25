package application

import (
	"context"
	"fmt"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
	"time"
)

// ReturnEquipment 把某台设备从服务项的实施准备清单中归还：清单行保留（保留借出历史），
// 但标记归还时间后不再占用设备，也不再算「不在公司」。设备维护人员与项目经理都可发起归还，
// 因为设备可能由现场提前寄回。
func (s *Service) ReturnEquipment(ctx context.Context, p platform.Principal, itemID, resourceID string) error {
	filter, err := authorizeAnyProjectScope(p, "project.implementation.plan", "project.device.manage")
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

// ListEquipmentReservations 返回与该项目计划窗口重叠的其他服务项设备占用，
// 供「实施准备」的选择器把已占用设备置灰并解释占用方。计划尚未发布时返回全部未过滤占用。
func (s *Service) ListEquipmentReservations(ctx context.Context, p platform.Principal, itemID string) ([]domain.EquipmentReservation, error) {
	filter, err := authorizeAnyProjectScope(p, "project.implementation.plan", "project.read")
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

// resolvePreparationEquipment 解析实施准备提交的可选设备清单。无需设备的服务项可以留空；
// 一旦选择设备，设备必须命中有效能力档案，使用时段落在计划内且被检定有效期覆盖，
// 并且在该时段内没有被其他服务项占用。
func (s *Service) resolvePreparationEquipment(ctx context.Context, repo DeliveryRepository, tenantID, serviceItemID string, inputs []domain.PlanResourceInput, planStart, planEnd time.Time) ([]domain.PlanResource, error) {
	if len(inputs) == 0 {
		// 返回非 nil 空切片，使事件快照明确写入 []，避免重新发起实施准备时沿用旧设备清单。
		return []domain.PlanResource{}, nil
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
// 时段口径：两端都含当日，与设备检定有效期保持同一种日期语义。
// 因此 [1 日..5 日] 与 [5 日..10 日] 在 5 日当天重叠，算冲突（此前用半开区间会漏判）。
// 占用记录的使用时段解析失败时按「占用」处理：无法证明设备空闲时不得放行，
// 同时把数据异常显式报出来，避免整段占用检查静默失效。

// equipmentUsageConflicts 返回与该设备行使用时段重叠的其他服务项占用描述。
// 区间按左闭右开比较：结束日当天不算占用下一天。
// equipmentUsageConflicts 返回与目标时段重叠的其他设备占用。
//
// 时段口径：两端都含当日，与设备检定有效期保持同一种日期语义。
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
	items = deriveCapabilityEffectiveStatuses(items, time.Now().UTC())
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
	return s.upsertEquipment(ctx, p, item, false)
}

func (s *Service) upsertEquipment(ctx context.Context, p platform.Principal, item domain.Capability, createOnly bool) (domain.Capability, error) {
	if err := requireApplicationAuthorization(p, "project.device.manage"); err != nil {
		return item, err
	}
	normalizeCapability(&item)
	if item.ResourceType != "EQUIPMENT" || item.ResourceID == "" || item.ResourceName == "" || len(item.Codes) == 0 {
		return item, ErrValidation
	}
	if err := validateProjectTextLength(item.ResourceID, 64, "设备编号"); err != nil {
		return item, err
	}
	if err := validateProjectTextLength(item.ResourceName, 128, "设备名称"); err != nil {
		return item, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return item, e
	}
	item.TenantID = p.TenantID
	existing, err := repo.ListCapabilities(ctx, p.TenantID, "EQUIPMENT")
	if err != nil {
		return item, err
	}
	codeRules, err := s.loadCapabilityCodeRules(ctx, p.TenantID)
	if err != nil {
		return item, err
	}
	if err := validateCapabilityCodesAgainstCatalog(codeRules, item.ResourceType, item.Codes, existingCapabilityCodes(existing, item.ResourceType, item.ResourceID)); err != nil {
		return item, err
	}
	// 未提交使用范围时沿用既有设置，避免"编辑设备"顺手把「仅在公司使用」改回可借出。
	if strings.TrimSpace(item.UsageScope) == "" {
		item.UsageScope = existingUsageScope(existing, item.ResourceID)
	}
	item.UsageScope = strings.ToUpper(strings.TrimSpace(firstNonEmpty(item.UsageScope, domain.EquipmentUsageAny)))
	if item.UsageScope != domain.EquipmentUsageAny && item.UsageScope != domain.EquipmentUsageCompanyOnly {
		return item, ValidationError("使用范围只能是「可借出」或「仅在公司使用」")
	}
	item.Status = firstNonEmpty(item.Status, "ACTIVE")
	var saved domain.Capability
	if createOnly {
		// 生成的导入编号绝不能将插入竞争变为覆盖。
		creator, ok := repo.(EquipmentImportCreator)
		if !ok {
			return item, ValidationError("设备导入安全写入暂不可用，请稍后重试")
		}
		saved, err = creator.CreateEquipmentForImport(ctx, item, p.UserID)
	} else {
		saved, err = repo.UpsertCapability(ctx, item, p.UserID)
	}
	if err != nil {
		return saved, err
	}
	return deriveCapabilityEffectiveStatus(saved, time.Now().UTC()), nil
}

// DeleteEquipment 删除未被活动实施计划占用的设备主数据。已完成或已归还设备的历史快照
// 保留在实施计划/交付事件中；仍被未归还设备清单引用时必须先归还或撤销实施准备，避免
// 目录删除后让在途设备失去可追溯的管理入口。

// DeleteEquipment 删除未被活动实施计划占用的设备主数据。已完成或已归还设备的历史快照
// 保留在实施计划/交付事件中；仍被未归还设备清单引用时必须先归还或撤销实施准备，避免
// 目录删除后让在途设备失去可追溯的管理入口。
func (s *Service) DeleteEquipment(ctx context.Context, p platform.Principal, resourceID string) error {
	if err := requireApplicationAuthorization(p, "project.device.manage"); err != nil {
		return err
	}
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return ErrValidation
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	reservations, err := repo.ListEquipmentReservations(ctx, p.TenantID, "")
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if reservation.ResourceID == resourceID {
			return ConflictError("设备仍被服务项「" + firstNonEmpty(reservation.ServiceItemID, reservation.ProjectID) + "」占用，请先归还设备或撤销实施准备后再删除")
		}
	}
	return repo.DeleteEquipment(ctx, p.TenantID, resourceID)
}
