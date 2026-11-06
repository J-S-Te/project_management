package config

import (
	"strings"
	"testing"
)

// TestResolveAuditRequiredPrecedence 覆盖 BIZ-XAPI-10 的判定顺序：
// PROJECT_AUDIT_REQUIRED > 既有 PLATFORM_AUDIT_REQUIRED > APP_ENV=production > 生产环境编码。
func TestResolveAuditRequiredPrecedence(t *testing.T) {
	tests := []struct {
		name             string
		projectExplicit  string
		platformExplicit string
		appEnv           string
		environmentCode  string
		want             bool
	}{
		{name: "project explicit true wins", projectExplicit: "true", environmentCode: "dev", want: true},
		{name: "project explicit false wins over production", projectExplicit: "false", environmentCode: "prod", want: false},
		{name: "legacy platform explicit still honoured", platformExplicit: "true", environmentCode: "dev", want: true},
		{name: "project key wins over legacy key", projectExplicit: "false", platformExplicit: "true", environmentCode: "dev", want: false},
		{name: "app env production", appEnv: "production", environmentCode: "dev", want: true},
		{name: "app env non production wins over prod code", appEnv: "development", environmentCode: "prod", want: false},
		{name: "environment code prod fallback", environmentCode: "prod", want: true},
		{name: "environment code production alias", environmentCode: "production", want: true},
		{name: "dev defaults to not required", environmentCode: "dev", want: false},
		{name: "empty defaults to not required", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveAuditRequired(test.projectExplicit, test.platformExplicit, test.appEnv, test.environmentCode)
			if err != nil {
				t.Fatalf("ResolveAuditRequired() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveAuditRequired(%q,%q,%q,%q) = %v, want %v",
					test.projectExplicit, test.platformExplicit, test.appEnv, test.environmentCode, got, test.want)
			}
		})
	}
	for _, invalid := range []struct{ project, platform string }{{"yes", ""}, {"", "not-a-bool"}} {
		if _, err := ResolveAuditRequired(invalid.project, invalid.platform, "", "prod"); err == nil {
			t.Fatalf("invalid explicit switch project=%q platform=%q must fail closed", invalid.project, invalid.platform)
		}
	}
}

// TestDecideIntegrationDestinations 覆盖“审计 fail-closed、通知分级放行”的纯策略。
func TestDecideIntegrationDestinations(t *testing.T) {
	tests := []struct {
		name         string
		input        IntegrationDestinationInput
		wantFatal    bool
		wantDegraded []string
	}{
		{
			name:         "required audit missing is fatal",
			input:        IntegrationDestinationInput{AuditRequired: true, AuditConfigured: false},
			wantFatal:    true,
			wantDegraded: []string{"PLATFORM_AUDIT"},
		},
		{
			name:         "optional audit missing only degrades",
			input:        IntegrationDestinationInput{AuditRequired: false, AuditConfigured: false},
			wantFatal:    false,
			wantDegraded: []string{"PLATFORM_AUDIT"},
		},
		{
			name:         "notification missing is fatal in production",
			input:        IntegrationDestinationInput{AuditConfigured: true, NotificationEnabled: true, NotificationConfigured: false, Production: true},
			wantFatal:    true,
			wantDegraded: []string{"PLATFORM_NOTIFICATION"},
		},
		{
			name:         "notification missing only warns outside production",
			input:        IntegrationDestinationInput{AuditConfigured: true, NotificationEnabled: true, NotificationConfigured: false, Production: false},
			wantFatal:    false,
			wantDegraded: []string{"PLATFORM_NOTIFICATION"},
		},
		{
			name:         "fully configured has nothing to degrade",
			input:        IntegrationDestinationInput{AuditConfigured: true, NotificationEnabled: true, NotificationConfigured: true, Production: true},
			wantFatal:    false,
			wantDegraded: nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := DecideIntegrationDestinations(test.input)
			fatal := decision.AuditFatal || decision.NotificationFatal
			if fatal != test.wantFatal {
				t.Fatalf("decision = %+v, want fatal=%v", decision, test.wantFatal)
			}
			if strings.Join(decision.DegradedDestinations, ",") != strings.Join(test.wantDegraded, ",") {
				t.Fatalf("degraded = %v, want %v", decision.DegradedDestinations, test.wantDegraded)
			}
		})
	}
}

// TestLoadResolvesAuditRequiredAndProduction 确认配置加载与中间件共用同一判定。
func TestLoadResolvesAuditRequiredAndProduction(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("PROJECT_AUDIT_REQUIRED", "")
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "")
	t.Setenv("APP_ENV", "production")
	// C13：生产环境要求显式关闭内嵌后台，独立 project-sla-notifier 承担标准后台。
	t.Setenv("PROJECT_RUN_WORKER_WITH_API", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.AuditRequired || !cfg.ProductionEnvironment {
		t.Fatalf("AuditRequired=%v ProductionEnvironment=%v, want true/true", cfg.AuditRequired, cfg.ProductionEnvironment)
	}

	setValidEnvironment(t)
	t.Setenv("APP_ENV", "")
	t.Setenv("PLATFORM_ENVIRONMENT_CODE", "prod")
	t.Setenv("PROJECT_AUDIT_REQUIRED", "false")
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "")
	t.Setenv("PROJECT_RUN_WORKER_WITH_API", "false")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AuditRequired {
		t.Fatal("explicit PROJECT_AUDIT_REQUIRED=false must win over production environment")
	}
	if !cfg.ProductionEnvironment {
		t.Fatal("ProductionEnvironment = false, want true for PLATFORM_ENVIRONMENT_CODE=prod")
	}
}

// TestLoadNotificationDestinationGrading 确认通知缺失只在生产失败关闭，
// 非生产保留 WARN/readyz degraded 的启动路径。
func TestLoadNotificationDestinationGrading(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("APP_ENV", "development")
	t.Setenv("NOTIFICATION_ENABLED", "true")
	t.Setenv("PLATFORM_NOTIFICATION_URL", "")
	t.Setenv("PLATFORM_NOTIFICATION_CLIENT_ID", "")
	t.Setenv("PLATFORM_NOTIFICATION_CLIENT_SECRET", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("non-production missing notification destination must not block startup: %v", err)
	}
	if !cfg.NotificationEnabled || cfg.ProductionEnvironment {
		t.Fatalf("NotificationEnabled=%v ProductionEnvironment=%v", cfg.NotificationEnabled, cfg.ProductionEnvironment)
	}
	decision := DecideIntegrationDestinations(IntegrationDestinationInput{
		AuditRequired: cfg.AuditRequired, AuditConfigured: true,
		NotificationEnabled: cfg.NotificationEnabled, NotificationConfigured: false,
		Production: cfg.ProductionEnvironment,
	})
	if decision.NotificationFatal || !decision.NotificationMissing {
		t.Fatalf("decision = %+v, want non-fatal notification degradation", decision)
	}

	setValidEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PROJECT_RUN_WORKER_WITH_API", "false")
	t.Setenv("NOTIFICATION_ENABLED", "true")
	t.Setenv("PLATFORM_NOTIFICATION_URL", "")
	t.Setenv("PLATFORM_NOTIFICATION_CLIENT_ID", "")
	t.Setenv("PLATFORM_NOTIFICATION_CLIENT_SECRET", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NOTIFICATION_ENABLED=true in production") {
		t.Fatalf("production Load() error = %v, want notification destination rejection", err)
	}
}
