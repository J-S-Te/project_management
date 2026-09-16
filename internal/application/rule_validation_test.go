package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type validatingRuleRepository struct {
	scopeRepository
	rules []domain.Rule
}

func (r *validatingRuleRepository) ListRules(_ context.Context, _ string, kind string) ([]domain.Rule, error) {
	result := make([]domain.Rule, 0, len(r.rules))
	for _, rule := range r.rules {
		if rule.Kind == kind {
			result = append(result, rule)
		}
	}
	return result, nil
}

func (r *validatingRuleRepository) CreateRule(_ context.Context, rule domain.Rule) (domain.Rule, error) {
	rule.ID = int64(len(r.rules) + 1)
	r.rules = append(r.rules, rule)
	return rule, nil
}

func (r *validatingRuleRepository) UpdateRule(_ context.Context, _, kind string, id int64, rule domain.Rule) (domain.Rule, error) {
	for index := range r.rules {
		if r.rules[index].Kind == kind && r.rules[index].ID == id {
			rule.ID = id
			r.rules[index] = rule
			return rule, nil
		}
	}
	return domain.Rule{}, ErrNotFound
}

func (r *validatingRuleRepository) SetRuleEnabled(_ context.Context, _, kind string, id int64, enabled bool, _ string) (domain.Rule, error) {
	for index := range r.rules {
		if r.rules[index].Kind == kind && r.rules[index].ID == id {
			r.rules[index].Enabled = enabled
			return r.rules[index], nil
		}
	}
	return domain.Rule{}, ErrNotFound
}

func TestRuleConfigurationCatalogOnlyExposesExecutableValues(t *testing.T) {
	triggers, statuses := RuleConfigurationCatalog()
	if len(triggers) == 0 || len(statuses) == 0 {
		t.Fatal("rule configuration catalogs must not be empty")
	}
	for _, forbidden := range []string{EventAutomationTriggered, EventWarningTriggered, EventContractActivated} {
		if ruleOptionExists(triggers, forbidden) {
			t.Fatalf("non-executable or recursively-derived event %q must not be configurable", forbidden)
		}
	}
	if !ruleOptionExists(triggers, EventTeamAssigned) || !ruleOptionExists(triggers, EventScopeChangeDetected) || !ruleOptionExists(statuses, domain.ProjectStatusInProgress) {
		t.Fatalf("catalog misses runtime values: triggers=%+v statuses=%+v", triggers, statuses)
	}
	// 返回副本，调用方不能污染服务端校验白名单。
	triggers[0].Value = "MUTATED"
	second, _ := RuleConfigurationCatalog()
	if second[0].Value == "MUTATED" {
		t.Fatal("catalog leaked mutable global state")
	}
}

func TestAutomationRulesValidateTriggerRoleAndDuplicateCombination(t *testing.T) {
	repository := &validatingRuleRepository{}
	service := &Service{Repo: repository}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-1"})

	created, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: "automations", Name: " 分配后通知负责人 ", Trigger: " team_assigned ", Target: "team_lead", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "分配后通知负责人" || created.Trigger != EventTeamAssigned {
		t.Fatalf("automation rule was not normalized: %+v", created)
	}
	for name, input := range map[string]domain.Rule{
		"unknown trigger": {Kind: "automations", Name: "错误事件", Trigger: "PROJECT_CREATED", Target: "team_lead"},
		"unknown role":    {Kind: "automations", Name: "错误角色", Trigger: EventFieldCompleted, Target: "ghost_role"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.CreateRule(context.Background(), principal, input); !errors.Is(err, ErrValidation) {
				t.Fatalf("error=%v, want validation", err)
			}
		})
	}
	if _, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: "automations", Name: "重复", Trigger: EventTeamAssigned, Target: "team_lead",
	}); !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("duplicate error=%v, want resource conflict", err)
	}
}

