package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/j-s-te/project-management/internal/domain"
)

const capabilityCodeRuleKind = "capability-codes"

// normalizeCapabilityCodes 统一输入的大小写和空白，并稳定去重。
// 目录表使用不区分大小写的 MySQL 排序规则，应用层同样规范化可避免
// 响应、CSV 导入和能力比对出现同一编码的多种形式。
func normalizeCapabilityCodes(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		code := normalizeCapabilityCode(value)
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	return result
}

func normalizeCapabilityCode(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func normalizeCapability(item *domain.Capability) {
	item.ResourceType = strings.ToUpper(strings.TrimSpace(item.ResourceType))
	item.ResourceID = strings.TrimSpace(item.ResourceID)
	item.ResourceName = strings.TrimSpace(item.ResourceName)
	item.UserID = strings.TrimSpace(item.UserID)
	item.Codes = normalizeCapabilityCodes(item.Codes)
	// 人员资质不按日期失效；只有停用资质或失效的平台身份不可参与派工。
	// 清空调用方提交的历史日期，避免界面或 CSV 再次写回已废弃的限制。
	if item.ResourceType == "PERSON" {
		item.ValidFrom = time.Time{}
		item.ValidUntil = time.Time{}
	}
}

func normalizeCapabilityCodeRule(item *domain.Rule) error {
	item.Name = strings.TrimSpace(item.Name)
	item.Scope = normalizeCapabilityCode(item.Scope)
	item.CheckType = strings.ToUpper(strings.TrimSpace(item.CheckType))
	if item.Name == "" {
		return ValidationError("请填写资质 / 能力编码名称")
	}
	if utf8.RuneCountInString(item.Name) > 255 {
		return ValidationError("资质 / 能力编码名称不能超过 255 个字符")
	}
	if item.Scope == "" {
		return ValidationError("请填写资质 / 能力编码")
	}
	if utf8.RuneCountInString(item.Scope) > 128 {
		return ValidationError("资质 / 能力编码不能超过 128 个字符")
	}
	if strings.IndexFunc(item.Name, unicode.IsControl) >= 0 || strings.IndexFunc(item.Scope, unicode.IsControl) >= 0 {
		return ValidationError("资质 / 能力编码名称和编码不能包含换行或控制字符")
	}
	if item.CheckType != "PERSON" && item.CheckType != "EQUIPMENT" {
		return ValidationError("编码类型只能是「人员资质」或「设备能力」")
	}
	return nil
}

// ensureCapabilityCodeUnique 在唯一索引前给出业务可读的重复提示。
// 数据库唯一约束仍是并发情况下的最终保障。
func (s *Service) ensureCapabilityCodeUnique(ctx context.Context, tenantID string, item domain.Rule, excludingID int64) error {
	rules, err := s.Repo.ListRules(ctx, tenantID, capabilityCodeRuleKind)
	if err != nil {
		return err
	}
	for _, existing := range rules {
		if existing.ID == excludingID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(existing.CheckType), item.CheckType) && normalizeCapabilityCode(existing.Scope) == item.Scope {
			return ValidationError(fmt.Sprintf("编码「%s」已存在，请直接编辑或启用原配置", item.Scope))
		}
	}
	return nil
}

func (s *Service) ensureCapabilityCodeIdentityUnchanged(ctx context.Context, tenantID string, item domain.Rule) error {
	rules, err := s.Repo.ListRules(ctx, tenantID, capabilityCodeRuleKind)
	if err != nil {
		return err
	}
	for _, existing := range rules {
		if existing.ID != item.ID {
			continue
		}
		if normalizeCapabilityCode(existing.Scope) != item.Scope || !strings.EqualFold(strings.TrimSpace(existing.CheckType), item.CheckType) {
			return ValidationError("资质 / 能力编码及类型创建后不可修改；请新建编码并禁用旧编码，以保留历史引用语义")
		}
		return nil
	}
	return ErrNotFound
}

