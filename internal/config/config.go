package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddress                 string
	MySQLDSN                    string
	TemporalAddress             string
	TemporalNamespace           string
	TemporalTaskQueue           string
	TemporalAPIKey              string
	TemporalTLS                 bool
	RunWorkerWithAPI            bool
	PlatformBaseURL             string
	OIDCIssuer                  string
	OIDCBackchannelBaseURL      string
	OIDCClientID                string
	OIDCClientSecret            string
	OIDCRedirectURI             string
	OIDCPostLogoutRedirectURI   string
	OIDCTenantID                string
	OIDCSessionCookieName       string
	OIDCSessionTTL              time.Duration
	OIDCAuthorizationRefresh    time.Duration
	OIDCSessionSecure           bool
	AppPathPrefix               string
	PlatformApplicationCode     string
	PlatformEnvironmentCode     string
	PlatformApplicationID       string
	PlatformAuditClientID       string
	PlatformAuditClientSecret   string
	PlatformCatalogSync         bool
	PlatformCatalogClientID     string
	PlatformCatalogClientSecret string
}

func Load() (Config, error) {
	c := Config{
		HTTPAddress: env("PM_HTTP_ADDR", ":8082"), MySQLDSN: os.Getenv("MYSQL_DSN"),
		TemporalAddress: env("TEMPORAL_ADDRESS", "localhost:7233"), TemporalNamespace: env("TEMPORAL_NAMESPACE", "default"), TemporalTaskQueue: env("TEMPORAL_TASK_QUEUE", "project-management"), TemporalAPIKey: os.Getenv("TEMPORAL_API_KEY"),
		PlatformBaseURL: env("PLATFORM_BASE_URL", "http://localhost:8080"),
		OIDCIssuer:      env("OIDC_ISSUER", "http://localhost:8080"), OIDCBackchannelBaseURL: os.Getenv("OIDC_BACKCHANNEL_BASE_URL"),
		OIDCClientID: os.Getenv("OIDC_CLIENT_ID"), OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"), OIDCRedirectURI: os.Getenv("OIDC_REDIRECT_URI"),
		OIDCPostLogoutRedirectURI: os.Getenv("OIDC_POST_LOGOUT_REDIRECT_URI"), OIDCTenantID: os.Getenv("OIDC_TENANT_ID"),
		OIDCSessionCookieName: env("OIDC_SESSION_COOKIE_NAME", "project_management_session"), AppPathPrefix: env("APP_PATH_PREFIX", "/project_management"),
		PlatformApplicationCode: env("PLATFORM_APPLICATION_CODE", "project_management"), PlatformEnvironmentCode: env("PLATFORM_ENVIRONMENT_CODE", "prod"),
		PlatformApplicationID: os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_APPLICATION_ID"), PlatformAuditClientID: os.Getenv("PLATFORM_AUDIT_CLIENT_ID"),
		PlatformAuditClientSecret: os.Getenv("PLATFORM_AUDIT_CLIENT_SECRET"), PlatformCatalogClientID: os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_ID"),
		PlatformCatalogClientSecret: os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_SECRET"),
	}
	var err error
	if c.OIDCSessionTTL, err = duration("OIDC_SESSION_TTL", 8*time.Hour); err != nil {
		return c, err
	}
	if c.OIDCAuthorizationRefresh, err = duration("OIDC_AUTHORIZATION_REFRESH_INTERVAL", time.Minute); err != nil {
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
	if c.RunWorkerWithAPI, err = strconv.ParseBool(env("PROJECT_RUN_WORKER_WITH_API", "true")); err != nil {
		return c, fmt.Errorf("PROJECT_RUN_WORKER_WITH_API: %w", err)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if strings.TrimSpace(c.MySQLDSN) == "" {
		return fmt.Errorf("MYSQL_DSN is required")
	}
	if strings.TrimSpace(c.TemporalAddress) == "" || strings.TrimSpace(c.TemporalNamespace) == "" || strings.TrimSpace(c.TemporalTaskQueue) == "" {
		return fmt.Errorf("Temporal address, namespace and task queue are required")
	}
	for name, value := range map[string]string{"OIDC_CLIENT_ID": c.OIDCClientID, "OIDC_CLIENT_SECRET": c.OIDCClientSecret, "OIDC_REDIRECT_URI": c.OIDCRedirectURI, "OIDC_TENANT_ID": c.OIDCTenantID} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]string{"PLATFORM_BASE_URL": c.PlatformBaseURL, "OIDC_ISSUER": c.OIDCIssuer, "OIDC_REDIRECT_URI": c.OIDCRedirectURI} {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("%s must be a valid HTTP(S) URL", name)
		}
	}
	if c.OIDCSessionTTL <= 0 || c.OIDCAuthorizationRefresh <= 0 || c.OIDCAuthorizationRefresh >= c.OIDCSessionTTL {
		return fmt.Errorf("OIDC refresh interval must be positive and shorter than session TTL")
	}
	if c.AppPathPrefix == "/" || !strings.HasPrefix(c.AppPathPrefix, "/") || strings.HasSuffix(c.AppPathPrefix, "/") {
		return fmt.Errorf("APP_PATH_PREFIX must be a non-root absolute path without trailing slash")
	}
	if (c.PlatformAuditClientID == "") != (c.PlatformAuditClientSecret == "") {
		return fmt.Errorf("platform audit client ID and secret must be configured together")
	}
	if c.PlatformCatalogSync && (c.PlatformApplicationID == "" || c.PlatformCatalogClientID == "" || c.PlatformCatalogClientSecret == "") {
		return fmt.Errorf("catalog sync requires application ID, client ID and secret")
	}
	return nil
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
