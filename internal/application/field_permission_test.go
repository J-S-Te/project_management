package application

import (
	"context"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// 字段级脱敏必须覆盖每一条读路径。事件流 payload 里带着指派快照与拆解快照，
// 只处理项目/服务项列表会让 hidden 配置从 /delivery-events 整条漏出去。
func TestDeliveryEventsHonourFieldPermissions(t *testing.T) {
	principal := platform.Principal{
		TenantID: "tenant-1", UserID: "user-1", IdentityID: "user-1",
		Roles:       []string{"engineer"},
		Permissions: map[string]bool{"project.read": true},
		DataScopes:  []platform.DataScope{{RoleCode: "engineer", ScopeType: "APPLICATION"}},
	}
	repo := &hookRepository{rules: []domain.Rule{
		{Kind: "permissions", Enabled: true, RoleCode: "engineer", FieldName: "team_lead_id", AccessLevel: "hidden"},
		{Kind: "permissions", Enabled: true, RoleCode: "engineer", FieldName: "site", AccessLevel: "hidden"},
	}}
	service := Service{Repo: repo}
	events := []domain.DeliveryEvent{
		{ID: "e1", Type: EventTeamAssigned, Payload: map[string]any{"team_lead_id": "TL-9", "note": "保留"}},
		{ID: "e2", Type: EventDecompositionAdjusted, Payload: map[string]any{
			"reason": "补充协议",
			"service_items": []any{
				map[string]any{"site": "杭州机房", "category": "等保测评"},
			},
		}},
	}
	masked, err := service.applyFieldPermissionsToEvents(context.Background(), principal, events)
	if err != nil {
		t.Fatalf("applyFieldPermissionsToEvents failed: %v", err)
	}
	if got := masked[0].Payload["team_lead_id"]; got != maskedFieldValue {
		t.Fatalf("team_lead_id must be masked, got %v", got)
	}
	if got := masked[0].Payload["note"]; got != "保留" {
		t.Fatalf("non-hidden keys must survive, got %v", got)
	}
	snapshot, _ := masked[1].Payload["service_items"].([]any)
	if len(snapshot) != 1 {
		t.Fatalf("service_items snapshot lost: %+v", masked[1].Payload)
	}
	entry, _ := snapshot[0].(map[string]any)
	if entry["site"] != maskedFieldValue {
		t.Fatalf("nested snapshot must be masked too, got %+v", entry)
	}
	if entry["category"] != "等保测评" {
		t.Fatalf("non-hidden nested keys must survive, got %+v", entry)
	}
	if masked[1].Payload["reason"] != "补充协议" {
		t.Fatalf("event metadata must survive, got %+v", masked[1].Payload)
	}
}

// 未配置 hidden 规则时读路径零改动，事件保持原样。
func TestDeliveryEventsPassThroughWithoutHiddenRules(t *testing.T) {
	principal := platform.Principal{TenantID: "tenant-1", UserID: "user-1", Roles: []string{"engineer"}}
	service := Service{Repo: &hookRepository{}}
	events := []domain.DeliveryEvent{{ID: "e1", Payload: map[string]any{"team_lead_id": "TL-9"}}}
	masked, err := service.applyFieldPermissionsToEvents(context.Background(), principal, events)
	if err != nil {
		t.Fatalf("applyFieldPermissionsToEvents failed: %v", err)
	}
	if masked[0].Payload["team_lead_id"] != "TL-9" {
		t.Fatalf("no rules means no masking, got %+v", masked[0].Payload)
	}
}

// 指派关系本身也是敏感字段：隐藏配置必须同时作用于服务项读路径。
func TestServiceItemAssignmentFieldsAreMaskable(t *testing.T) {
	item := domain.ServiceItem{TeamLeadID: "TL-9", ProjectManagerID: "PM-9", EngineerIDs: []string{"E-1", "E-2"}}
	for _, field := range []string{"team_lead_id", "project_manager_id", "engineer_ids"} {
		if _, ok := maskableServiceItemFields[field]; !ok {
			t.Fatalf("%s must be configurable as a hidden field", field)
		}
		maskServiceItemField(&item, field)
	}
	if item.TeamLeadID != maskedFieldValue || item.ProjectManagerID != maskedFieldValue {
		t.Fatalf("assignment relations must be masked: %+v", item)
	}
	if len(item.EngineerIDs) != 1 || item.EngineerIDs[0] != maskedFieldValue {
		t.Fatalf("engineer list must be masked: %+v", item.EngineerIDs)
	}
}

// 报告归档权与报告编制权必须分开：现场执行角色不应顺带获得"把项目推向已完成"的能力。
func TestReportArchiveRequiresItsOwnPermission(t *testing.T) {
	cases := map[string]string{
		"COMPILING": "project.report.manage",
		"REVIEWED":  "project.report.manage",
		"ISSUED":    "project.report.manage",
		"ARCHIVED":  "project.report.archive",
		"archived":  "project.report.archive",
	}
	for phase, want := range cases {
		if got := reportPhasePermission(phase); got != want {
			t.Fatalf("reportPhasePermission(%q) = %q, want %q", phase, got, want)
		}
	}
	if reportPhasePermission("ARCHIVED") == reportPhasePermission("ISSUED") {
		t.Fatal("归档与签发必须要求不同权限")
	}
}

// 预警规则必须真正消费 check_type 与 threshold：否则"配了规则就无差别触发"，
// 配置与运行时行为无关。
func TestWarningRulesConsumeCheckTypeAndThreshold(t *testing.T) {
	principal := platform.Principal{TenantID: "tenant-1", UserID: "user-1"}
	conflicts := []string{"缺少能力：ISO27001", "资源 E-1 缺少有效资质/能力记录"}

	t.Run("check_type 限定类别，不匹配则不告警", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Name: "场地冲突预警", CheckType: "场地冲突"}}}
		service := Service{Repo: repo}
		service.fireConflictWarning(context.Background(), principal, "SI-1", conflicts)
		if len(repo.events) != 0 {
			t.Fatalf("non-matching check_type must not warn: %+v", repo.events)
		}
	})

	t.Run("check_type 命中类别才告警，并带出规则名", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Name: "资质能力预警", CheckType: "资质能力冲突"}}}
		service := Service{Repo: repo}
		service.fireConflictWarning(context.Background(), principal, "SI-1", conflicts)
		if len(repo.events) != 1 || repo.events[0].Type != EventWarningTriggered {
			t.Fatalf("matching check_type must warn once: %+v", repo.events)
		}
		rules, _ := repo.events[0].Payload["rules"].([]string)
		if len(rules) != 1 || rules[0] != "资质能力预警" {
			t.Fatalf("warning payload must name the fired rule: %+v", repo.events[0].Payload)
		}
		// 只保留命中该规则的那一条冲突，而不是把全部冲突都塞进去。
		matched, _ := repo.events[0].Payload["conflicts"].([]string)
		if len(matched) != 1 || !strings.Contains(matched[0], "资质") {
			t.Fatalf("warning payload must carry only matching conflicts: %+v", repo.events[0].Payload)
		}
	})

	t.Run("threshold 高于命中数量时不告警", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Name: "连续三项", Threshold: "连续 3 项冲突"}}}
		service := Service{Repo: repo}
		service.fireConflictWarning(context.Background(), principal, "SI-1", conflicts)
		if len(repo.events) != 0 {
			t.Fatalf("threshold 3 with 2 conflicts must not warn: %+v", repo.events)
		}
	})

	t.Run("threshold 达到时告警并回填阈值", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: true, Name: "任意两项", Threshold: "2"}}}
		service := Service{Repo: repo}
		service.fireConflictWarning(context.Background(), principal, "SI-1", conflicts)
		if len(repo.events) != 1 {
			t.Fatalf("threshold 2 with 2 conflicts must warn: %+v", repo.events)
		}
		if got := repo.events[0].Payload["threshold"]; got != 2 {
			t.Fatalf("payload must record the threshold, got %v", got)
		}
	})

	t.Run("停用的规则不参与判定", func(t *testing.T) {
		repo := &hookRepository{rules: []domain.Rule{{Enabled: false, Name: "停用"}}}
		service := Service{Repo: repo}
		service.fireConflictWarning(context.Background(), principal, "SI-1", conflicts)
		if len(repo.events) != 0 {
			t.Fatalf("disabled rule must not warn: %+v", repo.events)
		}
	})
}

