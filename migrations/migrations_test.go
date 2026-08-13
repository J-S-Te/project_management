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
