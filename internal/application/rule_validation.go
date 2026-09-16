package application

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

func normalizeRuleName(input *domain.Rule) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return ValidationError("配置名称不能为空")
	}
	if utf8.RuneCountInString(input.Name) > 255 || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 {
		return ValidationError("配置名称不能超过 255 个字符或包含控制字符")
	}
	return nil
}

func validateCatalogRole(roleCode string) error {
	roles, err := platform.RoleCatalog()
	if err != nil {
		return fmt.Errorf("load project role catalog: %w", err)
	}
	for _, role := range roles {
		if role.Code == roleCode {
			return nil
		}
	}
	return ValidationError("所选角色不在项目系统角色目录中")
}

func normalizeAutomationRule(input *domain.Rule) error {
	input.Trigger = strings.ToUpper(strings.TrimSpace(input.Trigger))
	input.Target = strings.TrimSpace(input.Target)
	if !ruleOptionExists(automationTriggerCatalog, input.Trigger) {
		return ValidationError("所选触发事件不受自动化引擎支持")
	}
	if err := validateCatalogRole(input.Target); err != nil {
		return err
	}
	return nil
}

func normalizeSlaRule(input *domain.Rule) error {
	input.Status = strings.TrimSpace(input.Status)
	if !ruleOptionExists(slaStatusCatalog, input.Status) {
		return ValidationError("所选状态不支持服务项 SLA")
	}
	if input.DeadlineHours < 1 {
		return ValidationError("时限小时必须大于等于 1")
	}
	if input.RemindHours < 0 || input.RemindHours >= input.DeadlineHours {
		return ValidationError("提前提醒小时必须大于等于 0 且小于时限小时；0 表示不提前提醒")
	}
	return nil
}

func (s *Service) ensureRuleCombinationUnique(ctx context.Context, tenantID string, input domain.Rule, excludingID int64) error {
	rules, err := s.Repo.ListRules(ctx, tenantID, input.Kind)
	if err != nil {
		return err
	}
	for _, existing := range rules {
		if existing.ID == excludingID {
			continue
		}
		var duplicate bool
		switch input.Kind {
		case "automations":
			duplicate = strings.TrimSpace(existing.Trigger) == input.Trigger && strings.TrimSpace(existing.Target) == input.Target
		case "permissions":
			duplicate = strings.TrimSpace(existing.RoleCode) == input.RoleCode && strings.TrimSpace(existing.FieldName) == input.FieldName
		case "sla":
			duplicate = strings.TrimSpace(existing.Status) == input.Status
		}
		if duplicate {
			return ConflictError("相同生效条件的规则已存在，请直接编辑或启用原配置")
		}
	}
	return nil
}

// normalizeAndValidateRule 是创建、更新和重新启用规则的唯一校验入口。
// updating 只用于编码目录的不可变身份检查；excludingID 让规则更新/启用时忽略自身。
func (s *Service) normalizeAndValidateRule(ctx context.Context, p platform.Principal, input *domain.Rule, excludingID int64, updating bool) error {
	input.Kind = strings.TrimSpace(input.Kind)
	if err := normalizeRuleName(input); err != nil {
		return err
	}
	switch input.Kind {
	case "split-rules":
		input.Scope = strings.TrimSpace(input.Scope)
		if input.Scope == "" {
			return ValidationError("适用范围不能为空")
		}
	case "warning-rules":
		if err := normalizeWarningRule(input); err != nil {
			return err
		}
	case "automations":
		if err := normalizeAutomationRule(input); err != nil {
			return err
		}
		return s.ensureRuleCombinationUnique(ctx, p.TenantID, *input, excludingID)
	case "permissions":
		if err := normalizeFieldPermissionRule(input); err != nil {
			return err
		}
		if err := validateCatalogRole(input.RoleCode); err != nil {
			return err
		}
		return s.ensureRuleCombinationUnique(ctx, p.TenantID, *input, excludingID)
	case "sla":
		if err := normalizeSlaRule(input); err != nil {
			return err
		}
		return s.ensureRuleCombinationUnique(ctx, p.TenantID, *input, excludingID)
	case "standards":
		input.Scope = strings.TrimSpace(input.Scope)
		if input.Scope == "" {
			return ValidationError("适用方法或范围不能为空")
		}
	case capabilityCodeRuleKind:
		if err := normalizeCapabilityCodeRule(input); err != nil {
			return err
		}
		if updating {
			if err := s.ensureCapabilityCodeIdentityUnchanged(ctx, p.TenantID, *input); err != nil {
				return err
			}
		}
		return s.ensureCapabilityCodeUnique(ctx, p.TenantID, *input, excludingID)
	default:
		return ValidationError("规则类型不受支持")
	}
	return nil
}
