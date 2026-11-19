package httpapi

import (
	"slices"
	"testing"

	"github.com/j-s-te/project-management/internal/platform"
)

// TestApprovalTaskNavigationIncludesApplicantsAndApprovers 验证审批参与者均有统一待办入口。
// 参数：t 为测试上下文。
// 返回值：无；审批角色缺入口或无关岗位被扩展工作区时失败。
func TestApprovalTaskNavigationIncludesApplicantsAndApprovers(t *testing.T) {
	for _, role := range []string{"team_lead", "technical_director", "project_manager", "admin", "system_admin"} {
		if !slices.Contains(navigationSections([]string{role}), "approvals") {
			t.Errorf("role %s missing approval workspace", role)
		}
	}
	for _, role := range []string{"engineer", "penetration_engineer", "device_admin"} {
		if slices.Contains(navigationSections([]string{role}), "approvals") {
			t.Errorf("unrelated role %s gained approval workspace", role)
		}
	}
}

// TestExtendedApprovalNavigationRequiresExistingCapability 验证新入口只使用既有业务能力。
// 参数：t 为测试上下文，能力由明确的原权限码提供。
// 返回值：无；缺少业务权限时扩大导航或隐藏有效入口均失败。
func TestExtendedApprovalNavigationRequiresExistingCapability(t *testing.T) {
	for _, test := range []struct {
		role, permission, section string
		native, wantApprovals     bool
	}{
		{"engineer", "project.deviation.report", "exceptions", false, true},
		{"quality_manager", "project.deviation.review", "exceptions", false, true},
		{"custom", "project.special_method.review", "methods", false, true},
		{"business_admin", "project.decomposition.manage", "projects", true, true},
		{"business_admin", "project.decomposition.manage", "projects", false, false},
		{"business_admin", "service_item.confirm", "methods", false, true},
		{"engineer", "project.read", "projects", false, false},
	} {
		p := platform.Principal{Roles: []string{test.role}, Permissions: map[string]bool{test.permission: true}}
		original := navigationSections([]string{test.role})
		sections := approvalNavigationSections(original, p, test.native)
		if !slices.Contains(sections, test.section) ||
			slices.Contains(sections, "approvals") != test.wantApprovals || sections[0] != original[0] {
			t.Errorf("role=%s permission=%s native=%v sections=%v",
				test.role, test.permission, test.native, sections)
		}
	}
}
