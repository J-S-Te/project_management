package migrations

import (
	"strings"
	"testing"
)

func TestOIDCSessionMigrationPreservesReplayAuditAndIdentityRevocationIndex(t *testing.T) {
	body, err := Files.ReadFile("000003_project_oidc_sessions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"state_hash BINARY(32)", "consumed_at DATETIME(3) NULL", "session_id_hash BINARY(32)", "principal_json JSON", "id_token_ciphertext MEDIUMBLOB", "oauth_token_ciphertext MEDIUMBLOB", "idx_pm_oidc_session_subject (tenant_id, identity_id, revoked_at)"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestProjectScopeMigrationAddsStableOwnerBoundaries(t *testing.T) {
	body, err := Files.ReadFile("000004_project_data_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"owner_org_id", "owner_identity_id", "manager_identity_id", "idx_pm_project_tenant_owner_org", "idx_pm_project_tenant_owner_identity"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestBackchannelLogoutMigrationAddsSIDAndReplayBoundary(t *testing.T) {
	body, err := Files.ReadFile("000006_oidc_backchannel_logout.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"oidc_subject VARCHAR(128)", "oidc_session_id VARCHAR(128)",
		"idx_pm_oidc_session_sid", "pm_oidc_backchannel_logout_replay", "PRIMARY KEY (jti)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("back-channel logout migration missing %q", required)
		}
	}
}

func TestServiceItemSystemLevelMigration(t *testing.T) {
	body, err := Files.ReadFile("000007_project_service_system_level.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ADD COLUMN system_level VARCHAR(64) NOT NULL DEFAULT ''") {
		t.Fatal("system level migration is missing")
	}
}

func TestProjectGovernanceMigrationAddsReviewReportAndComplianceStructures(t *testing.T) {
	body, err := Files.ReadFile("000008_project_governance.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		// 服务项治理列必须幂等补齐：先查 information_schema 再执行 ALTER，
		// 因为 MySQL 的 DDL 会隐式提交，早期失败的库可能已经加过这些列。
		"information_schema.columns",
		"ADD COLUMN tech_review_status VARCHAR(32) NOT NULL DEFAULT ''NONE'' AFTER conflict_status",
		"ADD COLUMN report_status VARCHAR(32) NOT NULL DEFAULT ''NONE'' AFTER tech_review_comment",
		"CREATE TABLE IF NOT EXISTS pm_impl_plan",
		"auth_doc_no VARCHAR(128)",
		"auth_scope TEXT",
		"emergency_contact VARCHAR(128)",
		"rollback_plan TEXT",
		// TEXT 列不能用字面量默认值（MySQL 1101），必须使用表达式默认值。
		"penetration_test_plan TEXT NOT NULL DEFAULT ('')",
		"CREATE TABLE IF NOT EXISTS pm_split_rule",
		"CREATE TABLE IF NOT EXISTS pm_warning_rule",
		"CREATE TABLE IF NOT EXISTS pm_automation",
		"CREATE TABLE IF NOT EXISTS pm_field_permission",
		"CREATE TABLE IF NOT EXISTS pm_sla",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("governance migration missing %q", required)
		}
	}
	if strings.Contains(sql, "TEXT NOT NULL DEFAULT ''") {
		t.Fatal("governance migration uses a literal TEXT default, which MySQL rejects with error 1101")
	}
}

// 人员资质档案必须能回基础平台复核：identity_status 记录复核结论，
// identity_checked_at 记录复核时间；未关联平台账号的历史档案保持 UNLINKED。
func TestPersonnelIdentityMigrationAddsReviewColumns(t *testing.T) {
	body, err := Files.ReadFile("000013_personnel_identity_check.sql")
	if err != nil {
		t.Fatal(err)
	}
	content := string(body)
	for _, expected := range []string{
		"ALTER TABLE pm_capability ADD COLUMN identity_status VARCHAR(16) NOT NULL DEFAULT 'UNLINKED'",
		"ALTER TABLE pm_capability ADD COLUMN identity_checked_at DATETIME(3) NULL",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("migration missing %q:\n%s", expected, content)
		}
	}
}

