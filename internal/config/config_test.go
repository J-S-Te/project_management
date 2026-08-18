package config

import (
	"strings"
	"testing"
)

func setValidEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"PM_HTTP_ADDR":                                ":8082",
		"MYSQL_DSN":                                   "project:secret@tcp(localhost:3306)/project_management?parseTime=true",
		"PLATFORM_BASE_URL":                           "http://localhost:8080",
		"OIDC_ISSUER":                                 "http://localhost:8080",
		"OIDC_BACKCHANNEL_BASE_URL":                   "",
		"OIDC_CLIENT_ID":                              "project_management-dev-web",
		"OIDC_CLIENT_SECRET":                          "secret",
		"OIDC_IDP_HINT":                               "basic-platform",
		"OIDC_REDIRECT_URI":                           "http://localhost:5173/project_management/auth/callback",
		"OIDC_POST_LOGOUT_REDIRECT_URI":               "http://localhost:5173/project_management/logged-out",
		"OIDC_TENANT_ID":                              "01J00000000000000000000000",
		"OIDC_SESSION_COOKIE_NAME":                    "project_management_session",
		"OIDC_SESSION_COOKIE_SECURE":                  "false",
		"OIDC_SESSION_ENCRYPTION_KEY_BASE64":          "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"OIDC_AUTHORIZATION_MAX_STALE":                "5m",
		"PLATFORM_APPLICATION_CODE":                   "project_management",
		"PLATFORM_ENVIRONMENT_CODE":                   "dev",
		"PLATFORM_AUDIT_CLIENT_ID":                    "",
		"PLATFORM_AUDIT_CLIENT_SECRET":                "",
		"PLATFORM_AUTHORIZATION_CATALOG_SYNC_ENABLED": "false",
		"CONTRACT_INTEGRATION_ENABLED":                "false",
		"CONTRACT_INTEGRATION_REQUIRE_BEARER":         "false",
		"DASHBOARD_MACHINE_ENABLED":                   "false",
		"DASHBOARD_MACHINE_REQUIRE_BEARER":            "false",
		"DASHBOARD_MACHINE_CLIENT_ID":                 "",
		"DASHBOARD_MACHINE_AUDIENCE":                  "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestLoadRequiresExplicitIssuer(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("OIDC_ISSUER", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OIDC_ISSUER") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsInvalidSessionEncryptionKey(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("OIDC_SESSION_ENCRYPTION_KEY_BASE64", "not-a-32-byte-key")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OIDC_SESSION_ENCRYPTION_KEY_BASE64") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadAcceptsValidPlatformIntegrationConfiguration(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDCIDPHint != "basic-platform" {
		t.Fatalf("OIDCIDPHint = %q", cfg.OIDCIDPHint)
	}
}

func TestLoadRejectsPlatformBaseURLWithPath(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("PLATFORM_BASE_URL", "https://platform.example.com/internal")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PLATFORM_BASE_URL") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsInvalidBackchannelAndLogoutURLs(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("OIDC_BACKCHANNEL_BASE_URL", "http://platform-api:8080/internal")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OIDC_BACKCHANNEL_BASE_URL") {
		t.Fatalf("backchannel error = %v", err)
	}

	setValidEnvironment(t)
	t.Setenv("OIDC_POST_LOGOUT_REDIRECT_URI", "javascript:alert(1)")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OIDC_POST_LOGOUT_REDIRECT_URI") {
		t.Fatalf("logout error = %v", err)
	}
}

func TestLoadRejectsInvalidSessionCookieName(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("OIDC_SESSION_COOKIE_NAME", "project session")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OIDC_SESSION_COOKIE_NAME") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRequiresKeycloakMachineCallerContractWhenBearerIsEnabled(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("CONTRACT_INTEGRATION_ENABLED", "true")
	t.Setenv("CONTRACT_INTEGRATION_REQUIRE_BEARER", "true")
	t.Setenv("CONTRACT_INTEGRATION_CLIENT_ID", "")
	t.Setenv("CONTRACT_INTEGRATION_AUDIENCE", "project_management-internal")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CONTRACT_INTEGRATION_CLIENT_ID") {
		t.Fatalf("error = %v", err)
	}

	setValidEnvironment(t)
	t.Setenv("CONTRACT_INTEGRATION_ENABLED", "false")
	t.Setenv("CONTRACT_INTEGRATION_REQUIRE_BEARER", "true")
	t.Setenv("CONTRACT_INTEGRATION_CLIENT_ID", "contract_management-integration")
	t.Setenv("CONTRACT_INTEGRATION_AUDIENCE", "project_management-internal")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CONTRACT_INTEGRATION_REQUIRE_BEARER") {
		t.Fatalf("error = %v", err)
	}

	// H4：启用内部投递后不得关闭来源校验（RequireBearer=false 必须被拒绝）。
	setValidEnvironment(t)
	t.Setenv("CONTRACT_INTEGRATION_ENABLED", "true")
	t.Setenv("CONTRACT_INTEGRATION_REQUIRE_BEARER", "false")
	t.Setenv("CONTRACT_INTEGRATION_CLIENT_ID", "contract_management-integration")
	t.Setenv("CONTRACT_INTEGRATION_AUDIENCE", "project_management-internal")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CONTRACT_INTEGRATION_REQUIRE_BEARER must be true") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRequiresVerifiedDashboardMachineCaller(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("DASHBOARD_MACHINE_ENABLED", "true")
	t.Setenv("DASHBOARD_MACHINE_REQUIRE_BEARER", "false")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DASHBOARD_MACHINE_REQUIRE_BEARER must be true") {
		t.Fatalf("error = %v", err)
	}

	setValidEnvironment(t)
	t.Setenv("DASHBOARD_MACHINE_ENABLED", "true")
	t.Setenv("DASHBOARD_MACHINE_REQUIRE_BEARER", "true")
	t.Setenv("DASHBOARD_MACHINE_CLIENT_ID", "")
	t.Setenv("DASHBOARD_MACHINE_AUDIENCE", "basic-platform-application")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DASHBOARD_MACHINE_CLIENT_ID") {
		t.Fatalf("error = %v", err)
	}

	setValidEnvironment(t)
	t.Setenv("DASHBOARD_MACHINE_ENABLED", "true")
	t.Setenv("DASHBOARD_MACHINE_REQUIRE_BEARER", "true")
	t.Setenv("DASHBOARD_MACHINE_CLIENT_ID", "data_analysis-machine")
	t.Setenv("DASHBOARD_MACHINE_AUDIENCE", "basic-platform-application")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !config.DashboardMachineEnabled || !config.DashboardMachineRequireBearer {
		t.Fatalf("dashboard machine config=%+v", config)
	}
}
