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
	"github.com/j-s-te/project-management/internal/shutdown"
	"github.com/j-s-te/project-management/internal/temporalworker"
	"github.com/j-s-te/project-management/migrations"
)

// main 装配项目 API、会话认证及后台依赖并启动服务。
// 参数：无。
// 返回值：无；初始化失败终止启动。
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	// B2：后台 goroutine 用独立生命周期上下文，收到停机信号后取消并按预算 join。
	lifeCtx, lifeCancel := context.WithCancel(ctx)
	defer lifeCancel()
	group := shutdown.NewGroup()
	licenseCtx, licenseStop := context.WithCancel(ctx)
	defer licenseStop()
	commercialGate, err := bootstrap.OpenCommercialLicense(licenseCtx, logger)
	if err != nil {
		logger.Error("commercial license configuration failed")
		os.Exit(1)
	}
	metrics := temporalworker.NewMetricsRegistry()
	metricsServer, err := temporalworker.StartMetricsServer(lifeCtx, cfg.TemporalMetricsAddress, metrics, logger)
	if err != nil {
		logger.Error("start Temporal metrics server", "error", err)
		os.Exit(1)
	}
	group.Go("temporal-metrics-server", func() { <-metricsServer.Done() })
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
		// 已投递行清理默认关闭：被删除的是"只存在于本地"的已投递审计证据，
		// 必须由运维显式配置 AUDIT_OUTBOX_DELIVERED_RETENTION_DAYS 正数天才启用。
		// 关闭时明确落一条日志，避免运维误以为历史行已在按 30 天滚动清理。
		if retention := auditoutbox.DeliveredRetentionFromEnv(); retention > 0 {
			logger.Info("audit outbox delivered-row cleanup enabled",
				"table", "prj_audit_outbox", "cleanup", "enabled",
				"retention_days", int(retention/(24*time.Hour)), "env", "AUDIT_OUTBOX_DELIVERED_RETENTION_DAYS")
		} else {
			logger.Info("audit outbox delivered-row cleanup disabled (retention unset or <=0): delivered rows are kept indefinitely as local audit evidence",
				"table", "prj_audit_outbox", "cleanup", "disabled",
				"enable_with", "AUDIT_OUTBOX_DELIVERED_RETENTION_DAYS=<positive days>")
		}
		// B2：dispatcher 挂在生命周期上下文上，退出时被有界 join，而不是随进程消失。
		group.Go("audit-outbox-dispatcher", func() { auditDispatcher.Run(lifeCtx) })
		// C2：同进程私有指标端口（:9092）暴露审计 outbox 积压，读的仍是现有 prj_audit_outbox。
		backlogCollector := &temporalworker.OutboxBacklogCollector{
			Source: auditOutboxBacklog{store: auditStore}, Registry: metrics, Logger: logger,
		}
		group.Go("audit-outbox-backlog-collector", func() { backlogCollector.Run(lifeCtx) })
	} else {
		// 审计发布器配置不完整时不会启动投递循环：事务内审计仍写入
		// prj_audit_outbox，但没有任何循环把它送到平台，表会只增不减、
		// 平台的项目审计也会静默缺失。保持既有"启动不 fail-fast"语义
		// （是否必开由 PROJECT_AUDIT_REQUIRED/PLATFORM_AUDIT_REQUIRED 与
		// /readyz 决定），但必须留下可检索的 WARN。
		logger.Warn("audit outbox dispatcher not started: PLATFORM_AUDIT_CLIENT_ID/PLATFORM_AUDIT_CLIENT_SECRET/PLATFORM_APPLICATION_CODE/PLATFORM_ENVIRONMENT_CODE incomplete; rows written to prj_audit_outbox will never be delivered to the platform until the audit publisher is configured",
			"table", "prj_audit_outbox", "dispatcher", "disabled",
			"impact", "transactional audit events accumulate locally and are not visible on the platform",
			"readyz_field", "audit_outbox_dispatcher")
	}
	identity, err := platform.NewOIDCAuthenticator(startupCtx, platform.OIDCOptions{CentralSessionEnabled: cfg.CentralSessionEnabled, CentralSessionPublicBaseURL: cfg.CentralSessionPublicBaseURL, Issuer: cfg.OIDCIssuer, BackchannelBaseURL: cfg.OIDCBackchannelBaseURL, PlatformBaseURL: cfg.PlatformBaseURL, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, IdentityProviderHint: cfg.OIDCIDPHint, RedirectURI: cfg.OIDCRedirectURI, PostLogoutRedirectURI: cfg.OIDCPostLogoutRedirectURI, TenantID: cfg.OIDCTenantID, ApplicationCode: cfg.PlatformApplicationCode, EnvironmentCode: cfg.PlatformEnvironmentCode, SessionCookieName: cfg.OIDCSessionCookieName, SessionIdleTTL: cfg.OIDCSessionIdleTTL, SessionAbsoluteTTL: cfg.OIDCSessionAbsoluteTTL, AuthorizationRefreshInterval: cfg.OIDCAuthorizationRefresh, AuthorizationMaxStale: cfg.OIDCAuthorizationMaxStale, AuthorizationTimeout: cfg.OIDCAuthorizationTimeout, SessionSecure: cfg.OIDCSessionSecure, PathPrefix: cfg.AppPathPrefix, Audit: audit}, platform.NewGORMOIDCStore(db), cfg.OIDCSessionEncryptionKey)
	if err != nil {
		logger.Error("initialize platform OIDC", "error", err)
		os.Exit(1)
	}
	var settlementReferenceBearer platform.ClientCredentialsTokenVerifier
	if cfg.SettlementReferenceEnabled {
		settlementReferenceBearer, err = platform.NewClientCredentialsTokenVerifier(startupCtx, platform.ClientCredentialsVerifierOptions{
			Issuer: cfg.SettlementReferenceIssuer, Audience: cfg.SettlementReferenceAudience, PublicKeyPath: cfg.SettlementReferencePublicKeyPath,
			ClientID: cfg.SettlementReferenceClientID, TenantID: cfg.OIDCTenantID, CallerApplicationCode: "settlement", CallerEnvironmentCode: cfg.SettlementReferenceCallerEnv, RequiredScope: "settlement.project_reference.read",
			AllowAnyClientSubject: cfg.SettlementReferenceAllowAnyClient,
		})
		if err != nil {
			logger.Error("initialize settlement reference verifier", "error", err)
			os.Exit(1)
		}
	}
	var contractImportBearer platform.ClientCredentialsTokenVerifier
	if cfg.ProjectContractImportClientID != "" {
		contractImportBearer, err = platform.NewClientCredentialsTokenVerifier(startupCtx, platform.ClientCredentialsVerifierOptions{
			Issuer: cfg.ProjectContractImportIssuer, Audience: cfg.ProjectContractImportAudience, PublicKeyPath: cfg.ProjectContractImportPublicKeyPath,
			ClientID: cfg.ProjectContractImportClientID, TenantID: cfg.OIDCTenantID, CallerApplicationCode: "contract_management", CallerEnvironmentCode: cfg.ProjectContractImportCallerEnv, RequiredScope: "project.contract.import",
		})
		if err != nil {
			logger.Error("initialize contract import verifier", "error", err)
			os.Exit(1)
		}
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
	notifications := platform.NewNotificationPublisher(cfg.PlatformBaseURL, cfg.PlatformNotificationURL, cfg.PlatformNotificationClientID, cfg.PlatformNotificationSecret, cfg.PlatformNotificationScope)
	// BIZ-XAPI-10 分级策略：审计必开缺失 → 生产失败关闭；通知缺失 → 生产失败、
	// 非生产只 WARN（本地 outbox 保留积压，/readyz degraded 暴露）。
	destinationDecision := config.DecideIntegrationDestinations(config.IntegrationDestinationInput{
		AuditRequired:          cfg.AuditRequired,
		AuditConfigured:        auditConfig.Enabled,
		NotificationEnabled:    cfg.NotificationEnabled,
		NotificationConfigured: notifications != nil,
		Production:             cfg.ProductionEnvironment,
	})
	if destinationDecision.AuditFatal {
		logger.Error("platform audit destination is required but its configuration is incomplete",
			"missing_configuration", auditConfig.MissingFields)
		os.Exit(1)
	}
	if destinationDecision.AuditMissing {
		logger.Warn("platform audit reporting disabled; write operations will not be sent to the platform audit service", "missing_configuration", auditConfig.MissingFields)
	} else {
		logger.Info("platform audit reporting enabled")
	}
	if destinationDecision.NotificationFatal {
		logger.Error("NOTIFICATION_ENABLED=true but the platform notification publisher could not be configured in production")
		os.Exit(1)
	}
	if notifications != nil {
		logger.Info("platform notification integration enabled")
	} else if destinationDecision.NotificationMissing {
		logger.Warn("NOTIFICATION_ENABLED=true but the platform notification publisher is not configured; local outbox backlog will accumulate and /readyz stays degraded")
	} else {
		logger.Warn("platform notification integration disabled; automated notifications will stay in the local outbox")
	}
	integrationDestinations := &httpapi.IntegrationDestinationStatus{
		AuditRequired:          cfg.AuditRequired,
		AuditConfigured:        auditConfig.Enabled,
		NotificationRequired:   cfg.NotificationEnabled,
		NotificationConfigured: notifications != nil,
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
	service := &application.Service{NativeEnabled: cfg.ProjectNativeEnabled, Repo: repository, Personnel: personnel, Notifications: notifications, Contracts: contracts, EvidenceFiles: evidenceFiles, AuditStore: auditStore, Logger: logger,
		// BIZ-PROJECT-05：签到坐标与站点主数据的一致性格局由部署配置决定（阈值/站点缺失策略）。
		FieldCheckIn: application.FieldCheckInPolicy{SiteDistanceMeters: cfg.FieldCheckIn.SiteDistanceMeters, SiteMissingPolicy: cfg.FieldCheckIn.SiteMissingPolicy},
		// BIZ-PROJECT-06：缺项说明门禁的强度由部署配置决定（新装 enforce，存量升级先 advisory）。
		FieldEvidenceGate: application.FieldEvidenceGatePolicy{Mode: cfg.FieldIncompleteReasonGate}}
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
		ContractImport:      &httpapi.ContractImportOptions{Enabled: cfg.ProjectContractImportEnabled, BearerVerifier: contractImportBearer},
		SettlementReference: &httpapi.SettlementReferenceOptions{Enabled: cfg.SettlementReferenceEnabled, BearerVerifier: settlementReferenceBearer},
		CommercialLicense:   commercialGate,
		PendingMigrations:   pendingMigrations,
		// C14：能力维度快照；可选未启用只反映在 state=disabled，不影响整体就绪。
		Capabilities: map[string]any{
			"native_project":       map[string]any{"enabled": cfg.ProjectNativeEnabled, "state": readinessCapabilityState(cfg.ProjectNativeEnabled)},
			"contract_integration": map[string]any{"enabled": cfg.ContractIntegrationEnabled, "state": readinessCapabilityState(cfg.ContractIntegrationEnabled)},
			"approval_validation":  map[string]any{"enabled": cfg.ContractApprovalValidationEnabled, "state": readinessCapabilityState(cfg.ContractApprovalValidationEnabled)},
			"contract_import":      map[string]any{"enabled": cfg.ProjectContractImportEnabled, "state": readinessCapabilityState(cfg.ProjectContractImportEnabled)},
			"settlement_reference": map[string]any{"enabled": cfg.SettlementReferenceEnabled, "state": readinessCapabilityState(cfg.SettlementReferenceEnabled)},
		},
		IntegrationDestinations: integrationDestinations,
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
	serveErr := serveWithGracefulShutdown(server, shutdownCtx.Done(), logger)
	// B2：HTTP 排空之后取消后台上下文并按预算 join；超预算点名并非零退出。
	if drainErr := drainRuntime(ctx, lifeCancel, group, shutdown.Budget(), logger); drainErr != nil {
		os.Exit(1)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		logger.Error("server stopped", "error", serveErr)
		os.Exit(1)
	}
	logger.Info("project management API stopped", "background_goroutines", group.Names())
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

// readinessCapabilityState 按 C14 口径渲染能力状态：disabled 属可选未购买/被
// 停用，不计入就绪降级；enabled 表示配置校验通过后的运行态。
// 参数：enabled 为该能力开关的运行事实。
// 返回值：ready 或 disabled。
func readinessCapabilityState(enabled bool) string {
	if enabled {
		return "ready"
	}
	return "disabled"
}

// drainRuntime 取消后台生命周期上下文，并在预算内 join 全部后台 goroutine。
// 参数：ctx 为父上下文；cancel 取消后台循环；group 为 goroutine 登记表；
// budget 为 join 预算；logger 用于记录点名日志。
// 返回值：全部 join 成功为 nil；预算耗尽时返回点名错误（调用方据此非零退出）。
func drainRuntime(ctx context.Context, cancel context.CancelFunc, group *shutdown.Group, budget time.Duration, logger *slog.Logger) error {
	if cancel != nil {
		cancel()
	}
	drainCtx, stop := context.WithTimeout(ctx, budget)
	defer stop()
	if err := group.Drain(drainCtx); err != nil {
		logger.Error("shutdown drain exceeded budget", "error_code", "SHUTDOWN_DRAIN_DEADLINE", "error", err.Error(), "budget", budget.String())
		return err
	}
	return nil
}

// auditOutboxBacklog 把 auditoutbox.GORMStore 的积压快照适配成指标采集器需要的形状。
type auditOutboxBacklog struct{ store *auditoutbox.GORMStore }

// CountBacklog 读取审计 outbox 积压；查询失败原样返回，由采集器标记为“未知”。
func (source auditOutboxBacklog) CountBacklog(ctx context.Context) (temporalworker.OutboxBacklog, error) {
	counts, err := source.store.CountBacklog(ctx)
	return temporalworker.OutboxBacklog{Pending: counts.Pending, Dead: counts.Dead}, err
}