func TestWarningThresholdParsesConfiguredText(t *testing.T) {
	cases := map[string]int{"": 1, "3": 3, "连续 3 项冲突": 3, "abc": 1, "0": 1}
	for raw, want := range cases {
		if got := warningThreshold(domain.Rule{Threshold: raw}); got != want {
			t.Fatalf("warningThreshold(%q) = %d, want %d", raw, got, want)
		}
	}
}

// 字段级脱敏是安全策略，必须与运营参数分开授权：只持有 project_rule.manage 的角色
// 不得修改脱敏配置，而规则按 kind 选表，因此按 kind 判定权限是可靠的。
func TestRuleKindPermissionSeparatesFieldMasking(t *testing.T) {
	if got := ruleKindPermission("permissions"); got != "project.field_permission.manage" {
		t.Fatalf("字段级脱敏应要求独立权限，got %q", got)
	}
	for _, kind := range []string{"split-rules", "warning-rules", "automations", "sla", "standards", ""} {
		if got := ruleKindPermission(kind); got != "project_rule.manage" {
			t.Fatalf("%s 仍应由 project_rule.manage 管理，got %q", kind, got)
		}
	}
	// 大写/空白不应绕过判定。
	if got := ruleKindPermission(" Permissions "); got != "project.field_permission.manage" {
		t.Fatalf("规范化后仍须命中脱敏权限，got %q", got)
	}
}
