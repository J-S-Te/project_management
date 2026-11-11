package config

import "testing"

// TestDashboardTrustCannotDriftToOtherMachinePurpose 验证看板用途固定调用方与scope。
// 参数：t为测试上下文。
// 返回值：无，其他业务机器用途可冒充看板时失败。
func TestDashboardTrustCannotDriftToOtherMachinePurpose(t *testing.T) {
	setValidEnvironment(t)
	for key, value := range map[string]string{"ENABLED": "true", "REQUIRE_BEARER": "true", "CLIENT_ID": "data-analysis-sub", "AUDIENCE": "basic-platform-application", "ISSUER": "basic-platform", "PUBLIC_KEY_PATH": "/synthetic/key.pem", "CALLER_APPLICATION_CODE": "contract_management", "CALLER_ENVIRONMENT_CODE": "test", "REQUIRED_SCOPE": "dashboard.project.read"} {
		t.Setenv("DASHBOARD_MACHINE_"+key, value)
	}
	if _, err := Load(); err == nil {
		t.Fatal("accepted other application as dashboard caller")
	}
	t.Setenv("DASHBOARD_MACHINE_CALLER_APPLICATION_CODE", "data_analysis")
	t.Setenv("DASHBOARD_MACHINE_REQUIRED_SCOPE", "project.contract.import")
	if _, err := Load(); err == nil {
		t.Fatal("accepted import purpose as dashboard scope")
	}
}
