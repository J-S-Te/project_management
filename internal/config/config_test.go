package config

import (
	"strings"
	"testing"
	"time"
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
		"TEMPORAL_WORKER_BUILD_ID":                    "project-worker-v1",
		"TEMPORAL_WORKER_DEPLOYMENT_NAME":             "project-management",
		"TEMPORAL_WORKER_VERSIONING_ENABLED":          "true",
		"TEMPORAL_WORKER_VERSIONING_POLICY":           "PINNED",
		"TEMPORAL_METRICS_ADDRESS":                    ":9092",
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
	if !cfg.TemporalWorkerVersioning || cfg.TemporalWorkerDeploymentName != "project-management" || cfg.TemporalWorkerBuildID == "" || cfg.TemporalWorkerVersioningPolicy != "PINNED" || cfg.TemporalMetricsAddress != ":9092" {
		t.Fatalf("Temporal worker defaults = %+v", cfg)
	}
}

func TestLoadRejectsInvalidTemporalWorkerVersioningPolicy(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("TEMPORAL_WORKER_VERSIONING_POLICY", "LATEST")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TEMPORAL_WORKER_VERSIONING_POLICY") {
		t.Fatalf("error = %v", err)
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

func TestLoadRequiresPlatformMachineCallerContractWhenBearerIsEnabled(t *testing.T) {
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

	setValidEnvironment(t)
	t.Setenv("CONTRACT_INTEGRATION_ENABLED", "true")
	t.Setenv("CONTRACT_INTEGRATION_REQUIRE_BEARER", "true")
	t.Setenv("CONTRACT_INTEGRATION_CLIENT_ID", "contract_management-dev-project-integration")
	t.Setenv("CONTRACT_INTEGRATION_AUDIENCE", "basic-platform-application")
	t.Setenv("CONTRACT_INTEGRATION_REQUIRED_SCOPE", "PENDING_ONBOARDING")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CONTRACT_INTEGRATION_REQUIRED_SCOPE") {
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
	t.Setenv("DASHBOARD_MACHINE_ISSUER", "basic-platform")
	t.Setenv("DASHBOARD_MACHINE_PUBLIC_KEY_PATH", "/tmp/application-jwt-public.pem")
	t.Setenv("DASHBOARD_MACHINE_CALLER_APPLICATION_CODE", "data_analysis")
	t.Setenv("DASHBOARD_MACHINE_CALLER_ENVIRONMENT_CODE", "test")
	t.Setenv("DASHBOARD_MACHINE_REQUIRED_SCOPE", "dashboard.project.read")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !config.DashboardMachineEnabled || !config.DashboardMachineRequireBearer {
		t.Fatalf("dashboard machine config=%+v", config)
	}
}

func TestLoadRequiresCompleteFileGatewayConfiguration(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("FILE_GATEWAY_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "FILE_GATEWAY_") {
		t.Fatalf("error=%v", err)
	}
	setValidEnvironment(t)
	t.Setenv("FILE_GATEWAY_ENABLED", "true")
	t.Setenv("FILE_GATEWAY_BASE_URL", "http://file-gateway:8086")
	t.Setenv("FILE_GATEWAY_APPLICATION_ID", "app-1")
	t.Setenv("FILE_GATEWAY_CLIENT_ID", "client")
	t.Setenv("FILE_GATEWAY_CLIENT_SECRET", "secret")
	t.Setenv("FILE_GATEWAY_SCOPE", "platform:file:upload platform:file:bind platform:file:download")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !config.FileGatewayEnabled {
		t.Fatal("file gateway should be enabled")
	}
}

// TestSettlementReferenceRequiresIndependentMachineConfig 验证启用引用时不能使用缺失机器身份。
// 参数：t 为测试上下文。
// 返回值：无。
func TestSettlementReferenceRequiresIndependentMachineConfig(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("SETTLEMENT_REFERENCE_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SETTLEMENT_REFERENCE") {
		t.Fatalf("缺失机器配置未拒绝：%v", err)
	}
}

// setSettlementReferenceEnvironment 填充结算引用除 client id 外的完整信任绑定。
func setSettlementReferenceEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("SETTLEMENT_REFERENCE_ENABLED", "true")
	t.Setenv("SETTLEMENT_REFERENCE_AUDIENCE", "basic-platform-application")
	t.Setenv("SETTLEMENT_REFERENCE_ISSUER", "basic-platform")
	t.Setenv("SETTLEMENT_REFERENCE_PUBLIC_KEY_PATH", "/app/data/keys/jwt-ed25519-public.pem")
	t.Setenv("SETTLEMENT_REFERENCE_CALLER_ENVIRONMENT_CODE", "prod")
}

// TestSettlementReferenceAllowAnyClientAcceptsEmptyClientID 验证解耦后 client id 可为空，
// 但 issuer/audience/public key/caller env 仍必须完整。
func TestSettlementReferenceAllowAnyClientAcceptsEmptyClientID(t *testing.T) {
	setValidEnvironment(t)
	setSettlementReferenceEnvironment(t)
	t.Setenv("SETTLEMENT_REFERENCE_ALLOW_ANY_CLIENT", "true")
	t.Setenv("SETTLEMENT_REFERENCE_CLIENT_ID", "")
	config, err := Load()
	if err != nil {
		t.Fatalf("allow-any settlement reference rejected: %v", err)
	}
	if !config.SettlementReferenceEnabled || !config.SettlementReferenceAllowAnyClient || config.SettlementReferenceClientID != "" {
		t.Fatalf("unexpected settlement reference config: enabled=%v allow_any=%v client_id=%q",
			config.SettlementReferenceEnabled, config.SettlementReferenceAllowAnyClient, config.SettlementReferenceClientID)
	}
	for name, clear := range map[string]func(){
		"SETTLEMENT_REFERENCE_ISSUER":                  func() { t.Setenv("SETTLEMENT_REFERENCE_ISSUER", "") },
		"SETTLEMENT_REFERENCE_AUDIENCE":                func() { t.Setenv("SETTLEMENT_REFERENCE_AUDIENCE", "") },
		"SETTLEMENT_REFERENCE_PUBLIC_KEY_PATH":         func() { t.Setenv("SETTLEMENT_REFERENCE_PUBLIC_KEY_PATH", "") },
		"SETTLEMENT_REFERENCE_CALLER_ENVIRONMENT_CODE": func() { t.Setenv("SETTLEMENT_REFERENCE_CALLER_ENVIRONMENT_CODE", "") },
	} {
		t.Run(name, func(t *testing.T) {
			setValidEnvironment(t)
			setSettlementReferenceEnvironment(t)
			t.Setenv("SETTLEMENT_REFERENCE_ALLOW_ANY_CLIENT", "true")
			clear()
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("%s missing not rejected: %v", name, err)
			}
		})
	}
}

