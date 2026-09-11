package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/bootstrap"
	"github.com/j-s-te/project-management/internal/config"
	"github.com/j-s-te/project-management/internal/httpapi"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/internal/temporalworker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	metrics := temporalworker.NewMetricsRegistry()
	if err := temporalworker.StartMetricsServer(ctx, cfg.TemporalMetricsAddress, metrics, logger); err != nil {
		logger.Error("start Temporal metrics server", "error", err)
		os.Exit(1)
	}
	db, err := bootstrap.OpenDatabase(ctx, cfg.MySQLDSN)
	if err != nil {
		logger.Error("database failed", "error", err)
		os.Exit(1)
	}
	defer bootstrap.CloseDatabase(db)
	repository := store.NewRepository(db)
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	audit := platform.NewAuditReporter(cfg.PlatformBaseURL, cfg.PlatformAuditClientID, cfg.PlatformAuditClientSecret, cfg.PlatformApplicationCode, cfg.PlatformEnvironmentCode)
	identity, err := platform.NewOIDCAuthenticator(startupCtx, platform.OIDCOptions{Issuer: cfg.OIDCIssuer, BackchannelBaseURL: cfg.OIDCBackchannelBaseURL, PlatformBaseURL: cfg.PlatformBaseURL, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, IdentityProviderHint: cfg.OIDCIDPHint, RedirectURI: cfg.OIDCRedirectURI, PostLogoutRedirectURI: cfg.OIDCPostLogoutRedirectURI, TenantID: cfg.OIDCTenantID, ApplicationCode: cfg.PlatformApplicationCode, EnvironmentCode: cfg.PlatformEnvironmentCode, SessionCookieName: cfg.OIDCSessionCookieName, SessionTTL: cfg.OIDCSessionTTL, AuthorizationRefreshInterval: cfg.OIDCAuthorizationRefresh, AuthorizationMaxStale: cfg.OIDCAuthorizationMaxStale, AuthorizationTimeout: cfg.OIDCAuthorizationTimeout, SessionSecure: cfg.OIDCSessionSecure, PathPrefix: cfg.AppPathPrefix, Audit: audit}, platform.NewGORMOIDCStore(db), cfg.OIDCSessionEncryptionKey)
	if err != nil {
		logger.Error("initialize platform OIDC", "error", err)
		os.Exit(1)
	}
	var contractBearer platform.ClientCredentialsTokenVerifier
	if cfg.ContractIntegrationEnabled {
		contractBearer, err = platform.NewKeycloakClientCredentialsTokenVerifier(startupCtx, platform.KeycloakClientCredentialsVerifierOptions{
			Issuer: cfg.OIDCIssuer, BackchannelBaseURL: cfg.OIDCBackchannelBaseURL,
			ClientID: cfg.ContractIntegrationClientID, Audience: cfg.ContractIntegrationAudience,
			TenantID: cfg.OIDCTenantID, CallerApplicationCode: "contract_management", CallerEnvironmentCode: cfg.PlatformEnvironmentCode,
			Timeout: cfg.OIDCAuthorizationTimeout,
		})
		if err != nil {
			logger.Error("initialize contract integration bearer verifier", "error", err)
			os.Exit(1)
		}
	}
	var dashboardBearer platform.ClientCredentialsTokenVerifier
	if cfg.DashboardMachineEnabled {
		dashboardBearer, err = platform.NewClientCredentialsTokenVerifier(startupCtx, platform.ClientCredentialsVerifierOptions{
			Issuer:                cfg.DashboardMachineIssuer,
			PublicKeyPath:         cfg.DashboardMachinePublicKeyPath,
			ClientID:              cfg.DashboardMachineClientID,
			Audience:              cfg.DashboardMachineAudience,
			TenantID:              cfg.OIDCTenantID,
			CallerApplicationCode: cfg.DashboardMachineCallerApp,
			CallerEnvironmentCode: cfg.DashboardMachineCallerEnv,
			RequiredScope:         cfg.DashboardMachineScope,
		})
		if err != nil {
			logger.Error("initialize dashboard machine bearer verifier", "error", err)
			os.Exit(1)
		}
	}
	if err := platform.SyncAuthorizationCatalog(startupCtx, platform.CatalogSyncOptions{Enabled: cfg.PlatformCatalogSync, BaseURL: cfg.PlatformBaseURL, ApplicationID: cfg.PlatformApplicationID, ClientID: cfg.PlatformCatalogClientID, ClientSecret: cfg.PlatformCatalogClientSecret}); err != nil {
		logger.Error("sync platform authorization catalog", "error", err)
		os.Exit(1)
	}
	auditConfig := platform.CheckAuditReporterConfiguration(cfg.PlatformBaseURL, cfg.PlatformAuditClientID, cfg.PlatformAuditClientSecret, cfg.PlatformApplicationCode, cfg.PlatformEnvironmentCode)
	if auditConfig.Enabled {
		logger.Info("platform audit reporting enabled")
	} else {
		logger.Warn("platform audit reporting disabled; write operations will not be sent to the platform audit service", "missing_configuration", auditConfig.MissingFields)
	}
	var personnel platform.OwnerDirectory
	if cfg.OwnerDirectoryEnabled {
		personnel = platform.NewOwnerDirectory(cfg.PlatformBaseURL, cfg.PlatformOwnerDirectoryURL, cfg.PlatformOwnerDirectoryClientID, cfg.PlatformOwnerDirectorySecret, cfg.PlatformOwnerDirectoryScope)
		if personnel == nil {
			logger.Error("platform owner directory is enabled but the client could not be configured")
			os.Exit(1)
		}
		logger.Info("platform owner directory integration enabled")
	} else {
		logger.Warn("platform owner directory integration disabled; personnel pickers will be unavailable")
	}
	service := &application.Service{Repo: repository, Personnel: personnel}
	router := httpapi.NewRouter(service, identity, audit, logger, httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{
			Enabled:        cfg.ContractIntegrationEnabled,
			BearerVerifier: contractBearer,
		},
		DashboardIntegration: &httpapi.DashboardIntegrationOptions{
			Enabled:        cfg.DashboardMachineEnabled,
			BearerVerifier: dashboardBearer,
		},
	})
	server := &http.Server{Addr: cfg.HTTPAddress, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	logger.Info("project management API started", "address", cfg.HTTPAddress, "task_queue", cfg.TemporalTaskQueue, "embedded_worker", cfg.RunWorkerWithAPI, "worker_deployment", cfg.TemporalWorkerDeploymentName, "worker_build_id", cfg.TemporalWorkerBuildID, "worker_versioning", cfg.TemporalWorkerVersioning)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
