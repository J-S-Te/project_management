package config

import "testing"

// 项目侧交接协议使用平台 application JWT 验签器（caller 应用/环境与唯一 scope 必须匹配）。
// 平台清单把 caller 应用与 audience 覆盖为平台自身的 application_code（platform）与
// application JWT audience（basic-platform-application）；这里钉住清单值真的能装载，
// 避免验签器把平台令牌当成其它子系统拒之门外。
func TestLoadHandoverIntegrationAcceptsPlatformCallerApplication(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HANDOVER_INTEGRATION_ENABLED", "true")
	t.Setenv("HANDOVER_INTEGRATION_REQUIRE_BEARER", "true")
	t.Setenv("HANDOVER_INTEGRATION_CLIENT_ID", "platform-handover")
	t.Setenv("HANDOVER_INTEGRATION_AUDIENCE", "basic-platform-application")
	t.Setenv("HANDOVER_INTEGRATION_ISSUER", "basic-platform")
	t.Setenv("HANDOVER_INTEGRATION_PUBLIC_KEY_PATH", "/app/data/keys/jwt-ed25519-public.pem")
	t.Setenv("HANDOVER_INTEGRATION_CALLER_APPLICATION_CODE", "platform")
	t.Setenv("HANDOVER_INTEGRATION_CALLER_ENVIRONMENT_CODE", "prod")
	t.Setenv("HANDOVER_INTEGRATION_REQUIRED_SCOPE", "handover.machine")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !c.HandoverIntegrationEnabled || !c.HandoverIntegrationRequireBearer {
		t.Fatalf("handover integration enabled = %+v", c)
	}
	if c.HandoverIntegrationCallerApp != "platform" {
		t.Fatalf("handover caller application = %q, want platform", c.HandoverIntegrationCallerApp)
	}
	if c.HandoverIntegrationIssuer != "basic-platform" || c.HandoverIntegrationAudience != "basic-platform-application" ||
		c.HandoverIntegrationClientID != "platform-handover" || c.HandoverIntegrationScope != "handover.machine" ||
		c.HandoverIntegrationCallerEnv != "prod" {
		t.Fatalf("handover integration binding = %+v", c)
	}
}

// 未提供平台清单时保持默认关闭；调用方应用/scope 的内建默认值必须已经等于清单值
// （platform / handover.machine），这样端上漏配 HANDOVER_INTEGRATION_CALLER_APPLICATION_CODE
// 也不会启动成功后全网 401。
func TestLoadDisablesHandoverIntegrationByDefault(t *testing.T) {
	setValidEnvironment(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.HandoverIntegrationEnabled {
		t.Fatalf("handover integration enabled by default: %+v", c)
	}
	if c.HandoverIntegrationCallerApp != "platform" || c.HandoverIntegrationScope != "handover.machine" {
		t.Fatalf("handover integration defaults = %q/%q, want platform/handover.machine", c.HandoverIntegrationCallerApp, c.HandoverIntegrationScope)
	}
}

// 平台清单只覆盖其余键、漏配 CALLER_APPLICATION_CODE 时，配置层默认值仍需与清单一致，
// 不能让端点把平台令牌判成其它 application_code 而全部 401。
func TestLoadDefaultsHandoverCallerApplicationToPlatform(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HANDOVER_INTEGRATION_ENABLED", "true")
	t.Setenv("HANDOVER_INTEGRATION_REQUIRE_BEARER", "true")
	t.Setenv("HANDOVER_INTEGRATION_CLIENT_ID", "platform-handover")
	t.Setenv("HANDOVER_INTEGRATION_AUDIENCE", "basic-platform-application")
	t.Setenv("HANDOVER_INTEGRATION_ISSUER", "basic-platform")
	t.Setenv("HANDOVER_INTEGRATION_PUBLIC_KEY_PATH", "/app/data/keys/jwt-ed25519-public.pem")
	t.Setenv("HANDOVER_INTEGRATION_CALLER_ENVIRONMENT_CODE", "prod")
	t.Setenv("HANDOVER_INTEGRATION_CALLER_APPLICATION_CODE", "")
	t.Setenv("HANDOVER_INTEGRATION_REQUIRED_SCOPE", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.HandoverIntegrationCallerApp != "platform" || c.HandoverIntegrationScope != "handover.machine" {
		t.Fatalf("handover caller app/scope defaults = %q/%q, want platform/handover.machine", c.HandoverIntegrationCallerApp, c.HandoverIntegrationScope)
	}
}

// 启用交接端点却关闭内部来源校验会让端点退化为“无 Bearer + 请求头租户”。
func TestLoadRejectsHandoverIntegrationWithoutBearer(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HANDOVER_INTEGRATION_ENABLED", "true")
	t.Setenv("HANDOVER_INTEGRATION_REQUIRE_BEARER", "false")

	if _, err := Load(); err == nil {
		t.Fatal("handover integration without bearer verification must fail closed")
	}
}
