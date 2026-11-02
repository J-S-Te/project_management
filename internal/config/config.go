package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	CentralSessionEnabled       bool
	CentralSessionPublicBaseURL string
	HTTPAddress                 string
	// TrustedProxies 是唯一允许采信 X-Real-IP / X-Forwarded-For 的直接对端集合。
	TrustedProxies                 []string
	MySQLDSN                       string
	TemporalAddress                string
	TemporalNamespace              string
	TemporalTaskQueue              string
	TemporalAPIKey                 string
	TemporalTLS                    bool
	TemporalWorkerBuildID          string
	TemporalWorkerDeploymentName   string
	TemporalWorkerVersioning       bool
	TemporalWorkerVersioningPolicy string
	TemporalMetricsAddress         string
	RunWorkerWithAPI               bool
	Background                     BackgroundConfig
	Snapshot                       ProjectSnapshotConfig
	FieldCheckIn                   FieldCheckInConfig
	// FieldIncompleteReasonGate 是现场完成"缺项说明"门禁的强度（BIZ-PROJECT-06）：
	// ADVISORY 只记录不拒绝（存量升级过渡），ENFORCE 执行 422 拒绝（新装默认）。
	FieldIncompleteReasonGate string
	PlatformBaseURL           string
	OIDCIssuer                string
	OIDCBackchannelBaseURL    string
	OIDCClientID              string
	OIDCClientSecret          string
	OIDCIDPHint               string
	OIDCRedirectURI           string
	OIDCPostLogoutRedirectURI string
	OIDCTenantID              string
	OIDCSessionCookieName     string
	// OIDCSessionIdleTTL 是无显式交互的最长容忍时间；OIDCSessionAbsoluteTTL 是
	// 从登录成功起不能延长的绝对上限。两者不能退化为单一绝对期限。
	OIDCSessionIdleTTL        time.Duration
	OIDCSessionAbsoluteTTL    time.Duration
	OIDCAuthorizationRefresh  time.Duration
	OIDCAuthorizationMaxStale time.Duration
	OIDCAuthorizationTimeout  time.Duration
	OIDCSessionEncryptionKey  []byte
	OIDCSessionSecure         bool
	AppPathPrefix             string
	PlatformApplicationCode   string
	PlatformEnvironmentCode   string
	PlatformApplicationID     string
	PlatformAuditClientID     string
	PlatformAuditClientSecret string
	// AuditRequired 是 BIZ-XAPI-10 的审计目的地必开判定：
	// 显式 PROJECT_AUDIT_REQUIRED 优先，兼容既有 PLATFORM_AUDIT_REQUIRED，
	// 否则 APP_ENV=production / PLATFORM_ENVIRONMENT_CODE=prod 视为生产必开。
	AuditRequired bool
	// ProductionEnvironment 用于“审计允许告警放行、通知必须失败关闭”的分级策略。
	ProductionEnvironment          bool
	OwnerDirectoryEnabled          bool
	PlatformOwnerDirectoryURL      string
	PlatformOwnerDirectoryClientID string
	PlatformOwnerDirectorySecret   string
	PlatformOwnerDirectoryScope    string

	// Notification 集成：自动化规则命中后把站内信投递给基础平台的统一 outbox。
	NotificationEnabled bool
	// SLA 扫描：SLA 超期/临近的主动提醒需要周期任务；租户列表显式给出，
	// 避免一条命令扫全库（系统侧任务不代替用户授权，必须限定范围）。
	SlaScanTenants                    []string
	SlaScanInterval                   time.Duration
	NotificationDispatchInterval      time.Duration
	PlatformNotificationURL           string
	PlatformNotificationClientID      string
	PlatformNotificationSecret        string
	PlatformNotificationScope         string
	PlatformCatalogSync               bool
	PlatformCatalogClientID           string
	PlatformCatalogClientSecret       string
	SettlementReferenceEnabled        bool
	SettlementReferenceAllowAnyClient bool
	SettlementReferenceClientID       string
	SettlementReferenceAudience       string
	SettlementReferenceIssuer         string
	SettlementReferencePublicKeyPath  string
	SettlementReferenceCallerEnv      string
	ContractIntegrationEnabled        bool
	ContractIntegrationRequireBearer  bool
	ContractIntegrationClientID       string
	ContractIntegrationAudience       string
	ContractIntegrationIssuer         string
	ContractIntegrationPublicKeyPath  string
	ContractIntegrationCallerApp      string
	ContractIntegrationCallerEnv      string
	ContractIntegrationScope          string

	ProjectContractImportEnabled       bool
	ProjectContractImportClientID      string
	ProjectContractImportAudience      string
	ProjectContractImportIssuer        string
	ProjectContractImportPublicKeyPath string
	ProjectContractImportCallerEnv     string

	// HandoverIntegration 是基础平台人员交接机器协议的入站配置。
	// 调用方应用/环境与唯一 scope 由跨系统契约固定：caller=platform/prod、scope=handover.machine；
	// 平台清单 HANDOVER_INTEGRATION_CALLER_APPLICATION_CODE 若漏配，默认值也必须落在 platform，
	// 否则验签器会把平台令牌判为其它子系统而一律 401。
	HandoverIntegrationEnabled             bool
	HandoverIntegrationRequireBearer       bool
	HandoverIntegrationClientID            string
	HandoverIntegrationAudience            string
	HandoverIntegrationIssuer              string
	HandoverIntegrationPublicKeyPath       string
	HandoverIntegrationCallerApp           string
	HandoverIntegrationCallerEnv           string
	HandoverIntegrationScope               string
	ContractApprovalValidationEnabled      bool
	ContractApprovalValidationURL          string
	ContractApprovalValidationClientID     string
	ContractApprovalValidationClientSecret string
	ContractApprovalValidationScope        string
	FileGatewayEnabled                     bool
	FileGatewayBaseURL                     string
	FileGatewayApplicationID               string
	FileGatewayClientID                    string
	FileGatewayClientSecret                string
	FileGatewayScope                       string
	DashboardMachineEnabled                bool
	DashboardMachineRequireBearer          bool
	DashboardMachineClientID               string
	DashboardMachineAudience               string
	DashboardMachineIssuer                 string
	DashboardMachinePublicKeyPath          string
	DashboardMachineCallerApp              string
	DashboardMachineCallerEnv              string
	DashboardMachineScope                  string
}