type capabilityCodeCatalog struct {
	configured bool
	enabled    map[string]struct{}
}

func capabilityCatalogFor(rules []domain.Rule, resourceType string) capabilityCodeCatalog {
	catalog := capabilityCodeCatalog{enabled: map[string]struct{}{}}
	for _, rule := range rules {
		if !strings.EqualFold(strings.TrimSpace(rule.CheckType), resourceType) {
			continue
		}
		catalog.configured = true
		if rule.Enabled {
			catalog.enabled[normalizeCapabilityCode(rule.Scope)] = struct{}{}
		}
	}
	return catalog
}

func (s *Service) loadCapabilityCodeRules(ctx context.Context, tenantID string) ([]domain.Rule, error) {
	return s.Repo.ListRules(ctx, tenantID, capabilityCodeRuleKind)
}

// validateCapabilityCodesAgainstCatalog 对新写入失败关闭：必须先建立该类型目录。
// grandfathered 是同一档案上次已存的编码；它们可在编辑其它字段时原样保留，
// 即使目录已停用/删除。这只是历史兼容，不允许把该编码新增到别的档案。
func validateCapabilityCodesAgainstCatalog(rules []domain.Rule, resourceType string, codes, grandfathered []string) error {
	catalog := capabilityCatalogFor(rules, resourceType)
	retained := make(map[string]struct{}, len(grandfathered))
	for _, code := range normalizeCapabilityCodes(grandfathered) {
		retained[code] = struct{}{}
	}
	invalid := make([]string, 0)
	for _, code := range codes {
		if _, allowed := catalog.enabled[code]; allowed {
			continue
		}
		if _, historical := retained[code]; historical {
			continue
		}
		invalid = append(invalid, code)
	}
	if len(invalid) == 0 {
		return nil
	}
	sort.Strings(invalid)
	label := "人员资质"
	if resourceType == "EQUIPMENT" {
		label = "设备能力"
	}
	if !catalog.configured {
		return ValidationError(fmt.Sprintf("尚未配置%s编码目录，请先在「系统配置 > 资质 / 能力编码」中新建并启用编码", label))
	}
	return ValidationError(fmt.Sprintf("%s编码「%s」未在系统配置中启用，请先在「系统配置 > 资质 / 能力编码」中维护", label, strings.Join(invalid, "、")))
}

// validateRequiredCodesAgainstCatalog 校验检测类别的必检能力码。分配环节目前只用
// required_codes 校验工程师，因此这里只能引用 PERSON 资质目录，不得用设备码冒充。
func validateRequiredCodesAgainstCatalog(rules []domain.Rule, codes []string) error {
	if len(codes) == 0 {
		return nil
	}
	catalog := capabilityCatalogFor(rules, "PERSON")
	invalid := make([]string, 0)
	for _, code := range codes {
		if _, ok := catalog.enabled[code]; !ok {
			invalid = append(invalid, code)
		}
	}
	if len(invalid) == 0 {
		return nil
	}
	sort.Strings(invalid)
	if !catalog.configured {
		return ValidationError("尚未配置人员资质编码目录，请先在「系统配置 > 资质 / 能力编码」中新建并启用编码")
	}
	return ValidationError(fmt.Sprintf("必检能力码「%s」未在资质 / 能力编码目录中启用", strings.Join(invalid, "、")))
}

func existingCapabilityCodes(items []domain.Capability, resourceType, resourceID string) []string {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.ResourceType), resourceType) && strings.TrimSpace(item.ResourceID) == resourceID {
			return item.Codes
		}
	}
	return nil
}

// CapabilityCodeReferenceRepository 由真实 MySQL 仓储实现，用于在删除目录项前
// 检查能力档案和检测类别的历史引用。
type CapabilityCodeReferenceRepository interface {
	CountCapabilityCodeReferences(context.Context, string, string, string) (int64, error)
}
