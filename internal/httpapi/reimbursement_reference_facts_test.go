package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/j-s-te/project-management/authz"
)

// TestExpenseParticipantRoleRemainsMinimum 验证费用参与入口不扩大项目详情或交付权限。
// 参数：t为测试上下文。
// 返回值：无；最小角色新增project.read或其他权限时失败。
func TestExpenseParticipantRoleRemainsMinimum(t *testing.T) {
	var manifest struct {
		Roles []struct {
			Code        string   `json:"code"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(authz.PermissionManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, role := range manifest.Roles {
		if role.Code == "expense_participant" {
			if len(role.Permissions) != 1 || role.Permissions[0] != "project.expense_reference.eligible" {
				t.Fatal("expense role widened", role.Permissions)
			}
			return
		}
	}
	t.Fatal("expense participant role missing")
}
