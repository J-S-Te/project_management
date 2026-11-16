package httpapi

import "testing"

// TestResourceMetricsNavigationKeepsRoleDefaults 用途：验证集中台账入口不改变岗位原默认工作区。
// 参数：t 为导航边界测试。
// 返回值：无；业务岗位缺入口或默认页变化时失败。
func TestResourceMetricsNavigationKeepsRoleDefaults(t *testing.T) {
	for role, first := range map[string]string{
		"admin": "dashboard", "system_admin": "dashboard", "project_manager": "dashboard",
		"technical_director": "dashboard", "device_admin": "equipment", "quality_manager": "implementation",
		"team_lead": "projects", "engineer": "projects", "penetration_engineer": "projects",
	} {
		sections := navigationSections([]string{role})
		found := false
		for _, section := range sections {
			if section == "resource-usage" {
				found = true
			}
		}
		if !found || len(sections) == 0 || sections[0] != first {
			t.Fatalf("%s资源入口或默认工作区异常：%v", role, sections)
		}
	}
	for _, role := range []string{"business_admin", "unknown"} {
		for _, section := range navigationSections([]string{role}) {
			if section == "resource-usage" {
				t.Fatalf("%s无关工作区被扩大", role)
			}
		}
	}
}
