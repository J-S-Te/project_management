package application

import (
	"context"
	"fmt"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
	"time"
)

// 用途：把当前服务项已分配人员解析成实施计划清单快照。
// 参数：ctx 为请求上下文，repo 为交付仓储，tenantID 为租户，item 为当前服务项，
//
//	inputs 为选择的人员，planStart 和 planEnd 为计划窗口。
//
// 返回值：快照清单；空选、跨服务项、重复、无效档案或超出计划窗口时返回错误。
func (s *Service) resolvePlanPersonnel(ctx context.Context, repo DeliveryRepository, tenantID string, item domain.ServiceItem, inputs []domain.PlanResourceInput, planStart, planEnd time.Time) ([]domain.PlanResource, error) {
	if len(inputs) == 0 {
		return nil, ValidationError("请至少添加一名实施人员")
	}
	known, err := repo.ListCapabilities(ctx, tenantID, "PERSON")
	if err != nil {
		return nil, err
	}
	byResourceID := make(map[string]domain.Capability, len(known))
	byUserID := make(map[string]domain.Capability)
	for _, capability := range known {
		if capability.ResourceType != "PERSON" {
			continue
		}
		byResourceID[capability.ResourceID] = capability
		if userID := strings.TrimSpace(capability.UserID); userID != "" {
			byUserID[userID] = capability
		}
	}
	resources := make([]domain.PlanResource, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	assigned := append([]string{item.TeamLeadID, item.ProjectManagerID}, item.EngineerIDs...)
	for index, input := range inputs {
		line := fmt.Sprintf("第 %d 行", index+1)
		if strings.EqualFold(strings.TrimSpace(input.ResourceType), "EQUIPMENT") {
			return nil, ValidationError(line + "：设备清单在「实施准备」中维护，实施计划只提交人员")
		}
		resourceID := strings.TrimSpace(input.ResourceID)
		capability, found := byResourceID[resourceID]
		if !found {
			capability = byUserID[resourceID]
		}
		if capability.ResourceType == "PERSON" {
			// 以档案编号去重，兼容旧计划的档案编号和任务分配的平台用户编号。
			input.ResourceID = capability.ResourceID
		}
		row, err := s.buildPlanResource(byResourceID, input, "PERSON", planStart, planEnd, seen)
		if err != nil {
			return nil, ValidationError(line + "：" + err.Error())
		}
		userID := strings.TrimSpace(capability.UserID)
		if !containsString(assigned, row.ResourceID) && (userID == "" || !containsString(assigned, userID)) {
			return nil, ValidationError(line + "：实施人员必须来自当前服务项已分配人员")
		}
		resources = append(resources, row)
	}
	return resources, nil
}

// buildPlanResource 把一行清单输入解析成带快照的资源行：校验类型、命中有效能力档案、
// 去重、使用时段落在计划内；只有设备要求检定有效期覆盖使用时段。

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
	// Changed 是本次真正改写身份状态的档案数：目录结论与库内一致时不写。
	Changed int `json:"changed"`
}

// SyncPersonnelIdentities 把人员资质档案回基础平台负责人目录复核一次。
// 资质与能力在项目管理系统内维护，但"这个人是否真实存在（在职）"只能由基础平台回答：
// 目录中查得到的标记 ACTIVE，查无此人的标记 MISSING，未关联 user_id 的历史档案保持
// UNLINKED。目录报错的 ID 记为 Unverified 且不改写，避免把平台故障误判成离职。

// SyncPersonnelIdentities 把人员资质档案回基础平台负责人目录复核一次。
// 资质与能力在项目管理系统内维护，但"这个人是否真实存在（在职）"只能由基础平台回答：
// 目录中查得到的标记 ACTIVE，查无此人的标记 MISSING，未关联 user_id 的历史档案保持
// UNLINKED。目录报错的 ID 记为 Unverified 且不改写，避免把平台故障误判成离职。
// 幂等：目录结论与库内一致的档案不写库（Changed=0），后台每轮重复复核不会产生写入。
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
	// 同时只把与库内不一致的结论交给仓储：重复复核同一目录结果不应产生任何
	// 写入（后台每轮都会调用本方法，逐人重写会制造无意义的写放大与审计噪声）。
	// 状态变化数按 user_id 去重，避免同一账号的多条历史档案重复计数。
	changes := make(map[string]string, len(linked))
	for _, item := range items {
		if strings.TrimSpace(item.UserID) == "" {
			result.Unlinked++
			continue
		}
		userID := strings.TrimSpace(item.UserID)
		status, resolved := statuses[userID]
		switch {
		case !resolved:
			result.Unverified++
		case status == domain.IdentityStatusActive:
			result.Active++
		default:
			result.Missing++
		}
		if resolved && status != item.IdentityStatus {
			changes[userID] = status
		}
	}
	result.Changed = len(changes)
	if len(changes) > 0 {
		if err := repo.UpdateCapabilityIdentities(ctx, p.TenantID, changes, time.Now().UTC()); err != nil {
			return result, err
		}
	}
	return result, nil
}
