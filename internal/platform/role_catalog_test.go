package platform

import (
	"strings"
	"testing"
)

// 字段级权限的角色下拉直接消费这份目录：目录必须与授权时使用的角色集合完全一致，
// 否则界面能选到的角色码会被规则接口照单全收却永远不命中任何主体。
func TestRoleCatalogMatchesAuthorizationCatalog(t *testing.T) {
	roles, err := RoleCatalog()
	if err != nil {
		t.Fatalf("load role catalog: %v", err)
	}
	catalog, err := LoadAuthorizationCatalog()
	if err != nil {
		t.Fatalf("load authorization catalog: %v", err)
	}
	if len(roles) != len(catalog.roles) {
		t.Fatalf("role catalog size = %d, authorization catalog size = %d", len(roles), len(catalog.roles))
	}
	for _, role := range roles {
		if role.Code == "" || role.Code != strings.TrimSpace(role.Code) {
			t.Fatalf("role code %q is not canonical", role.Code)
		}
		if role.Name == "" {
			t.Fatalf("role %q has no display name", role.Code)
		}
		if _, ok := catalog.roles[role.Code]; !ok {
			t.Fatalf("role %q is not a known authorization role", role.Code)
		}
	}
	// 界面按 manifest 声明顺序展示（管理员在最前），顺序是契约的一部分，不是随机顺序。
	if roles[0].Code != "admin" {
		t.Fatalf("first role = %q, want admin", roles[0].Code)
	}
	if roles[0].Name == roles[0].Code {
		t.Fatalf("role %q has no Chinese display name", roles[0].Code)
	}
}