func TestSlaRulesValidateStatusNumbersAndDuplicateStatus(t *testing.T) {
	repository := &validatingRuleRepository{}
	service := &Service{Repo: repository}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-1"})

	created, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: "sla", Name: "实施中 SLA", Status: domain.ProjectStatusInProgress, DeadlineHours: 24, RemindHours: 4, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.DeadlineHours != 24 || created.RemindHours != 4 {
		t.Fatalf("created=%+v", created)
	}
	for name, input := range map[string]domain.Rule{
		"unknown status":      {Kind: "sla", Name: "未知", Status: "已完成", DeadlineHours: 24, RemindHours: 4},
		"zero deadline":       {Kind: "sla", Name: "零时限", Status: domain.ProjectStatusPreparing, DeadlineHours: 0},
		"remind equals limit": {Kind: "sla", Name: "错误提醒", Status: domain.ProjectStatusPreparing, DeadlineHours: 4, RemindHours: 4},
		"negative reminder":   {Kind: "sla", Name: "负提醒", Status: domain.ProjectStatusPreparing, DeadlineHours: 4, RemindHours: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.CreateRule(context.Background(), principal, input); !errors.Is(err, ErrValidation) {
				t.Fatalf("error=%v, want validation", err)
			}
		})
	}
	if _, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: "sla", Name: "重复状态", Status: domain.ProjectStatusInProgress, DeadlineHours: 48, RemindHours: 8,
	}); !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("duplicate error=%v, want resource conflict", err)
	}
}

func TestFieldPermissionValidatesRoleAndDuplicateCombination(t *testing.T) {
	repository := &validatingRuleRepository{}
	service := &Service{Repo: repository}
	principal := principalWith("project.field_permission.manage", platform.DataScope{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-1"})

	if _, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: "permissions", Name: "隐藏客户", RoleCode: "engineer", FieldName: "customer", AccessLevel: "hidden", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: "permissions", Name: "无效角色", RoleCode: "ghost_role", FieldName: "site", AccessLevel: "hidden",
	}); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "角色目录") {
		t.Fatalf("unknown role error=%v", err)
	}
	if _, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: "permissions", Name: "重复字段", RoleCode: "engineer", FieldName: "customer", AccessLevel: "hidden",
	}); !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("duplicate error=%v, want resource conflict", err)
	}
}

func TestCreateUpdateAndEnableShareRuleValidation(t *testing.T) {
	repository := &validatingRuleRepository{rules: []domain.Rule{
		{ID: 1, Kind: "sla", Name: "历史非法 SLA", Status: "不存在的状态", DeadlineHours: 24, RemindHours: 4, Enabled: false},
		{ID: 2, Kind: "automations", Name: "历史非法自动化", Trigger: "PROJECT_CREATED", Target: "team_lead", Enabled: false},
		{ID: 3, Kind: "automations", Name: " 历史合法自动化 ", Trigger: " team_assigned ", Target: " team_lead ", Enabled: false},
	}}
	service := &Service{Repo: repository}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-1"})

	if _, err := service.UpdateRule(context.Background(), principal, 1, domain.Rule{
		Kind: "sla", Name: "仍然非法", Status: "不存在的状态", DeadlineHours: 24, RemindHours: 4,
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("update bypassed validation: %v", err)
	}
	if _, err := service.SetRuleEnabled(context.Background(), principal, "automations", 2, true); !errors.Is(err, ErrValidation) {
		t.Fatalf("enable bypassed validation: %v", err)
	}
	if repository.rules[1].Enabled {
		t.Fatal("invalid legacy rule was enabled")
	}
	// 停用是安全收敛动作，即便历史规则已不符合新目录，也必须仍然允许。
	if _, err := service.SetRuleEnabled(context.Background(), principal, "automations", 2, false); err != nil {
		t.Fatalf("disable invalid legacy rule: %v", err)
	}
	enabled, err := service.SetRuleEnabled(context.Background(), principal, "automations", 3, true)
	if err != nil {
		t.Fatalf("enable valid legacy rule: %v", err)
	}
	if !enabled.Enabled || enabled.Name != "历史合法自动化" || enabled.Trigger != EventTeamAssigned || enabled.Target != "team_lead" {
		t.Fatalf("re-enabled rule was not normalized and persisted: %+v", enabled)
	}
}