// Load 加载并校验项目运行配置和独立机器集成边界。
// 参数：无。
// 返回值：配置及缺失或非法配置错误。
func Load() (Config, error) {
	c := Config{
		HTTPAddress: env("PM_HTTP_ADDR", ":8082"), MySQLDSN: os.Getenv("MYSQL_DSN"),
		TrustedProxies:  splitList(env("APP_TRUSTED_PROXIES", "127.0.0.1/32,::1/128")),
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
		PlatformCatalogClientSecret:            os.Getenv("PLATFORM_AUTHORIZATION_CATALOG_CLIENT_SECRET"),
		PlatformOwnerDirectoryURL:              strings.TrimSpace(os.Getenv("PLATFORM_OWNER_DIRECTORY_URL")),
		PlatformOwnerDirectoryClientID:         strings.TrimSpace(os.Getenv("PLATFORM_OWNER_DIRECTORY_CLIENT_ID")),
		PlatformOwnerDirectorySecret:           os.Getenv("PLATFORM_OWNER_DIRECTORY_CLIENT_SECRET"),
		PlatformOwnerDirectoryScope:            strings.TrimSpace(os.Getenv("PLATFORM_OWNER_DIRECTORY_SCOPE")),
		PlatformNotificationURL:                strings.TrimSpace(os.Getenv("PLATFORM_NOTIFICATION_URL")),
		PlatformNotificationClientID:           strings.TrimSpace(os.Getenv("PLATFORM_NOTIFICATION_CLIENT_ID")),
		PlatformNotificationSecret:             os.Getenv("PLATFORM_NOTIFICATION_CLIENT_SECRET"),
		PlatformNotificationScope:              strings.TrimSpace(os.Getenv("PLATFORM_NOTIFICATION_SCOPE")),
		ProjectContractImportClientID:          strings.TrimSpace(os.Getenv("PROJECT_CONTRACT_IMPORT_CLIENT_ID")),
		ProjectContractImportAudience:          strings.TrimSpace(os.Getenv("PROJECT_CONTRACT_IMPORT_AUDIENCE")),
		ProjectContractImportIssuer:            strings.TrimSpace(os.Getenv("PROJECT_CONTRACT_IMPORT_ISSUER")),
		ProjectContractImportPublicKeyPath:     strings.TrimSpace(os.Getenv("PROJECT_CONTRACT_IMPORT_PUBLIC_KEY_PATH")),
		ProjectContractImportCallerEnv:         strings.TrimSpace(os.Getenv("PROJECT_CONTRACT_IMPORT_CALLER_ENVIRONMENT_CODE")),
		SettlementReferenceClientID:            strings.TrimSpace(os.Getenv("SETTLEMENT_REFERENCE_CLIENT_ID")),
		SettlementReferenceAudience:            strings.TrimSpace(os.Getenv("SETTLEMENT_REFERENCE_AUDIENCE")),
		SettlementReferenceIssuer:              strings.TrimSpace(os.Getenv("SETTLEMENT_REFERENCE_ISSUER")),
		SettlementReferencePublicKeyPath:       strings.TrimSpace(os.Getenv("SETTLEMENT_REFERENCE_PUBLIC_KEY_PATH")),
		SettlementReferenceCallerEnv:           strings.TrimSpace(os.Getenv("SETTLEMENT_REFERENCE_CALLER_ENVIRONMENT_CODE")),
		ContractIntegrationClientID:            strings.TrimSpace(os.Getenv("CONTRACT_INTEGRATION_CLIENT_ID")),
		ContractIntegrationAudience:            strings.TrimSpace(os.Getenv("CONTRACT_INTEGRATION_AUDIENCE")),
		ContractIntegrationIssuer:              env("CONTRACT_INTEGRATION_ISSUER", "basic-platform"),
		ContractIntegrationPublicKeyPath:       env("CONTRACT_INTEGRATION_PUBLIC_KEY_PATH", "/app/data/keys/jwt-ed25519-public.pem"),
		ContractIntegrationCallerApp:           env("CONTRACT_INTEGRATION_CALLER_APPLICATION_CODE", "contract_management"),
		ContractIntegrationCallerEnv:           env("CONTRACT_INTEGRATION_CALLER_ENVIRONMENT_CODE", strings.TrimSpace(os.Getenv("PLATFORM_ENVIRONMENT_CODE"))),
		ContractIntegrationScope:               env("CONTRACT_INTEGRATION_REQUIRED_SCOPE", "project.contract.import"),
		HandoverIntegrationClientID:            strings.TrimSpace(os.Getenv("HANDOVER_INTEGRATION_CLIENT_ID")),
		HandoverIntegrationAudience:            strings.TrimSpace(os.Getenv("HANDOVER_INTEGRATION_AUDIENCE")),
		HandoverIntegrationIssuer:              env("HANDOVER_INTEGRATION_ISSUER", "basic-platform"),
		HandoverIntegrationPublicKeyPath:       env("HANDOVER_INTEGRATION_PUBLIC_KEY_PATH", "/app/data/keys/jwt-ed25519-public.pem"),
		HandoverIntegrationCallerApp:           env("HANDOVER_INTEGRATION_CALLER_APPLICATION_CODE", "platform"),
		HandoverIntegrationCallerEnv:           env("HANDOVER_INTEGRATION_CALLER_ENVIRONMENT_CODE", strings.TrimSpace(os.Getenv("PLATFORM_ENVIRONMENT_CODE"))),
		HandoverIntegrationScope:               env("HANDOVER_INTEGRATION_REQUIRED_SCOPE", "handover.machine"),
		ContractApprovalValidationURL:          strings.TrimSpace(os.Getenv("CONTRACT_APPROVAL_VALIDATION_URL")),
		ContractApprovalValidationClientID:     strings.TrimSpace(os.Getenv("CONTRACT_APPROVAL_VALIDATION_CLIENT_ID")),
		ContractApprovalValidationClientSecret: os.Getenv("CONTRACT_APPROVAL_VALIDATION_CLIENT_SECRET"),
		ContractApprovalValidationScope:        strings.TrimSpace(os.Getenv("CONTRACT_APPROVAL_VALIDATION_SCOPE")),
		FileGatewayBaseURL:                     strings.TrimSpace(os.Getenv("FILE_GATEWAY_BASE_URL")),
		FileGatewayApplicationID:               strings.TrimSpace(os.Getenv("FILE_GATEWAY_APPLICATION_ID")),
		FileGatewayClientID:                    strings.TrimSpace(os.Getenv("FILE_GATEWAY_CLIENT_ID")),
		FileGatewayClientSecret:                os.Getenv("FILE_GATEWAY_CLIENT_SECRET"),
		FileGatewayScope:                       strings.TrimSpace(os.Getenv("FILE_GATEWAY_SCOPE")),
		DashboardMachineClientID:               strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_CLIENT_ID")),
		DashboardMachineAudience:               strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_AUDIENCE")),
		DashboardMachineIssuer:                 strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_ISSUER")),
		DashboardMachinePublicKeyPath:          strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_PUBLIC_KEY_PATH")),
		DashboardMachineCallerApp:              strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_CALLER_APPLICATION_CODE")),
		DashboardMachineCallerEnv:              strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_CALLER_ENVIRONMENT_CODE")),
		DashboardMachineScope:                  strings.TrimSpace(os.Getenv("DASHBOARD_MACHINE_REQUIRED_SCOPE")),
	}
	var err error
	if c.ProjectContractImportEnabled, err = strconv.ParseBool(env("PROJECT_CONTRACT_IMPORT_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("PROJECT_CONTRACT_IMPORT_ENABLED: %w", err)
	}
	if c.SettlementReferenceEnabled, err = strconv.ParseBool(env("SETTLEMENT_REFERENCE_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("SETTLEMENT_REFERENCE_ENABLED: %w", err)
	}
	if c.SettlementReferenceAllowAnyClient, err = strconv.ParseBool(env("SETTLEMENT_REFERENCE_ALLOW_ANY_CLIENT", "false")); err != nil {
		return c, fmt.Errorf("SETTLEMENT_REFERENCE_ALLOW_ANY_CLIENT: %w", err)
	}
	// 统一会话语义：空闲期限默认 30m，绝对期限默认 8h。
	if c.OIDCSessionIdleTTL, err = duration("OIDC_SESSION_IDLE_TTL", 30*time.Minute); err != nil {
		return c, err
	}
	// 兼容旧键：新绝对期限未显式设置时，旧 OIDC_SESSION_TTL 继续作为本次绝对期限，
	// 避免升级后把旧环境静默放大到 8 小时；新键与旧键同时存在时以新键为准。
	absoluteFallback := 8 * time.Hour
	if legacy := strings.TrimSpace(os.Getenv("OIDC_SESSION_TTL")); legacy != "" && strings.TrimSpace(os.Getenv("OIDC_SESSION_ABSOLUTE_TTL")) == "" {
		parsed, parseErr := time.ParseDuration(legacy)
		if parseErr != nil {
			return c, fmt.Errorf("OIDC_SESSION_TTL: %w", parseErr)
		}
		absoluteFallback = parsed
	}
	if c.OIDCSessionAbsoluteTTL, err = duration("OIDC_SESSION_ABSOLUTE_TTL", absoluteFallback); err != nil {
		return c, err
	}
	c.CentralSessionEnabled, err = strconv.ParseBool(env("CENTRAL_SESSION_ENABLED", "false"))
	if err != nil {
		return c, fmt.Errorf("CENTRAL_SESSION_ENABLED: %w", err)
	}
	c.CentralSessionPublicBaseURL = strings.TrimSpace(os.Getenv("CENTRAL_SESSION_PUBLIC_BASE_URL"))
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
	if c.NotificationEnabled, err = strconv.ParseBool(env("NOTIFICATION_ENABLED", "false")); err != nil {
		return Config{}, fmt.Errorf("NOTIFICATION_ENABLED: %w", err)
	}
	if c.SlaScanInterval, err = time.ParseDuration(env("SLA_SCAN_INTERVAL", "15m")); err != nil {
		return c, fmt.Errorf("SLA_SCAN_INTERVAL: %w", err)
	}
	if c.SlaScanInterval <= 0 {
		return c, fmt.Errorf("SLA_SCAN_INTERVAL must be positive")
	}
	if c.NotificationDispatchInterval, err = time.ParseDuration(env("NOTIFICATION_DISPATCH_INTERVAL", "15s")); err != nil {
		return c, fmt.Errorf("NOTIFICATION_DISPATCH_INTERVAL: %w", err)
	}
	if c.NotificationDispatchInterval <= 0 {
		return c, fmt.Errorf("NOTIFICATION_DISPATCH_INTERVAL must be positive")
	}
	c.SlaScanTenants = splitList(os.Getenv("SLA_SCAN_TENANTS"))
	if c.OwnerDirectoryEnabled, err = strconv.ParseBool(env("OWNER_DIRECTORY_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("OWNER_DIRECTORY_ENABLED: %w", err)
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
	if c.HandoverIntegrationEnabled, err = strconv.ParseBool(env("HANDOVER_INTEGRATION_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("HANDOVER_INTEGRATION_ENABLED: %w", err)
	}
	if c.HandoverIntegrationRequireBearer, err = strconv.ParseBool(env("HANDOVER_INTEGRATION_REQUIRE_BEARER", "true")); err != nil {
		return c, fmt.Errorf("HANDOVER_INTEGRATION_REQUIRE_BEARER: %w", err)
	}
	if c.ContractApprovalValidationEnabled, err = strconv.ParseBool(env("CONTRACT_APPROVAL_VALIDATION_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("CONTRACT_APPROVAL_VALIDATION_ENABLED: %w", err)
	}
	if c.FileGatewayEnabled, err = strconv.ParseBool(env("FILE_GATEWAY_ENABLED", "false")); err != nil {
		return c, fmt.Errorf("FILE_GATEWAY_ENABLED: %w", err)
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
	if c.FieldIncompleteReasonGate, err = fieldIncompleteReasonGate(); err != nil {
		return c, err
	}
	// BIZ-XAPI-10：审计必开判定与生产标记在启动装配、审计中间件与 /readyz 共用，
	// 避免三处各读一套环境变量产生漂移。
	if c.AuditRequired, err = ResolveAuditRequired(
		os.Getenv("PROJECT_AUDIT_REQUIRED"),
		os.Getenv("PLATFORM_AUDIT_REQUIRED"),
		os.Getenv("APP_ENV"),
		c.PlatformEnvironmentCode,
	); err != nil {
		return c, err
	}
	c.ProductionEnvironment = IsProduction(os.Getenv("APP_ENV"), c.PlatformEnvironmentCode)
	if err := c.loadFeatures(); err != nil {
		return c, err
	}
	return c, c.validate()
}

// 现场完成缺项说明门禁的两种强度（BIZ-PROJECT-06）。
const (
	FieldIncompleteReasonGateAdvisory = "ADVISORY"
	FieldIncompleteReasonGateEnforce  = "ENFORCE"
)

// fieldIncompleteReasonGate 读取缺项说明门禁的强度。
//
// 默认（键未配置）取 ADVISORY：存量部署通常没有这个键，若默认 enforce，旧前端仍用
// 窗口近似判定"缺项"并强制必填说明，而新后端按权威证据拒绝该说明，会形成短暂死锁。
// 因此默认只记录不拒绝，等前端跟进后再显式切 FIELD_INCOMPLETE_REASON_GATE=enforce；
// 全新部署若希望从第一天就是硬约束，直接显式配置 enforce 即可。
//
// 同时接受 PROJECT_ 前缀别名，与本仓其它键的命名习惯保持一致。
func fieldIncompleteReasonGate() (string, error) {
	raw := strings.ToUpper(strings.TrimSpace(env("FIELD_INCOMPLETE_REASON_GATE", "")))
	if raw == "" {
		raw = strings.ToUpper(strings.TrimSpace(os.Getenv("PROJECT_FIELD_INCOMPLETE_REASON_GATE")))
	}
	switch raw {
	case "":
		return FieldIncompleteReasonGateAdvisory, nil
	case FieldIncompleteReasonGateAdvisory, FieldIncompleteReasonGateEnforce:
		return raw, nil
	default:
		return "", fmt.Errorf("FIELD_INCOMPLETE_REASON_GATE must be advisory or enforce")
	}
}

// validate 检查运行配置和集成门禁。
// 参数：c 为待使用的配置。
// 返回值：配置不满足契约时返回错误。
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
	// 失败关闭：空闲期限超出运维可接受范围或绝对期限不足时拒绝启动，避免把
	// 15 分钟绝对到期或永不过期当成"修复"。（旧 OIDC_SESSION_TTL=15m 且未配置新键
	// 时会在这里失败，运维必须显式配置 OIDC_SESSION_IDLE_TTL/OIDC_SESSION_ABSOLUTE_TTL。）
	if c.OIDCSessionIdleTTL < 60*time.Second || c.OIDCSessionIdleTTL > 24*time.Hour {
		return fmt.Errorf("OIDC_SESSION_IDLE_TTL must be between 60s and 24h")
	}
	// 中央模式由平台裁定真实空闲与原认证截止，本地期限关系只约束旧模式。
	if !c.CentralSessionEnabled && c.OIDCSessionAbsoluteTTL <= c.OIDCSessionIdleTTL {
		return fmt.Errorf("OIDC_SESSION_ABSOLUTE_TTL must be greater than OIDC_SESSION_IDLE_TTL")
	}
	if c.OIDCAuthorizationRefresh <= 0 || c.OIDCAuthorizationRefresh >= c.OIDCSessionAbsoluteTTL || c.OIDCAuthorizationMaxStale < c.OIDCAuthorizationRefresh || c.OIDCAuthorizationMaxStale >= c.OIDCSessionAbsoluteTTL {
		return fmt.Errorf("OIDC refresh and maximum stale intervals must be positive, ordered and shorter than absolute session TTL")
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
	if c.OwnerDirectoryEnabled {
		for name, value := range map[string]string{
			"PLATFORM_OWNER_DIRECTORY_URL":           c.PlatformOwnerDirectoryURL,
			"PLATFORM_OWNER_DIRECTORY_CLIENT_ID":     c.PlatformOwnerDirectoryClientID,
			"PLATFORM_OWNER_DIRECTORY_CLIENT_SECRET": c.PlatformOwnerDirectorySecret,
		} {
			if strings.TrimSpace(value) == "" || placeholder(value) {
				return fmt.Errorf("%s is required when OWNER_DIRECTORY_ENABLED=true", name)
			}
		}
		if !validHTTPURL(c.PlatformOwnerDirectoryURL) {
			return fmt.Errorf("PLATFORM_OWNER_DIRECTORY_URL must be a valid HTTP(S) URL")
		}
		if c.PlatformOwnerDirectoryScope != "" && c.PlatformOwnerDirectoryScope != "owner_directory.read" {
			return fmt.Errorf("PLATFORM_OWNER_DIRECTORY_SCOPE must be owner_directory.read")
		}
	}
	if c.NotificationEnabled {
		missing := make([]string, 0, 3)
		for name, value := range map[string]string{
			"PLATFORM_NOTIFICATION_URL":           c.PlatformNotificationURL,
			"PLATFORM_NOTIFICATION_CLIENT_ID":     c.PlatformNotificationClientID,
			"PLATFORM_NOTIFICATION_CLIENT_SECRET": c.PlatformNotificationSecret,
		} {
			if strings.TrimSpace(value) == "" || placeholder(value) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			// BIZ-XAPI-10：通知目的地缺失不阻断非生产启动（启动 WARN + /readyz degraded +
			// 本地 outbox 积压告警）；生产仍失败关闭，避免“已声明启用但永远不投递”。
			if c.ProductionEnvironment {
				sort.Strings(missing)
				return fmt.Errorf("%s is required when NOTIFICATION_ENABLED=true in production", strings.Join(missing, ", "))
			}
		} else {
			if !validHTTPURL(c.PlatformNotificationURL) {
				return fmt.Errorf("PLATFORM_NOTIFICATION_URL must be a valid HTTP(S) URL")
			}
			if c.PlatformNotificationScope != "" && c.PlatformNotificationScope != "notification.ingest" {
				return fmt.Errorf("PLATFORM_NOTIFICATION_SCOPE must be notification.ingest")
			}
		}
	}
	if c.ContractIntegrationRequireBearer {
		if !c.ContractIntegrationEnabled {
			return fmt.Errorf("CONTRACT_INTEGRATION_REQUIRE_BEARER requires CONTRACT_INTEGRATION_ENABLED")
		}
		for name, value := range map[string]string{
			"CONTRACT_INTEGRATION_CLIENT_ID": c.ContractIntegrationClientID, "CONTRACT_INTEGRATION_AUDIENCE": c.ContractIntegrationAudience,
			"CONTRACT_INTEGRATION_ISSUER": c.ContractIntegrationIssuer, "CONTRACT_INTEGRATION_PUBLIC_KEY_PATH": c.ContractIntegrationPublicKeyPath,
			"CONTRACT_INTEGRATION_CALLER_APPLICATION_CODE": c.ContractIntegrationCallerApp,
			"CONTRACT_INTEGRATION_CALLER_ENVIRONMENT_CODE": c.ContractIntegrationCallerEnv,
			"CONTRACT_INTEGRATION_REQUIRED_SCOPE":          c.ContractIntegrationScope,
		} {
			if strings.TrimSpace(value) == "" || placeholder(value) {
				return fmt.Errorf("%s is required when bearer authentication is enabled", name)
			}
		}
	}
	// H4 修复：内部投递来源校验不可关闭。
	if c.HandoverIntegrationEnabled && !c.HandoverIntegrationRequireBearer {
		return fmt.Errorf("HANDOVER_INTEGRATION_REQUIRE_BEARER must be true when HANDOVER_INTEGRATION_ENABLED=true (internal delivery source verification is mandatory)")
	}
	if c.ContractIntegrationEnabled && !c.ContractIntegrationRequireBearer {
		return fmt.Errorf("CONTRACT_INTEGRATION_REQUIRE_BEARER must be true when CONTRACT_INTEGRATION_ENABLED=true (internal delivery source verification is mandatory)")
	}
	if c.ContractApprovalValidationEnabled {
		for name, value := range map[string]string{
			"CONTRACT_APPROVAL_VALIDATION_URL":           c.ContractApprovalValidationURL,
			"CONTRACT_APPROVAL_VALIDATION_CLIENT_ID":     c.ContractApprovalValidationClientID,
			"CONTRACT_APPROVAL_VALIDATION_CLIENT_SECRET": c.ContractApprovalValidationClientSecret,
			"CONTRACT_APPROVAL_VALIDATION_SCOPE":         c.ContractApprovalValidationScope,
		} {
			if strings.TrimSpace(value) == "" || placeholder(value) {
				return fmt.Errorf("%s is required when CONTRACT_APPROVAL_VALIDATION_ENABLED=true", name)
			}
		}
		if !validHTTPURL(c.ContractApprovalValidationURL) {
			return fmt.Errorf("CONTRACT_APPROVAL_VALIDATION_URL must be a valid HTTP(S) URL")
		}
		if c.ContractApprovalValidationScope != "contract.approved.internal.read" {
			return fmt.Errorf("CONTRACT_APPROVAL_VALIDATION_SCOPE must be contract.approved.internal.read")
		}
	}
	if c.FileGatewayEnabled {
		for name, value := range map[string]string{"FILE_GATEWAY_BASE_URL": c.FileGatewayBaseURL, "FILE_GATEWAY_APPLICATION_ID": c.FileGatewayApplicationID, "FILE_GATEWAY_CLIENT_ID": c.FileGatewayClientID, "FILE_GATEWAY_CLIENT_SECRET": c.FileGatewayClientSecret, "FILE_GATEWAY_SCOPE": c.FileGatewayScope} {
			if strings.TrimSpace(value) == "" || placeholder(value) {
				return fmt.Errorf("%s is required when FILE_GATEWAY_ENABLED=true", name)
			}
		}
		if !validHTTPURL(c.FileGatewayBaseURL) {
			return fmt.Errorf("FILE_GATEWAY_BASE_URL must be a valid HTTP(S) URL")
		}
		if c.FileGatewayScope != "platform:file:upload platform:file:bind platform:file:download" {
			return fmt.Errorf("FILE_GATEWAY_SCOPE must be platform:file:upload platform:file:bind platform:file:download")
		}
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
	if c.SettlementReferenceEnabled {
		// ALLOW_ANY_CLIENT=true 时不再固定结算 client_id：项目按平台应用、环境与
		// settlement.project_reference.read scope 授权验签，其余信任绑定仍必须完整。
		required := map[string]string{"AUDIENCE": c.SettlementReferenceAudience, "ISSUER": c.SettlementReferenceIssuer, "PUBLIC_KEY_PATH": c.SettlementReferencePublicKeyPath, "CALLER_ENVIRONMENT_CODE": c.SettlementReferenceCallerEnv}
		if !c.SettlementReferenceAllowAnyClient {
			required["CLIENT_ID"] = c.SettlementReferenceClientID
		}
		for name, value := range required {
			if strings.TrimSpace(value) == "" || placeholder(value) {
				return fmt.Errorf("SETTLEMENT_REFERENCE_%s is required when reference integration is enabled", name)
			}
		}
	}
	if c.ProjectContractImportEnabled || c.ProjectContractImportClientID != "" {
		for name, value := range map[string]string{"CLIENT_ID": c.ProjectContractImportClientID, "AUDIENCE": c.ProjectContractImportAudience, "ISSUER": c.ProjectContractImportIssuer, "PUBLIC_KEY_PATH": c.ProjectContractImportPublicKeyPath, "CALLER_ENVIRONMENT_CODE": c.ProjectContractImportCallerEnv} {
			if strings.TrimSpace(value) == "" || placeholder(value) {
				return fmt.Errorf("PROJECT_CONTRACT_IMPORT_%s is required for import authentication", name)
			}
		}
	}
	return c.validateFeatures()
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

// ResolveAuditRequired 解析审计目的地是否必开（BIZ-XAPI-10）。
// 参数：explicitProject 为新键 PROJECT_AUDIT_REQUIRED，explicitPlatform 为兼容既有
// PLATFORM_AUDIT_REQUIRED，appEnv 为 APP_ENV，environmentCode 为 PLATFORM_ENVIRONMENT_CODE。
// 返回值：生效判定；显式值非法时返回错误（失败关闭）。
// 判定顺序：新键 > 旧键 > APP_ENV=production > 生产环境编码。兼容旧键是为了不改变
// 既有部署的默认语义；新部署应使用 PROJECT_AUDIT_REQUIRED。
func ResolveAuditRequired(explicitProject, explicitPlatform, appEnv, environmentCode string) (bool, error) {
	for _, item := range []struct{ name, value string }{
		{"PROJECT_AUDIT_REQUIRED", explicitProject},
		{"PLATFORM_AUDIT_REQUIRED", explicitPlatform},
	} {
		if raw := strings.TrimSpace(item.value); raw != "" {
			required, err := strconv.ParseBool(raw)
			if err != nil {
				return false, fmt.Errorf("%s: %w", item.name, err)
			}
			return required, nil
		}
	}
	if env := strings.TrimSpace(appEnv); env != "" {
		return strings.EqualFold(env, "production"), nil
	}
	return isProductionEnvironmentCode(environmentCode), nil
}

// AuditRequiredFromEnvironment 是中间件与启动装配共用的判定入口。
// 参数：无，从进程环境读取开关与生产特征。
// 返回值：生效判定；非法显式值按失败关闭处理（true）。
func AuditRequiredFromEnvironment() bool {
	required, err := ResolveAuditRequired(
		os.Getenv("PROJECT_AUDIT_REQUIRED"),
		os.Getenv("PLATFORM_AUDIT_REQUIRED"),
		os.Getenv("APP_ENV"),
		os.Getenv("PLATFORM_ENVIRONMENT_CODE"),
	)
	if err != nil {
		return true
	}
	return required
}

// IsProduction 判定当前进程是否生产环境：APP_ENV 优先，其次平台环境编码。
func IsProduction(appEnv, environmentCode string) bool {
	if env := strings.TrimSpace(appEnv); env != "" {
		return strings.EqualFold(env, "production")
	}
	return isProductionEnvironmentCode(environmentCode)
}

// isProductionEnvironmentCode 按平台环境编码判断生产：兼容 prod 与 production。
func isProductionEnvironmentCode(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "prod", "production":
		return true
	default:
		return false
	}
}

// IntegrationDestinationInput 是启动期审计/通知目的地检查的只读输入。
type IntegrationDestinationInput struct {
	AuditRequired          bool
	AuditConfigured        bool
	NotificationEnabled    bool
	NotificationConfigured bool
	Production             bool
}

// IntegrationDestinationDecision 是分级策略结果（BIZ-XAPI-10）：
// 审计 required 缺失为致命；通知缺失只在生产致命，非生产降级告警。
type IntegrationDestinationDecision struct {
	AuditMissing         bool
	AuditFatal           bool
	NotificationMissing  bool
	NotificationFatal    bool
	DegradedDestinations []string
}

// DecideIntegrationDestinations 把“目的地是否配置”翻译为启动策略。
// 参数：in 为开关、配置完整性与生产标记。
// 返回值：致命/降级项；供 main 决定 os.Exit(1) 或 WARN。
func DecideIntegrationDestinations(in IntegrationDestinationInput) IntegrationDestinationDecision {
	decision := IntegrationDestinationDecision{}
	if !in.AuditConfigured {
		decision.AuditMissing = true
		decision.AuditFatal = in.AuditRequired
		decision.DegradedDestinations = append(decision.DegradedDestinations, "PLATFORM_AUDIT")
	}
	if in.NotificationEnabled && !in.NotificationConfigured {
		decision.NotificationMissing = true
		decision.NotificationFatal = in.Production
		decision.DegradedDestinations = append(decision.DegradedDestinations, "PLATFORM_NOTIFICATION")
	}
	return decision
}

func placeholder(value string) bool {
	upper := strings.ToUpper(strings.TrimSpace(value))
	return strings.Contains(upper, "PENDING") || strings.Contains(upper, "CHANGEME") || strings.Contains(upper, "EXAMPLE.COM")
}

// splitList 把逗号分隔的环境变量解析成去空、去重的列表。
func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
