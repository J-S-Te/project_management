package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/auditoutbox"
	"github.com/j-s-te/project-management/internal/bootstrap"
	"github.com/j-s-te/project-management/internal/config"
	"github.com/j-s-te/project-management/internal/handover"
	"github.com/j-s-te/project-management/internal/httpapi"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/internal/temporalworker"
	"github.com/j-s-te/project-management/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	licenseCtx, licenseStop := context.WithCancel(ctx)
	defer licenseStop()
	commercialGate, err := bootstrap.OpenCommercialLicense(licenseCtx, logger)
	if err != nil {
		logger.Error("commercial license configuration failed")
		os.Exit(1)
	}
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
	// 批次 2：事务内审计 outbox 的投递循环；平台不可用时事件留在本地重试。
	var auditStore *auditoutbox.GORMStore
	if audit != nil {
		auditStore = &auditoutbox.GORMStore{DB: db, Lease: 2 * time.Minute, MaxTries: 20}
		auditDispatcher := &auditoutbox.Dispatcher{
			Store:    auditStore,
			Reporter: audit, WorkerID: "project-api-audit", Poll: 2 * time.Second, BatchSize: 50,
			MaxAttempts: 20, MaxBackoff: 5 * time.Minute, Logger: logger,
		}
		go auditDispatcher.Run(ctx)
	}
	identity, err := platform.NewOIDCAuthenticator(startupCtx, platform.OIDCOptions{Issuer: cfg.OIDCIssuer, BackchannelBaseURL: cfg.OIDCBackchannelBaseURL, PlatformBaseURL: cfg.PlatformBaseURL, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, IdentityProviderHint: cfg.OIDCIDPHint, RedirectURI: cfg.OIDCRedirectURI, PostLogoutRedirectURI: cfg.OIDCPostLogoutRedirectURI, TenantID: cfg.OIDCTenantID, ApplicationCode: cfg.PlatformApplicationCode, EnvironmentCode: cfg.PlatformEnvironmentCode, SessionCookieName: cfg.OIDCSessionCookieName, SessionTTL: cfg.OIDCSessionTTL, AuthorizationRefreshInterval: cfg.OIDCAuthorizationRefresh, AuthorizationMaxStale: cfg.OIDCAuthorizationMaxStale, AuthorizationTimeout: cfg.OIDCAuthorizationTimeout, SessionSecure: cfg.OIDCSessionSecure, PathPrefix: cfg.AppPathPrefix, Audit: audit}, platform.NewGORMOIDCStore(db), cfg.OIDCSessionEncryptionKey)
	if err != nil {
		logger.Error("initialize platform OIDC", "error", err)
		os.Exit(1)
	}
	var contractBearer platform.ClientCredentialsTokenVerifier
	if cfg.ContractIntegrationEnabled {
		contractBearer, err = platform.NewClientCredentialsTokenVerifier(startupCtx, platform.ClientCredentialsVerifierOptions{
			Issuer: cfg.ContractIntegrationIssuer, Audience: cfg.ContractIntegrationAudience, PublicKeyPath: cfg.ContractIntegrationPublicKeyPath,
			ClientID: cfg.ContractIntegrationClientID, TenantID: cfg.OIDCTenantID,
			CallerApplicationCode: cfg.ContractIntegrationCallerApp, CallerEnvironmentCode: cfg.ContractIntegrationCallerEnv,
			RequiredScope: cfg.ContractIntegrationScope,
		})
		if err != nil {
			logger.Error("initialize contract integration bearer verifier", "error", err)
			os.Exit(1)
		}
	}
	var handoverBearer platform.ClientCredentialsTokenVerifier
	if cfg.HandoverIntegrationEnabled {
		handoverBearer, err = platform.NewClientCredentialsTokenVerifier(startupCtx, platform.ClientCredentialsVerifierOptions{
			Issuer: cfg.HandoverIntegrationIssuer, Audience: cfg.HandoverIntegrationAudience, PublicKeyPath: cfg.HandoverIntegrationPublicKeyPath,
			ClientID: cfg.HandoverIntegrationClientID, TenantID: cfg.OIDCTenantID,
			CallerApplicationCode: cfg.HandoverIntegrationCallerApp, CallerEnvironmentCode: cfg.HandoverIntegrationCallerEnv,
			RequiredScope: cfg.HandoverIntegrationScope,
		})
		if err != nil {
			logger.Error("initialize handover machine bearer verifier", "error", err)
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
	notifications := platform.NewNotificationPublisher(cfg.PlatformBaseURL, cfg.PlatformNotificationURL, cfg.PlatformNotificationClientID, cfg.PlatformNotificationSecret, cfg.PlatformNotificationScope)
	if notifications != nil {
		logger.Info("platform notification integration enabled")
	}
	// 启动即核对库结构：代码先上线、迁移没跑是真实发生过的故障模式，届时任何写路径都会因
	// 缺列/缺表报 500，对外只剩"服务暂不可用"。这里显式检测并把待执行迁移挂到就绪检查上。
	pendingMigrations, pendingErr := migration.Pending(context.Background(), cfg.MySQLDSN, migrations.Files)
	if pendingErr != nil {
		logger.Error("check pending migrations failed", "error", pendingErr)
	} else if len(pendingMigrations) > 0 {
		logger.Error("database schema is behind the application code; run project-migrate before serving traffic",
			"pending_migrations", pendingMigrations, "count", len(pendingMigrations))
	}
	var contracts platform.ApprovedContractVerifier
	if cfg.ContractApprovalValidationEnabled {
		contracts = platform.NewApprovedContractVerifier(cfg.PlatformBaseURL, cfg.ContractApprovalValidationURL, cfg.ContractApprovalValidationClientID, cfg.ContractApprovalValidationClientSecret, cfg.ContractApprovalValidationScope)
		if contracts == nil {
			logger.Error("contract approval validation is enabled but client configuration is incomplete")
			os.Exit(1)
		}
	}
	var evidenceFiles platform.EvidenceFileGateway
	if cfg.FileGatewayEnabled {
		evidenceFiles = platform.NewEvidenceFileGateway(cfg.PlatformBaseURL, cfg.FileGatewayBaseURL, cfg.FileGatewayApplicationID, cfg.FileGatewayClientID, cfg.FileGatewayClientSecret)
		if evidenceFiles == nil {
			logger.Error("file gateway is enabled but client configuration is incomplete")
			os.Exit(1)
		}
	}
	service := &application.Service{Repo: repository, Personnel: personnel, Notifications: notifications, Contracts: contracts, EvidenceFiles: evidenceFiles, AuditStore: auditStore, Logger: logger}
	handoverRepository, handoverErr := handover.NewGORMRepository(db)
	if handoverErr != nil {
		logger.Error("initialize handover repository", "error", handoverErr)
		os.Exit(1)
	}
	handoverService, handoverErr := handover.NewService(handoverRepository, handover.NewID, time.Now)
	if handoverErr != nil {
		logger.Error("initialize handover service", "error", handoverErr)
		os.Exit(1)
	}
	// 写路径冻结覆盖：交接中的用户不得再被指派为项目经理。
	service.UseFreezeChecker(handoverRepository)
	// 统一审计来源：显式可信代理列表，未列入的直接对端不能改写来源地址。
	httpapi.ConfigureTrustedProxies(cfg.TrustedProxies)
	router := httpapi.NewRouter(service, identity, audit, logger, httpapi.RouterOptions{
		CommercialLicense: commercialGate,
		PendingMigrations: pendingMigrations,
		ProjectSnapshotLimits: application.ProjectSnapshotLimits{
			BatchSize: 200, MaxProjects: cfg.Snapshot.MaxProjects,
			MaxServiceItems: cfg.Snapshot.MaxServiceItems,
			MaxBytes:        cfg.Snapshot.MaxBytes, MaxLineBytes: cfg.Snapshot.MaxLineBytes,
			Timeout: cfg.Snapshot.Timeout,
		},
		ContractIntegration: &httpapi.ContractIntegrationOptions{
			Enabled:        cfg.ContractIntegrationEnabled,
			BearerVerifier: contractBearer,
		},
		DashboardIntegration: &httpapi.DashboardIntegrationOptions{
			Enabled:        cfg.DashboardMachineEnabled,
			BearerVerifier: dashboardBearer,
		},
		HandoverService: handoverService,
		HandoverIntegration: &httpapi.HandoverIntegrationOptions{
			Enabled:        cfg.HandoverIntegrationEnabled,
			BearerVerifier: handoverBearer,
		},
	})
	server := &http.Server{Addr: cfg.HTTPAddress, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	if cfg.RunWorkerWithAPI {
		logger.Warn("PROJECT_RUN_WORKER_WITH_API is deprecated; start the separate project-sla-notifier component")
	}
	logger.Info("project management API started", "address", cfg.HTTPAddress, "background_component", "project-sla-notifier")
	// AUD-2026-029：监听 SIGTERM/SIGINT 后先排空在途请求再退出（对齐 worker 的
	// InterruptCh 风格）。强制审计模式下写请求的响应先缓冲、Report 成功才提交，
	// 若 SIGTERM 直接断连，缓冲中的响应与在途审计上报都会丢失。
	shutdownCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stopSignals()
	if err := serveWithGracefulShutdown(server, shutdownCtx.Done(), logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// serverShutdownTimeout 是收到信号后排空在途请求的上限：超过即强制退出，
// 避免卡死的下游连接把进程挂住（容器运行时最终会升级为 SIGKILL）。
const serverShutdownTimeout = 15 * time.Second

// serveWithGracefulShutdown 运行 HTTP 服务直到监听退出或收到停机信号；
// 信号到达后调用 server.Shutdown 排空在途请求，返回 Shutdown 或监听的错误。
func serveWithGracefulShutdown(server *http.Server, shutdown <-chan struct{}, logger *slog.Logger) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	select {
	case err := <-serveErr:
		return err
	case <-shutdown:
		ctx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		logger.Info("shutdown signal received, draining in-flight requests", "timeout", serverShutdownTimeout.String())
		return server.Shutdown(ctx)
	}
}
