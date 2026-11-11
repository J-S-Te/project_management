package httpapi_test

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
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

// TestFieldExecutionPermissionBelongsToProjectManagerAndEngineers 锁定现场执行权限矩阵与当前目录版本。
// 参数：t 为测试上下文。
// 返回值：无；现场执行权被收回、被下发给非现场角色，或工程师越过审批/发布/配置边界时失败。
func TestFieldExecutionPermissionBelongsToProjectManagerAndEngineers(t *testing.T) {
	var manifest struct {
		CatalogVersion       string `json:"catalog_version"`
		ClaimsRoleConfigHash string `json:"claims_role_config_hash"`
		Roles                []struct {
			Code        string   `json:"code"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(authz.PermissionManifest, &manifest); err != nil {
		t.Fatalf("decode permission manifest: %v", err)
	}
	if manifest.CatalogVersion != "12" {
		t.Fatalf("catalog_version=%q, want 12 for engineer field execution", manifest.CatalogVersion)
	}
	// 角色配置哈希必须按 `<应用>-v<目录版本>-<变更>` 命名，平台据此判断授权目录是否需要重新发布。
	hashPrefix := "project-management-v" + manifest.CatalogVersion + "-"
	if !strings.HasPrefix(manifest.ClaimsRoleConfigHash, hashPrefix) || len(manifest.ClaimsRoleConfigHash) == len(hashPrefix) {
		t.Fatalf("claims_role_config_hash=%q, want prefix %q with a change suffix", manifest.ClaimsRoleConfigHash, hashPrefix)
	}

	roles := make(map[string]map[string]bool, len(manifest.Roles))
	for _, role := range manifest.Roles {
		permissions := make(map[string]bool, len(role.Permissions))
		for _, permission := range role.Permissions {
			permissions[permission] = true
		}
		roles[role.Code] = permissions
	}
	if len(roles) == 0 {
		t.Fatal("permission manifest has no roles")
	}
	for _, role := range manifest.Roles {
		for _, code := range role.Permissions {
			if code == "" || code != strings.TrimSpace(code) {
				t.Fatalf("角色 %s 的权限码 %q 含空白", role.Code, code)
			}
		}
	}

	// 现场执行权的持有者只有项目经理与两个工程师岗位：现场实施按指派受理，
	// 计划、审批、报告与配置不属于这两个工程师岗位。现场完成另保留管理员兜底，
	// 用于指派关系缺失时关闭项目，语义与现场执行不同。
	expectedHolders := map[string]map[string]bool{
		"project.field.execute": {"project_manager": true, "engineer": true, "penetration_engineer": true},
		"project.field.complete": {
			"project_manager":      true,
			"engineer":             true,
			"penetration_engineer": true,
			"admin":                true,
			"system_admin":         true,
		},
	}
	for _, permission := range []string{"project.field.execute", "project.field.complete"} {
		want := expectedHolders[permission]
		for code, granted := range roles {
			if granted[permission] != want[code] {
				t.Fatalf("role %s %s=%v, want %v", code, permission, granted[permission], want[code])
			}
		}
	}

	// 工程师岗位的最小权限集：查看、现场执行、现场完成与偏离上报。
	// 任何新增权限都必须显式改这份期望，避免管理类权限被顺手放宽。
	engineerPermissions := []string{"project.read", "project.field.execute", "project.deviation.report", "project.field.complete"}
	engineerBoundaries := map[string][]string{
		"engineer":             engineerPermissions,
		"penetration_engineer": engineerPermissions,
	}
	for code, expected := range engineerBoundaries {
		granted := roles[code]
		if len(granted) != len(expected) {
			t.Fatalf("role %s permissions=%v, want exactly %v", code, sortedKeys(granted), expected)
		}
		for _, permission := range expected {
			if !granted[permission] {
				t.Fatalf("role %s missing %s, want %v", code, permission, expected)
			}
		}
	}

	// 审批、报告发布与规则配置类权限被误授予工程师岗位时，直接失败。
	denied := []string{
		"project.create",
		"service_item.confirm",
		"project_rule.manage",
		"project.capability_code.manage",
		"project.field_permission.manage",
		"project.resource.manage",
		"project.device.manage",
		"project.expense_participant.manage",
		"project.contract.import",
		"project.decomposition.manage",
		"project.team.assign",
		"project.team.revoke",
		"project.execution.assign",
		"project.execution.revoke",
		"project.implementation.plan",
		"project.implementation.revoke",
		"project.rollback.request",
		"project.rollback.approve",
		"project.native_scope.approve",
		"project.deviation.review",
		"project.special_method.review",
		"project.report.manage",
		"project.report.prepare",
		"project.report.review",
		"project.report.issue",
		"project.report.archive",
		"project.report.correction.request",
		"project.report.correction.approve",
	}
	for code := range engineerBoundaries {
		for _, permission := range denied {
			if roles[code][permission] {
				t.Fatalf("role %s must not own %s: engineers execute the field work, they do not approve, issue or configure", code, permission)
			}
		}
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key, ok := range values {
		if ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