// TestSettlementReferenceRequiresClientIDWhenSubjectPinned 验证未解耦时仍要求 client id。
func TestSettlementReferenceRequiresClientIDWhenSubjectPinned(t *testing.T) {
	setValidEnvironment(t)
	setSettlementReferenceEnvironment(t)
	t.Setenv("SETTLEMENT_REFERENCE_ALLOW_ANY_CLIENT", "false")
	t.Setenv("SETTLEMENT_REFERENCE_CLIENT_ID", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "SETTLEMENT_REFERENCE_CLIENT_ID") {
		t.Fatalf("empty client id accepted while subject is pinned: %v", err)
	}
	t.Setenv("SETTLEMENT_REFERENCE_CLIENT_ID", "settlement-prod-project-reference")
	config, err := Load()
	if err != nil {
		t.Fatalf("pinned settlement reference rejected: %v", err)
	}
	if config.SettlementReferenceAllowAnyClient || config.SettlementReferenceClientID != "settlement-prod-project-reference" {
		t.Fatalf("unexpected pinned config: allow_any=%v client_id=%q", config.SettlementReferenceAllowAnyClient, config.SettlementReferenceClientID)
	}
}

// TestLoadUsesUnifiedIdleAndAbsoluteSessionDefaults 验证统一会话默认值：
// 空闲 30 分钟、绝对 8 小时，而不是旧实现的单一绝对期限。
func TestLoadUsesUnifiedIdleAndAbsoluteSessionDefaults(t *testing.T) {
	setValidEnvironment(t)
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.OIDCSessionIdleTTL != 30*time.Minute || config.OIDCSessionAbsoluteTTL != 8*time.Hour {
		t.Fatalf("session TTLs = idle %s absolute %s, want 30m/8h", config.OIDCSessionIdleTTL, config.OIDCSessionAbsoluteTTL)
	}
}

// TestLoadKeepsLegacySessionTTLAsAbsoluteDeadline 验证旧键兼容：新绝对期限未显式
// 设置时旧 OIDC_SESSION_TTL 必须继续生效，不能被静默放大成默认 8 小时。
func TestLoadKeepsLegacySessionTTLAsAbsoluteDeadline(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("OIDC_SESSION_TTL", "2h")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.OIDCSessionAbsoluteTTL != 2*time.Hour || config.OIDCSessionIdleTTL != 30*time.Minute {
		t.Fatalf("legacy compatibility = idle %s absolute %s, want 30m/2h", config.OIDCSessionIdleTTL, config.OIDCSessionAbsoluteTTL)
	}
}

// TestLoadPrefersNewAbsoluteKeyOverLegacy 验证新旧键同时存在时以新键为准。
func TestLoadPrefersNewAbsoluteKeyOverLegacy(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("OIDC_SESSION_TTL", "2h")
	t.Setenv("OIDC_SESSION_ABSOLUTE_TTL", "6h")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.OIDCSessionAbsoluteTTL != 6*time.Hour {
		t.Fatalf("absolute TTL = %s, want 6h (new key wins)", config.OIDCSessionAbsoluteTTL)
	}
}

// TestLoadRejectsInvalidUnifiedSessionTTLs 验证无效配置失败关闭：空闲越界、
// 绝对期限不足或无法解析都不得以默认值继续启动。
func TestLoadRejectsInvalidUnifiedSessionTTLs(t *testing.T) {
	for _, test := range []struct {
		name      string
		env       map[string]string
		wantError string
	}{
		{"legacy absolute shorter than idle default", map[string]string{"OIDC_SESSION_TTL": "15m"}, "OIDC_SESSION_ABSOLUTE_TTL"},
		{"idle below lower bound", map[string]string{"OIDC_SESSION_IDLE_TTL": "30s"}, "OIDC_SESSION_IDLE_TTL"},
		{"idle above upper bound", map[string]string{"OIDC_SESSION_IDLE_TTL": "25h", "OIDC_SESSION_ABSOLUTE_TTL": "48h"}, "OIDC_SESSION_IDLE_TTL"},
		{"absolute not greater than idle", map[string]string{"OIDC_SESSION_IDLE_TTL": "8h", "OIDC_SESSION_ABSOLUTE_TTL": "8h"}, "OIDC_SESSION_ABSOLUTE_TTL"},
		{"absolute shorter than idle", map[string]string{"OIDC_SESSION_IDLE_TTL": "2h", "OIDC_SESSION_ABSOLUTE_TTL": "1h"}, "OIDC_SESSION_ABSOLUTE_TTL"},
		{"malformed legacy value", map[string]string{"OIDC_SESSION_TTL": "十五分钟"}, "OIDC_SESSION_TTL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %s", err, test.wantError)
			}
		})
	}
}
