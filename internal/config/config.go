package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddress                      string
	MySQLDSN                         string
	TemporalAddress                  string
	TemporalNamespace                string
	TemporalTaskQueue                string
	TemporalAPIKey                   string
	TemporalTLS                      bool
	TemporalWorkerBuildID            string
	TemporalWorkerDeploymentName     string
	TemporalWorkerVersioning         bool
	TemporalWorkerVersioningPolicy   string
	TemporalMetricsAddress           string
	RunWorkerWithAPI                 bool
	PlatformBaseURL                  string
	OIDCIssuer                       string
	OIDCBackchannelBaseURL           string
	OIDCClientID                     string
	OIDCClientSecret                 string
	OIDCIDPHint                      string
	OIDCRedirectURI                  string
	OIDCPostLogoutRedirectURI        string
	OIDCTenantID                     string
	OIDCSessionCookieName            string
	OIDCSessionTTL                   time.Duration
	OIDCAuthorizationRefresh         time.Duration
	OIDCAuthorizationMaxStale        time.Duration
	OIDCAuthorizationTimeout         time.Duration
	OIDCSessionEncryptionKey         []byte
	OIDCSessionSecure                bool
	AppPathPrefix                    string
	PlatformApplicationCode          string
	PlatformEnvironmentCode          string
	PlatformApplicationID            string
	PlatformAuditClientID            string
	PlatformAuditClientSecret        string
	PlatformCatalogSync              bool
	PlatformCatalogClientID          string
	PlatformCatalogClientSecret      string
	ContractIntegrationEnabled       bool
	ContractIntegrationRequireBearer bool
	ContractIntegrationClientID      string
	ContractIntegrationAudience      string
	DashboardMachineEnabled          bool
	DashboardMachineRequireBearer    bool
	DashboardMachineClientID         string
	DashboardMachineAudience         string
	DashboardMachineIssuer           string
	DashboardMachinePublicKeyPath    string
	DashboardMachineCallerApp        string
	DashboardMachineCallerEnv        string
	DashboardMachineScope            string
}