// 站点台账：site 原本只是从合同复制的自由文本，既无法聚合也没有坐标；
// pm_site 把它提升为主数据，坐标用可空列区分"未采集"与"坐标为 0"。
func TestSiteRegistryMigrationCreatesCoordinateAwareTable(t *testing.T) {
	body, err := Files.ReadFile("000014_site_registry.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS pm_site",
		"site_code VARCHAR(64) NOT NULL",
		"latitude DECIMAL(10,7) NULL",
		"longitude DECIMAL(10,7) NULL",
		"UNIQUE KEY uq_pm_site_tenant_code (tenant_id, site_code)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

// 合同拆解规则配置 v2：三块配置表 + 服务项体系要求字段 + 存量自由文本规则清理。
func TestSplitRuleConfigMigration(t *testing.T) {
	body, err := Files.ReadFile("000017_split_rule_config_v2.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"pm_split_policy", "pm_detection_category", "pm_split_override",
		"dimension_primary", "dimension_secondary", "default_status",
		"missing_rule_action", "scope_change_detection",
		"required_qualifications", "special_method",
		"match_conditions JSON", "override_settings JSON", "priority INT",
		"ADD COLUMN system_standard VARCHAR(128) NOT NULL DEFAULT ''",
		"DELETE FROM pm_split_rule WHERE kind = 'split-rules'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("split rule config migration missing %q", required)
		}
	}
	// 已应用的迁移不得再改：第三维用独立迁移追加。
	tertiary, err := Files.ReadFile("000018_split_policy_tertiary_dimension.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tertiary), "dimension_tertiary VARCHAR(32) NOT NULL DEFAULT ''") {
		t.Fatal("tertiary dimension migration is missing")
	}
	// 必检能力码：检测类别域的「必备资质」是资质名称，能力校验比对的是能力码，
	// 两者不是同一套编码，因此单独给出能力码列（默认留空，避免凭空造码导致分配一律冲突）。
	codes, err := Files.ReadFile("000019_detection_category_required_codes.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codes), "required_codes VARCHAR(255) NOT NULL DEFAULT ''") {
		t.Fatal("required codes migration is missing")
	}
}

func TestRuleConfigurationConstraintMigrationPreservesHistoryAndPreventsActiveDuplicates(t *testing.T) {
	body, err := Files.ReadFile("000023_rule_configuration_constraints.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"UPDATE pm_automation AS duplicate_rule",
		"uq_pm_automation_active_condition",
		"UPDATE pm_field_permission AS duplicate_rule",
		"uq_pm_field_permission_active_condition",
		"UPDATE pm_sla AS duplicate_rule",
		"uq_pm_sla_active_status",
		"GENERATED ALWAYS AS (CASE WHEN enabled = TRUE",
		"duplicate_rule.updated_by = 'migration-000023'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("rule constraint migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "DELETE FROM") {
		t.Fatal("duplicate rule migration must preserve historical rows instead of deleting them")
	}
}

func TestContractIdentityMigrationUsesStableIDForIdempotency(t *testing.T) {
	body, err := Files.ReadFile("000024_contract_identity_key.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"DROP INDEX uq_pm_project_contract_version",
		"GENERATED ALWAYS AS (CASE WHEN contract_id <> '' THEN contract_id ELSE contract END) STORED",
		"ADD UNIQUE KEY uq_pm_project_contract_version (tenant_id, contract_identity_key, contract_version)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("contract identity migration missing %q", required)
		}
	}
}

// 资质/能力编码使用独立的租户目录，并从存量能力档案回填，
// 避免上线后旧编码因未手工重录而立即失效。
func TestCapabilityCodeCatalogMigration(t *testing.T) {
	body, err := Files.ReadFile("000021_capability_code_catalog.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS pm_capability_code",
		"scope VARCHAR(128) NOT NULL",
		"check_type VARCHAR(16) NOT NULL",
		"UNIQUE KEY uk_pm_capability_code_tenant_type_code (tenant_id, check_type, scope)",
		"JOIN JSON_TABLE(",
		"capability.capability_codes",
		"UPPER(TRIM(code_row.code))",
		"UPPER(TRIM(capability.resource_type)) IN ('PERSON', 'EQUIPMENT')",
		"FROM pm_service_item AS service_item",
		"COALESCE(service_item.required_codes, JSON_ARRAY())",
		"'PERSON'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("capability code migration missing %q", required)
		}
	}
}
