package application

import (
	"context"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// maskedFieldValue 是字段级权限 hidden 脱敏后的占位值。
const maskedFieldValue = "***"

// maskableProjectFields 定义项目上可被字段级权限配置隐藏的敏感字段白名单。
// 主键、状态、身份 ID、时间戳等元数据字段不参与，避免规则配置破坏列表可用性。
var maskableProjectFields = map[string]struct{}{
	"name": {}, "customer": {}, "contract": {}, "category": {},
	"team": {}, "manager": {}, "due": {},
}

// maskableServiceItemFields 定义服务项上可被字段级权限配置隐藏的敏感字段白名单。
var maskableServiceItemFields = map[string]struct{}{
	"batch": {}, "site": {}, "category": {}, "requirement": {}, "system": {},
	"system_level": {}, "special": {}, "test_mode": {}, "source_service_id": {},
}

func maskProjectField(project *domain.Project, field string) {
	switch field {
	case "name":
		project.Name = maskedFieldValue
	case "customer":
		project.Customer = maskedFieldValue
	case "contract":
		project.Contract = maskedFieldValue
	case "category":
		project.Category = maskedFieldValue
	case "team":
		project.Team = maskedFieldValue
	case "manager":
		project.Manager = maskedFieldValue
	case "due":
		project.Due = maskedFieldValue
	}
}

func maskServiceItemField(item *domain.ServiceItem, field string) {
	switch field {
	case "batch":
		item.Batch = maskedFieldValue
	case "site":
		item.Site = maskedFieldValue
	case "category":
		item.Category = maskedFieldValue
	case "requirement":
		item.Requirement = maskedFieldValue
	case "system":
		item.System = maskedFieldValue
	case "system_level":
		item.SystemLevel = maskedFieldValue
	case "special":
		item.Special = maskedFieldValue
	case "test_mode":
		item.TestMode = maskedFieldValue
	case "source_service_id":
		item.SourceServiceID = maskedFieldValue
	}
}

// hiddenFieldsFor 读取租户启用的字段级权限规则，返回当前主体应隐藏的项目字段
// 与服务项字段。无规则或规则未启用时返回空集合，读路径零开销直通。
func (s *Service) hiddenFieldsFor(ctx context.Context, p platform.Principal) (projectHidden, itemHidden map[string]struct{}, err error) {
	rules, err := s.Repo.ListRules(ctx, p.TenantID, "permissions")
	if err != nil {
		return nil, nil, err
	}
	projectHidden = map[string]struct{}{}
	itemHidden = map[string]struct{}{}
	for i := range rules {
		rule := &rules[i]
		if !rule.Enabled || strings.TrimSpace(rule.AccessLevel) != "hidden" {
			continue
		}
		if !principalHasRole(p, rule.RoleCode) {
			continue
		}
		if _, ok := maskableProjectFields[rule.FieldName]; ok {
			projectHidden[rule.FieldName] = struct{}{}
		}
		if _, ok := maskableServiceItemFields[rule.FieldName]; ok {
			itemHidden[rule.FieldName] = struct{}{}
		}
	}
	return projectHidden, itemHidden, nil
}

// principalHasRole 判断主体是否持有指定应用角色码（与 pm_field_permission.role_code 对齐）。
func principalHasRole(p platform.Principal, roleCode string) bool {
	for _, role := range p.Roles {
		if role == roleCode {
			return true
		}
	}
	return false
}

// applyFieldPermissions 对读出的项目与服务项按字段级权限规则执行 hidden 脱敏。
// 任一集合为空时该项保持原样返回，不影响请求路径。
func (s *Service) applyFieldPermissions(ctx context.Context, p platform.Principal, projects []domain.Project, items []domain.ServiceItem) ([]domain.Project, []domain.ServiceItem, error) {
	projectHidden, itemHidden, err := s.hiddenFieldsFor(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	if len(projectHidden) > 0 && len(projects) > 0 {
		for field := range projectHidden {
			for i := range projects {
				maskProjectField(&projects[i], field)
			}
		}
	}
	if len(itemHidden) > 0 && len(items) > 0 {
		for field := range itemHidden {
			for i := range items {
				maskServiceItemField(&items[i], field)
			}
		}
	}
	return projects, items, nil
}