func Load() (Config, error) {
	c := Config{
		HTTPAddress: env("PM_HTTP_ADDR", ":8082"), MySQLDSN: os.Getenv("MYSQL_DSN"),
		TemporalAddress: env("TEMPORAL_ADDRESS", "localhost:7233"), TemporalNamespace: env("TEMPORAL_NAMESPACE", "default"), TemporalTaskQueue: env("TEMPORAL_TASK_QUEUE", "project-management"), TemporalAPIKey: os.Getenv("TEMPORAL_API_KEY"),
		TemporalWorkerBuildID: env("TEMPORAL_WORKER_BUILD_ID", "project-worker-v1"), TemporalWorkerDeploymentName: env("TEMPORAL_WORKER_DEPLOYMENT_NAME", "project-management"),
		TemporalWorkerVersioningPolicy: strings.ToUpper(env("TEMPORAL_WORKER_VERSIONING_POLICY", "PINNED")), TemporalMetricsAddress: env("TEMPORAL_METRICS_ADDRESS", ":9092"),
		PlatformBaseURL: strings.TrimSpace(os.Getenv("PLATFORM_BASE_URL")),
		OIDCIssuer:      strings.TrimSpace(os.Getenv("OIDC_ISSUER")), OIDCBackchannelBaseURL: os.Getenv("OIDC_BACKCHANNEL_BASE_URL"),
		OIDCClientID: os.Getenv("OIDC_CLIENT_ID"), OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"), OIDCIDPHint: strings.TrimSpace(os.Getenv("OIDC_IDP_HINT")), OIDCRedirectURI: os.Getenv("OIDC_REDIRECT_URI"),
		OIDCPostLogoutRedirectURI: os.Getenv("OIDC_POST_LOGOUT_REDIRECT_URI"), OIDCTenantID: os.Getenv("OIDC_TENANT_ID"),
		OIDCSessionCookieName: env("OIDC_SESSION_COOKIE_NAME", "project_management_session"), AppPathPrefix: env("APP_PATH_PREFIX", "/project_management"),
		PlatformApplicationCode: strings.TrimSpace(os.Getenv("PLATFORM_APPLICATION_CODE")), PlatformEnvironmentCode: strings.TrimSpace(os.Getenv("PLATFORM_ENVIRONMENT_CODE")),
		PlatformApplicationID: os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_APPLICATION_ID"), PlatformAuditClientID: os.Getenv("PLATFORM_AUDIT_CLIENT_ID"),
		PlatformAuditClientSecret: os.Getenv("PLATFORM_AUDIT_CLIENT_SECRET"), PlatformCatalogClientID: os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_ID"),
		PlatformCatalogClientSecret:   os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_SECRET"),
		ContractIntegrationClientID:   strings.TrimSpace(os.Getenv("CONTRACT_INTEGRATION_CLIENT_ID")),
		ContractIntegrationAudience:   strings.TrimSpace(os.Getenv("CONTRACT_INTEGRATION_AUDIENCE")),
		DashboardMachineClientID:      strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_CLIENT_ID")),
		DashboardMachineAudience:      strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_AUDIENCE")),
		DashboardMachineIssuer:        strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_ISSUER")),
		DashboardMachinePublicKeyPath: strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_PUBLIC_KEY_PATH")),
		DashboardMachineCallerApp:     strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_CALLER_APPLICATION_CODE")),
		DashboardMachineCallerEnv:     strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_CALLER_ENVIRONMENT_CODE")),
		DashboardMachineScope:         strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_REQUIRED_SCOPE")),
	}
	var err error
	if c.OIDCSessionTTL, err = duration("OIDC_SESSION_TTL", 8*time.Hour); err != nil {
		return c, err
	}
	if c.OIDCAuthorizationRefresh, err = duration("OIDC_AUTHORIZATION_REFRESH_INTERVAL", time.Minute); err != nil {
		return c, err
	}
	if c.OIDCAuthorizationMaxStale, err = duration("OIDC_AUTHORIZATION_MAX_STALE", 5*time.Minute); err != nil {
		return c, err
	}
	if c.OIDCAuthorizationTimeout, err = duration("OIDC_AUTHORIZATION_TIMEOUT", 10*time.Second); err != nil {
		return c, err
	}
	if c.OIDCSessionEncryptionKey, err = encryptionKey("OIDC_SESSION_ENCRYPTION_KEY_BASE64"); err != nil {
		return c, err
	}
	if c.OIDCSessionSecure, err = strconv.ParseBool(env("OIDC_SESSION_COOKIE_SECURE", "true")); err != nil {
		return c, fmt.Errorf("OIDC_SESSION_COOKIE_SECURE: %w", err)
	}
	if c.PlatformCatalogSync, err = strconv.ParseBool(env("PLATFORM_AUTHORIZATION_CATALOG_SYNC_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("PLATFORM_AUTHORIZATION_CATALOG_SYNC_ENABLED: %w", err)
	}
	if c.TemporalTLS, err = strconv.ParseBool(env("TEMPORAL_TLS", "false")); err != nil {
		return c, fmt.Errorf("TEMPORAL_TLS: %w", err)
	}
	if c.TemporalWorkerVersioning, err = strconv.ParseBool(env("TEMPORAL_WORKER_VERSIONING_ENABLED", "true")); err != nil {
		return c, fmt.Errorf("TEMPORAL_WORKER_VERSIONING_ENABLED: %w", err)
	}
	if c.RunWorkerWithAPI, err = strconv.ParseBool(env("PROJECT_RUN_WORKER_WITH_API", "true")); err != nil {
		return c, fmt.Errorf("PROJECT_RUN_WORKER_WITH_API: %w", err)
	}
	if c.ContractIntegrationEnabled, err = strconv.ParseBool(env("CONTRACT_INTEGRATION_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("CONTRACT_INTEGRATION_ENABLED: %w", err)
	}
	// 默认 false 保持"集成未启用"环境的兼容（H4 部署回归修复）：validate 会拒绝
	// ENABLED=true 且 REQUIRE_BEARER=false 的组合，因此启用集成必须显式开启来源校验。
	if c.ContractIntegrationRequireBearer, err = strconv.ParseBool(env("CONTRACT_INTEGRATION_REQUIRE_BEARER", "false")); err != nil {
		return c, fmt.Errorf("CONTRACT_INTEGRATION_REQUIRE_BEARER: %w", err)
	}
	if c.DashboardMachineEnabled, err = strconv.ParseBool(env("DASHBOARD_MACHINE_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("DASHBOARD_MACHINE_ENABLED: %w", err)
	}
	if c.DashboardMachineRequireBearer, err = strconv.ParseBool(env("DASHBOARD_MACHINE_REQUIRE_BEARER", "false")); err != nil {
		return c, fmt.Errorf("DASHBOARD_MACHINE_REQUIRE_BEARER: %w", err)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if strings.TrimSpace(c.HTTPAddress) == "" {
		return fmt.Errorf("PM_HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(c.MySQLDSN) == "" {
		return fmt.Errorf("MYSQL_DSN is required")
	}
	if strings.TrimSpace(c.TemporalAddress) == "" || strings.TrimSpace(c.TemporalNamespace) == "" || strings.TrimSpace(c.TemporalTaskQueue) == "" || strings.TrimSpace(c.TemporalWorkerBuildID) == "" || strings.TrimSpace(c.TemporalMetricsAddress) == "" {
		return fmt.Errorf("Temporal address, namespace, task queue, worker build ID and metrics address are required")
	}
	if c.TemporalWorkerVersioning && strings.TrimSpace(c.TemporalWorkerDeploymentName) == "" {
		return fmt.Errorf("TEMPORAL_WORKER_DEPLOYMENT_NAME is required when worker versioning is enabled")
	}
	if c.TemporalWorkerVersioningPolicy != "PINNED" && c.TemporalWorkerVersioningPolicy != "AUTO_UPGRADE" {
		return fmt.Errorf("TEMPORAL_WORKER_VERSIONING_POLICY must be PINNED or AUTO_UPGRADE")
	}
	for name, value := range map[string]string{"OIDC_ISSUER": c.OIDCIssuer, "OIDC_CLIENT_ID": c.OIDCClientID, "OIDC_CLIENT_SECRET": c.OIDCClientSecret, "OIDC_REDIRECT_URI": c.OIDCRedirectURI, "OIDC_TENANT_ID": c.OIDCTenantID, "PLATFORM_BASE_URL": c.PlatformBaseURL, "PLATFORM_APPLICATION_CODE": c.PlatformApplicationCode, "PLATFORM_ENVIRONMENT_CODE": c.PlatformEnvironmentCode} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		if placeholder(value) {
			return fmt.Errorf("%s contains a deployment placeholder", name)
		}
	}
	if !validHTTPOrigin(c.PlatformBaseURL) {
		return fmt.Errorf("PLATFORM_BASE_URL must be an HTTP(S) origin")
	}
	if !validHTTPURL(c.OIDCIssuer) {
		return fmt.Errorf("OIDC_ISSUER must be a valid HTTP(S) URL")
	}
	if c.OIDCBackchannelBaseURL != "" && !validHTTPOrigin(c.OIDCBackchannelBaseURL) {
		return fmt.Errorf("OIDC_BACKCHANNEL_BASE_URL must be an HTTP(S) origin")
	}
	if !validRedirectURL(c.OIDCRedirectURI) {
		return fmt.Errorf("OIDC_REDIRECT_URI must be a valid HTTP(S) redirect URL")
	}
	if c.OIDCPostLogoutRedirectURI != "" && !validRedirectURL(c.OIDCPostLogoutRedirectURI) {
		return fmt.Errorf("OIDC_POST_LOGOUT_REDIRECT_URI must be a valid HTTP(S) redirect URL")
	}
	if c.OIDCSessionTTL <= 0 || c.OIDCAuthorizationRefresh <= 0 || c.OIDCAuthorizationRefresh >= c.OIDCSessionTTL || c.OIDCAuthorizationMaxStale < c.OIDCAuthorizationRefresh || c.OIDCAuthorizationMaxStale >= c.OIDCSessionTTL {
		return fmt.Errorf("OIDC refresh and maximum stale intervals must be positive, ordered and shorter than session TTL")
	}
	if c.OIDCAuthorizationTimeout <= 0 || c.OIDCAuthorizationTimeout > 30*time.Second {
		return fmt.Errorf("OIDC_AUTHORIZATION_TIMEOUT must be between 1ns and 30s")
	}
	if len(c.OIDCSessionEncryptionKey) != 32 {
		return fmt.Errorf("OIDC_SESSION_ENCRYPTION_KEY_BASE64 must decode to 32 bytes")
	}
	if c.PlatformApplicationCode != "project_management" {
		return fmt.Errorf("PLATFORM_APPLICATION_CODE must be project_management")
	}
	if c.AppPathPrefix == "/" || !strings.HasPrefix(c.AppPathPrefix, "/") || strings.HasSuffix(c.AppPathPrefix, "/") {
		return fmt.Errorf("APP_PATH_PREFIX must be a non-root absolute path without trailing slash")
	}
	if !validCookieName(c.OIDCSessionCookieName) {
		return fmt.Errorf("OIDC_SESSION_COOKIE_NAME must be a valid cookie name")
	}
	if (c.PlatformAuditClientID == "") != (c.PlatformAuditClientSecret == "") {
		return fmt.Errorf("platform audit client ID and secret must be configured together")
	}
	if c.PlatformCatalogSync && (c.PlatformApplicationID == "" || c.PlatformCatalogClientID == "" || c.PlatformCatalogClientSecret == "") {
		return fmt.Errorf("catalog sync requires application ID, client ID and secret")
	}
	if c.ContractIntegrationRequireBearer {
		if !c.ContractIntegrationEnabled {
			return fmt.Errorf("CONTRACT_INTEGRATION_REQUIRE_BEARER requires CONTRACT_INTEGRATION_ENABLED")
		}
		for name, value := range map[string]string{"CONTRACT_INTEGRATION_CLIENT_ID": c.ContractIntegrationClientID, "CONTRACT_INTEGRATION_AUDIENCE": c.ContractIntegrationAudience} {
			if strings.TrimSpace(value) == "" || placeholder(value) {
				return fmt.Errorf("%s is required when bearer authentication is enabled", name)
			}
		}
	}
	// H4 修复：内部投递来源校验不可关闭。
	if c.ContractIntegrationEnabled && !c.ContractIntegrationRequireBearer {
		return fmt.Errorf("CONTRACT_INTEGRATION_REQUIRE_BEARER must be true when CONTRACT_INTEGRATION_ENABLED=true (internal delivery source verification is mandatory)")
	}
	if c.DashboardMachineRequireBearer {
		if !c.DashboardMachineEnabled {
			return fmt.Errorf("DASHBOARD_MACHINE_REQUIRE_BEARER requires DASHBOARD_MACHINE_ENABLED")
		}
		for _, item := range []struct{ name, value string }{
			{"DASHBOARD_MACHINE_CLIENT_ID", c.DashboardMachineClientID},
			{"DASHBOARD_MACHINE_AUDIENCE", c.DashboardMachineAudience},
			{"DASHBOARD_MACHINE_ISSUER", c.DashboardMachineIssuer},
			{"DASHBOARD_MACHINE_PUBLIC_KEY_PATH", c.DashboardMachinePublicKeyPath},
			{"DASHBOARD_MACHINE_CALLER_APPLICATION_CODE", c.DashboardMachineCallerApp},
			{"DASHBOARD_MACHINE_CALLER_ENVIRONMENT_CODE", c.DashboardMachineCallerEnv},
			{"DASHBOARD_MACHINE_REQUIRED_SCOPE", c.DashboardMachineScope},
		} {
			if strings.TrimSpace(item.value) == "" || placeholder(item.value) {
				return fmt.Errorf("%s is required when dashboard bearer authentication is enabled", item.name)
			}
		}
	}
	// 项目汇总包含全租户数据，机器接口启用后必须校验调用方身份。
	if c.DashboardMachineEnabled && !c.DashboardMachineRequireBearer {
		return fmt.Errorf("DASHBOARD_MACHINE_REQUIRE_BEARER must be true when DASHBOARD_MACHINE_ENABLED=true")
	}
	return nil
}

func validHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func validHTTPOrigin(value string) bool {
	if !validHTTPURL(value) {
		return false
	}
	parsed, _ := url.ParseRequestURI(value)
	return parsed.RawQuery == "" && (parsed.Path == "" || parsed.Path == "/")
}

func validRedirectURL(value string) bool {
	return validHTTPURL(value)
}

func validCookieName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", character) {
			return false
		}
	}
	return true
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func duration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func encryptionKey(key string) ([]byte, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil, fmt.Errorf("%s is required", key)
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(value)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("%s must be base64 encoding of exactly 32 bytes", key)
}

func placeholder(value string) bool {
	upper := strings.ToUpper(strings.TrimSpace(value))
	return strings.Contains(upper, "PENDING") || strings.Contains(upper, "CHANGEME") || strings.Contains(upper, "EXAMPLE.COM")
}
