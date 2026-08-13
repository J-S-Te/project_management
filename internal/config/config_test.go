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
	if _, err := Load(); err != nil {
		t.Fatal(err)
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
