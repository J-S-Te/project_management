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
