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
// 指派关系（团队负责人 / 项目经理 / 工程师）同样是敏感字段：隐藏配置必须覆盖它，
// 否则/服务项列表与事件流都能看到"谁被派到了哪个项目"。
var maskableServiceItemFields = map[string]struct{}{
	"batch": {}, "site": {}, "category": {}, "requirement": {}, "system": {},
	"system_level": {}, "special": {}, "test_mode": {}, "source_service_id": {},
	"team_lead_id": {}, "project_manager_id": {}, "engineer_ids": {},
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
	case "team_lead_id":
		item.TeamLeadID = maskedFieldValue
	case "project_manager_id":
		item.ProjectManagerID = maskedFieldValue
	case "engineer_ids":
		if len(item.EngineerIDs) > 0 {
			item.EngineerIDs = []string{maskedFieldValue}
		}
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

// applyFieldPermissionsToSlaItems 对 SLA 超期列表执行与服务项读路径相同的字段脱敏。
// 该列表会带出 site/category，不处理会让配置为 hidden 的字段从这条读路径整片漏出。
func (s *Service) applyFieldPermissionsToSlaItems(ctx context.Context, p platform.Principal, items []domain.SlaOverdueItem) ([]domain.SlaOverdueItem, error) {
	_, itemHidden, err := s.hiddenFieldsFor(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(itemHidden) == 0 || len(items) == 0 {
		return items, nil
	}
	for index := range items {
		if _, hide := itemHidden["site"]; hide {
			items[index].Site = maskedFieldValue
		}
		if _, hide := itemHidden["category"]; hide {
			items[index].Category = maskedFieldValue
		}
	}
	return items, nil
}

// applyFieldPermissionsToReservations 对设备预约列表执行项目字段脱敏：该列表带出客户名，
// 与项目读路径共用 customer 这一隐藏口径。
func (s *Service) applyFieldPermissionsToReservations(ctx context.Context, p platform.Principal, reservations []domain.EquipmentReservation) ([]domain.EquipmentReservation, error) {
	projectHidden, _, err := s.hiddenFieldsFor(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(projectHidden) == 0 || len(reservations) == 0 {
		return reservations, nil
	}
	if _, hide := projectHidden["customer"]; hide {
		for index := range reservations {
			reservations[index].Customer = maskedFieldValue
		}
	}
	return reservations, nil
}

// applyFieldPermissionsToEvents 对交付事件流执行与服务项读路径完全相同的字段脱敏。
// 事件 payload 里带着指派快照与拆解快照，若不处理，配置为 hidden 的字段会从
// /delivery-events 整条漏出去——脱敏只有覆盖所有读路径才算生效。
func (s *Service) applyFieldPermissionsToEvents(ctx context.Context, p platform.Principal, events []domain.DeliveryEvent) ([]domain.DeliveryEvent, error) {
	_, itemHidden, err := s.hiddenFieldsFor(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(itemHidden) == 0 || len(events) == 0 {
		return events, nil
	}
	for index := range events {
		events[index].Payload = maskEventPayload(events[index].Payload, itemHidden)
	}
	return events, nil
}

// maskEventPayload 递归掩码 payload 中命中隐藏字段的键；拆解事件里的 service_items
// 是服务项快照，按同一套字段名递归处理。返回新 map，不修改调用方持有的原对象。
func maskEventPayload(payload map[string]any, hidden map[string]struct{}) map[string]any {
	if payload == nil {
		return nil
	}
	masked := make(map[string]any, len(payload))
	for key, value := range payload {
		if _, hide := hidden[key]; hide {
			masked[key] = maskedFieldValue
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			masked[key] = maskEventPayload(typed, hidden)
		case []any:
			entries := make([]any, len(typed))
			for index, entry := range typed {
				if object, ok := entry.(map[string]any); ok {
					entries[index] = maskEventPayload(object, hidden)
					continue
				}
				entries[index] = entry
			}
			masked[key] = entries
		default:
			masked[key] = value
		}
	}
	return masked
}
