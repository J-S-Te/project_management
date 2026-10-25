package application

import (
	"context"
	"fmt"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"sort"
	"strings"
	"time"
)

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

const (
	capabilityEffectiveActive          = "ACTIVE"
	capabilityEffectiveDisabled        = "DISABLED"
	capabilityEffectiveExpired         = "EXPIRED"
	capabilityEffectiveNotYetEffective = "NOT_YET_EFFECTIVE"
)

// deriveCapabilityEffectiveStatus 将持久化管理开关与当前业务状态分离。
// 设备有效性基于日期，因此 ACTIVE 行会在 valid_until
// 次日自动失效，无需调度器或读取时写入。

// deriveCapabilityEffectiveStatus 将持久化管理开关与当前业务状态分离。
// 设备有效性基于日期，因此 ACTIVE 行会在 valid_until
// 次日自动失效，无需调度器或读取时写入。
func deriveCapabilityEffectiveStatus(item domain.Capability, now time.Time) domain.Capability {
	item.EffectiveStatus = capabilityEffectiveDisabled
	item.StatusReason = "已手工停用"
	if item.Status != "ACTIVE" {
		return item
	}
	item.EffectiveStatus = capabilityEffectiveActive
	item.StatusReason = ""
	if item.ResourceType != "EQUIPMENT" {
		return item
	}
	today := dayOf(now.UTC())
	if !item.ValidFrom.IsZero() && dayOf(item.ValidFrom).After(today) {
		item.EffectiveStatus = capabilityEffectiveNotYetEffective
		item.StatusReason = "检定有效期尚未开始"
		return item
	}
	if !item.ValidUntil.IsZero() && dayOf(item.ValidUntil).Before(today) {
		item.EffectiveStatus = capabilityEffectiveExpired
		item.StatusReason = "检定有效期已过期"
	}
	return item
}

func deriveCapabilityEffectiveStatuses(items []domain.Capability, now time.Time) []domain.Capability {
	for index := range items {
		items[index] = deriveCapabilityEffectiveStatus(items[index], now)
	}
	return items
}

// resourceIDPrefix 区分人员与设备的资源编号前缀，避免两类编号混用。
// existingUsageScope 在既有能力目录里查同编号设备的使用范围；查不到时返回默认的可借出，
// 保证新增设备与历史数据都落在同一个默认值上。

func (s *Service) UpsertCapability(ctx context.Context, p platform.Principal, item domain.Capability) (domain.Capability, error) {
	if err := requireApplicationAuthorization(p, "project.resource.manage"); err != nil {
		return item, err
	}
	repo, e := s.deliveryRepo()
	if e != nil {
		return item, e
	}
	normalizeCapability(&item)
	if item.ResourceType != "PERSON" {
		return item, ValidationError("设备能力请在「设备能力」中新建或维护")
	}
	item.TenantID = p.TenantID
	existing, err := repo.ListCapabilities(ctx, p.TenantID, item.ResourceType)
	if err != nil {
		return item, err
	}
	// 人员资质必须绑定基础平台当前可见的在职人员。资源名称是平台目录的显示名，
	// 不能信任浏览器提交的任意文本，以免出现“资质档案姓名”和身份主体不一致。
	person, err := s.resolveCapabilityPerson(ctx, item.UserID)
	if err != nil {
		return item, err
	}
	item.UserID = person.UserID
	item.ResourceName = person.DisplayName
	item.IdentityStatus = domain.IdentityStatusActive
	// 导入确认可能与预览后的其他编辑竞争。执行 upsert 时，
	// 不得将已有编号资质重新绑定到其他身份。
	for _, current := range existing {
		if item.ResourceID != "" && current.ResourceID == item.ResourceID && current.UserID != "" && current.UserID != item.UserID {
			return item, ValidationError("资质编号已属于其他人员，不能覆盖")
		}
	}
	if strings.TrimSpace(item.ResourceID) == "" {
		assignResourceID(existing, &item)
	}
	if err := validateCapability(item); err != nil {
		return item, err
	}
	codeRules, err := s.loadCapabilityCodeRules(ctx, p.TenantID)
	if err != nil {
		return item, err
	}
	if err := validateCapabilityCodesAgainstCatalog(codeRules, item.ResourceType, item.Codes, existingCapabilityCodes(existing, item.ResourceType, item.ResourceID)); err != nil {
		return item, err
	}
	item.Status = firstNonEmpty(item.Status, "ACTIVE")
	item.UsageScope = ""
	saved, err := repo.UpsertCapability(ctx, item, p.UserID)
	if err != nil {
		return saved, err
	}
	return deriveCapabilityEffectiveStatus(saved, time.Now().UTC()), nil
}

