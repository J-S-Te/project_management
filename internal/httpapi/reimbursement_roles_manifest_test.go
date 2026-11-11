package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/j-s-te/project-management/authz"
)

// TestSalesDirectorIsMinimumProjectDirectoryRole 验证销售总监独立目录角色不冒用业务管理员或扩大交付权限。
// 参数：t为测试上下文。
// 返回值：无；缺角色、版本未提升或权限过宽时失败。
func TestSalesDirectorIsMinimumProjectDirectoryRole(t *testing.T) {
	var manifest struct {
		Catalog string `json:"catalog_version"`
		Roles   []struct {
			Code        string   `json:"code"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(authz.PermissionManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, role := range manifest.Roles {
		if role.Code == "sales_director" {
			if manifest.Catalog != "13" || len(role.Permissions) != 1 || role.Permissions[0] != "project.expense_reference.eligible" {
				t.Fatal("sales role too broad", manifest.Catalog, role.Permissions)
			}
			return
		}
	}
	t.Fatal("sales_director missing; business_admin is not substitute")
}
