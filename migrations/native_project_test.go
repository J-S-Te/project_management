package migrations

import (
	"strings"
	"testing"
)

// TestNativeProjectMigrationKeepsContractAuthority 检查来源分型、空合同不适用和不可变范围的前向结构。
// 参数：t 为测试上下文。
// 返回值：无；静态结构缺失时失败，不冒充实际MySQL迁移验证。
func TestNativeProjectMigrationKeepsContractAuthority(t *testing.T) {
	body, err := Files.ReadFile("000040_project_native_source.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"DEFAULT 'CONTRACT'", "WHEN source_kind = 'CONTRACT'", "ELSE NULL END", "uq_pm_project_contract_identity (tenant_id, contract_identity_key)",
		"uq_pm_project_native_source (tenant_id, native_source_key)", "contract='' AND contract_id='' AND contract_version=''", "CREATE TABLE pm_native_project_command", "CREATE TABLE pm_native_counterparty", "CREATE TABLE pm_native_scope_version", "PRIMARY KEY (tenant_id,project_id,version)"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("missing native source boundary %q", required)
		}
	}
	if strings.Contains(sql, "UPDATE pm_project") || strings.Contains(sql, "DELETE FROM") {
		t.Fatal("native migration must not rewrite existing projects")
	}
}