// resolveCapabilityPerson 以平台 user_id 精确查询人员目录。目录只会返回本应用可见的
// 有效人员；没有精确命中时拒绝保存，避免前端构造不存在或已失效的人员资质。

// resolveCapabilityPerson 以平台 user_id 精确查询人员目录。目录只会返回本应用可见的
// 有效人员；没有精确命中时拒绝保存，避免前端构造不存在或已失效的人员资质。
func (s *Service) resolveCapabilityPerson(ctx context.Context, userID string) (platform.OwnerDirectoryUser, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return platform.OwnerDirectoryUser{}, ErrValidation
	}
	if s.Personnel == nil {
		return platform.OwnerDirectoryUser{}, ErrPersonnelUnavailable
	}
	page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{UserID: userID, Page: 1, PageSize: 1})
	if err != nil {
		return platform.OwnerDirectoryUser{}, fmt.Errorf("%w: %v", ErrPersonnelUnavailable, err)
	}
	for _, person := range page.Items {
		if strings.TrimSpace(person.UserID) == userID && strings.TrimSpace(person.DisplayName) != "" {
			person.UserID = userID
			person.DisplayName = strings.TrimSpace(person.DisplayName)
			return person, nil
		}
	}
	return platform.OwnerDirectoryUser{}, ErrValidation
}

// ImportCapabilities 批量写入人员资质记录。设备生命周期只能通过设备能力接口维护。
// 逐行校验并独立写入，
// 单行失败只累计跳过原因，不影响其余行，避免一条脏数据阻塞整批导入。

// ListQualifiedPersonnel 是唯一指派选择器来源。人员必须
// 同时拥有有效、关联身份的 PERSON 资质记录，
// 以及请求的平台有效角色。人员日期不约束
// 指派，只有设备校准使用有效期窗口。
func (s *Service) ListQualifiedPersonnel(ctx context.Context, p platform.Principal, roleCode, keyword string, page, pageSize int) (domain.QualifiedPersonnelPage, error) {
	result := domain.QualifiedPersonnelPage{Items: []domain.QualifiedPersonnel{}}
	if !p.Has("project.read") && !p.Has("project.team.assign") && !p.Has("project.execution.assign") {
		return result, ErrForbidden
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return result, err
	}
	allowed, err := s.assignmentRoleUserIDs(ctx, roleCode)
	if err != nil {
		return result, err
	}
	items, err := repo.ListCapabilities(ctx, p.TenantID, "PERSON")
	if err != nil {
		return result, err
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 50
	}
	wanted := strings.ToLower(strings.TrimSpace(keyword))
	eligible := make([]domain.QualifiedPersonnel, 0, len(items))
	for _, item := range items {
		userID := strings.TrimSpace(item.UserID)
		if item.Status != "ACTIVE" || item.IdentityStatus != domain.IdentityStatusActive || userID == "" {
			continue
		}
		if _, ok := allowed[userID]; !ok {
			continue
		}
		searchable := strings.ToLower(item.ResourceID + " " + item.ResourceName + " " + strings.Join(item.Codes, " "))
		if wanted != "" && !strings.Contains(searchable, wanted) {
			continue
		}
		eligible = append(eligible, domain.QualifiedPersonnel{UserID: userID, ResourceID: item.ResourceID, DisplayName: item.ResourceName, Codes: append([]string{}, item.Codes...), IdentityStatus: item.IdentityStatus})
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].DisplayName == eligible[j].DisplayName {
			return eligible[i].ResourceID < eligible[j].ResourceID
		}
		return eligible[i].DisplayName < eligible[j].DisplayName
	})
	result.Page, result.PageSize, result.Total = page, pageSize, int64(len(eligible))
	start := (page - 1) * pageSize
	if start >= len(eligible) {
		return result, nil
	}
	end := start + pageSize
	if end > len(eligible) {
		end = len(eligible)
	}
	result.Items = eligible[start:end]
	return result, nil
}

// PersonnelIdentitySyncResult 汇总一次人员资质身份复核的结果。

// qualifiedUserID 接受资质资源编号或其关联的
// 平台用户 ID，但始终返回平台用户 ID，用于存入
// 工作流责任字段及后续当前用户范围限定。
func qualifiedUserID(candidate string, items []domain.Capability) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	for _, item := range items {
		if item.ResourceType != "PERSON" || item.Status != "ACTIVE" || item.IdentityStatus != domain.IdentityStatusActive {
			continue
		}
		userID := strings.TrimSpace(item.UserID)
		if userID != "" && (candidate == userID || candidate == strings.TrimSpace(item.ResourceID)) {
			return userID, true
		}
	}
	return "", false
}

// effectiveWindowStart 返回用于设备检定校验的窗口开始日：
// 行级时段留空表示“全程使用”，此时取计划开始日；否则取行级填写值。
