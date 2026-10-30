package config

import (
	"strings"
	"testing"
)

// TestProjectContractImportConfiguration 验证默认关闭和关闭后仍保留完整认证配置。
// 参数：t 为测试上下文。
// 返回值：无。
func TestProjectContractImportConfiguration(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("PROJECT_CONTRACT_IMPORT_ENABLED", "false")
	t.Setenv("PROJECT_CONTRACT_IMPORT_CLIENT_ID", "")
	cfg, err := Load()
	if err != nil || cfg.ProjectContractImportEnabled {
		t.Fatalf("default: %v", err)
	}
	t.Setenv("PROJECT_CONTRACT_IMPORT_ENABLED", "invalid")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PROJECT_CONTRACT_IMPORT_ENABLED") {
		t.Fatalf("invalid flag=%v", err)
	}
	t.Setenv("PROJECT_CONTRACT_IMPORT_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PROJECT_CONTRACT_IMPORT_") {
		t.Fatalf("missing auth=%v", err)
	}
	for key, value := range map[string]string{"CLIENT_ID": "contract-import-client", "AUDIENCE": "project-api", "ISSUER": "basic-platform", "PUBLIC_KEY_PATH": "/test/public.pem", "CALLER_ENVIRONMENT_CODE": "dev"} {
		t.Setenv("PROJECT_CONTRACT_IMPORT_"+key, value)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECT_CONTRACT_IMPORT_ENABLED", "false")
	cfg, err = Load()
	if err != nil || cfg.ProjectContractImportClientID == "" {
		t.Fatalf("recovery auth: %v", err)
	}
}
