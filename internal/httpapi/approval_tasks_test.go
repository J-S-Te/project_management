package httpapi

import (
	"slices"
	"testing"
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
