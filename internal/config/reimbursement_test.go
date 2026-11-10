package config

import "testing"

// TestReimbursementConfigurationFailsClosed 验证默认关闭及用途客户端必须独立配置。
// 参数：t 为测试上下文。
// 返回值：无；缺失信任绑定或共用客户端被接受时测试失败。
func TestReimbursementConfigurationFailsClosed(t *testing.T) {
	t.Setenv("REIMBURSEMENT_INTEGRATION_ENABLED", "false")
	c, err := LoadReimbursementIntegration()
	if err != nil || c.Enabled {
		t.Fatal(c, err)
	}
	t.Setenv("REIMBURSEMENT_INTEGRATION_ENABLED", "true")
	if _, err = LoadReimbursementIntegration(); err == nil {
		t.Fatal("missing binding accepted")
	}
	for name, value := range map[string]string{"REIMBURSEMENT_REFERENCE_CLIENT_ID": "read-client", "REIMBURSEMENT_SPEND_CLIENT_ID": "spend-client", "REIMBURSEMENT_MACHINE_AUDIENCE": "project-internal", "REIMBURSEMENT_MACHINE_ISSUER": "https://platform.test", "REIMBURSEMENT_MACHINE_PUBLIC_KEY_PATH": "/tmp/test-public.pem", "REIMBURSEMENT_MACHINE_CALLER_ENVIRONMENT_CODE": "test"} {
		t.Setenv(name, value)
	}
	if _, err = LoadReimbursementIntegration(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REIMBURSEMENT_SPEND_CLIENT_ID", "read-client")
	if _, err = LoadReimbursementIntegration(); err == nil {
		t.Fatal("shared client accepted")
	}
}
