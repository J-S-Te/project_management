package httpapi_test

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/j-s-te/project-management/authz"
)

// 授权清单是鉴权的单一事实来源：路由要求的权限码必须都在清单里声明，
// 角色也不能引用未声明的权限码——否则该功能对任何角色都不可用，
// 或者平台下发的权限码会被 validateKnownSet 判为未知而导致会话校验失败。
func TestPermissionManifestCoversEveryRoutePermission(t *testing.T) {
	var manifest struct {
		CatalogVersion string `json:"catalog_version"`
		Permissions    []struct {
			Code string `json:"code"`
		} `json:"permissions"`
		Roles []struct {
			Code        string   `json:"code"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(authz.PermissionManifest, &manifest); err != nil {
		t.Fatalf("decode permission manifest: %v", err)
	}
	if version, err := strconv.Atoi(manifest.CatalogVersion); err != nil || version <= 0 {
		t.Fatalf("catalog_version = %q, want a positive integer", manifest.CatalogVersion)
	}

	declared := map[string]bool{}
	for _, permission := range manifest.Permissions {
		if permission.Code == "" || permission.Code == "all" || declared[permission.Code] {
			t.Fatalf("权限码 %q 为空、为通配或重复声明", permission.Code)
		}
		declared[permission.Code] = true
	}
	for _, role := range manifest.Roles {
		for _, code := range role.Permissions {
			if !declared[code] {
				t.Fatalf("角色 %s 引用了未声明的权限码 %q", role.Code, code)
			}
		}
	}

	source, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("read router source: %v", err)
	}
	routeCodes := map[string]bool{}
	for _, match := range regexp.MustCompile(`require(?:Any)?\(([^)]*)\)`).FindAllStringSubmatch(string(source), -1) {
		for _, quoted := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(match[1], -1) {
			routeCodes[quoted[1]] = true
		}
	}
	if len(routeCodes) == 0 {
		t.Fatal("未能从路由中解析出任何权限码，测试本身需要更新")
	}
	missing := make([]string, 0)
	for code := range routeCodes {
		if !declared[code] {
			missing = append(missing, code)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("路由要求但清单未声明的权限码：%v", missing)
	}
}
