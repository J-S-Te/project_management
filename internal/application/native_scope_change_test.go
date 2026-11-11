package application

import (
	"encoding/json"
	"github.com/j-s-te/project-management/authz"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"testing"
)

// TestNativeScopeFingerprintCoversCompleteScope 固定范围摘要覆盖方法、要求、能力和稳定行标识。
// 参数：t 为测试上下文。
// 返回值：无；遗漏权威业务字段时失败。
func TestNativeScopeFingerprintCoversCompleteScope(t *testing.T) {
	base := []domain.ServiceItem{{SourceServiceID: "line-1", Batch: "一", Site: "场所", Category: "类别", TestMode: "STANDARD", RequiredCodes: []string{"A"}}}
	original := NativeScopeFingerprint(base)
	for _, edit := range []func(*domain.ServiceItem){
		func(i *domain.ServiceItem) { i.Requirement = "新要求" },
		func(i *domain.ServiceItem) { i.TestMode = "PENETRATION" },
		func(i *domain.ServiceItem) { i.RequiredCodes = []string{"B"} },
		func(i *domain.ServiceItem) { i.SourceServiceID = "line-2" },
		func(i *domain.ServiceItem) { i.SystemStandard = "标准" },
	} {
		changed := append([]domain.ServiceItem(nil), base...)
		edit(&changed[0])
		if NativeScopeFingerprint(changed) == original {
			t.Fatal("scope change omitted from fingerprint")
		}
	}
	base[0].Status = "待分配"
	base[0].ID = "new-row"
	if NativeScopeFingerprint(base) != original {
		t.Fatal("fulfillment state changed immutable scope")
	}
}

// TestNativeScopeRolesAreSeparated 固定发起和批准分别需要真实权限与精确岗位。
// 参数：t 为测试上下文。
// 返回值：无；平台运维或普通员工得到业务审批能力时失败。
func TestNativeScopeRolesAreSeparated(t *testing.T) {
	for _, role := range []string{"business_admin", "admin", "technical_director", "system_admin", "engineer"} {
		p := platform.Principal{Roles: []string{role}, Permissions: map[string]bool{"project.decomposition.manage": true, "project.native_scope.approve": true}}
		if mayRequestNativeScope(p) != (role == "business_admin") {
			t.Fatalf("request role %s", role)
		}
		if mayApproveNativeScope(p) != (role == "admin" || role == "technical_director") {
			t.Fatalf("approval role %s", role)
		}
	}
}

// TestNativeScopeApprovalCatalogMapping 验证清单仅向已批准岗位新增审批权限。
// 参数：t 为测试上下文。
// 返回值：无；运维、业务发起人或其他岗位得到审批权限时失败。
func TestNativeScopeApprovalCatalogMapping(t *testing.T) {
	var manifest struct {
		CatalogVersion string `json:"catalog_version"`
		Roles          []struct {
			Code        string   `json:"code"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(authz.PermissionManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.CatalogVersion != "13" {
		t.Fatalf("catalog=%s", manifest.CatalogVersion)
	}
	for _, role := range manifest.Roles {
		has := false
		for _, permission := range role.Permissions {
			if permission == "project.native_scope.approve" {
				has = true
			}
		}
		if has != (role.Code == "technical_director" || role.Code == "admin") {
			t.Fatalf("approval mapping %s=%v", role.Code, has)
		}
	}
}
