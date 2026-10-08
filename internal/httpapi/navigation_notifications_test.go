package httpapi

import "testing"

func TestPersonalNotificationsAvailableWithoutExpandingRoleWorkspace(t *testing.T) {
	for _, role := range []string{"admin", "system_admin", "business_admin", "team_lead", "technical_director", "project_manager", "device_admin", "quality_manager", "engineer", "penetration_engineer", "unknown"} {
		t.Run(role, func(t *testing.T) {
			sections := navigationSections([]string{role})
			count := 0
			for _, section := range sections {
				if section == "notifications" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("expected exactly one personal inbox: %v", sections)
			}
			if role == "device_admin" && sections[0] != "equipment" {
				t.Fatalf("default workspace changed: %v", sections)
			}
		})
	}
}
