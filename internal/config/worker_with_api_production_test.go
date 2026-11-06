package config

import (
	"strings"
	"testing"
)

// TestProductionRejectsEmbeddedWorkerDefault 验证 C13：生产环境缺显式
// PROJECT_RUN_WORKER_WITH_API=false（默认 true）时失败关闭，而不是仅告警后
// 继续内嵌后台。
// 参数：t 为当前测试上下文。
// 返回值：无；生产默认内嵌未被拒绝时失败。
func TestProductionRejectsEmbeddedWorkerDefault(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PROJECT_RUN_WORKER_WITH_API", "")
	_, err := Load()
	if err == nil {
		t.Fatal("生产环境默认内嵌 worker 未被拒绝")
	}
	if !strings.Contains(err.Error(), "PROJECT_RUN_WORKER_WITH_API") {
		t.Fatalf("错误未指向内嵌后台开关: %v", err)
	}
}

// TestProductionExplicitFalseAllowsStartup 验证生产显式 false 正常加载。
// 参数：t 为当前测试上下文。
// 返回值：无；显式关闭被误拒时失败。
func TestProductionExplicitFalseAllowsStartup(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("PROJECT_RUN_WORKER_WITH_API", "false")
	if _, err := Load(); err != nil {
		t.Fatalf("生产显式关闭内嵌后台被误拒: %v", err)
	}
}

// TestDevelopmentEmbeddedWorkerStillAllowed 验证开发裸跑单进程不受影响：
// 非生产环境默认内嵌仍可加载。
// 参数：t 为当前测试上下文。
// 返回值：无；开发模式被误拒时失败。
func TestDevelopmentEmbeddedWorkerStillAllowed(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("APP_ENV", "")
	t.Setenv("PLATFORM_ENVIRONMENT_CODE", "dev")
	t.Setenv("PROJECT_RUN_WORKER_WITH_API", "")
	if cfg, err := Load(); err != nil {
		t.Fatalf("开发默认内嵌被误拒: %v", err)
	} else if !cfg.RunWorkerWithAPI {
		t.Fatal("开发默认 RunWorkerWithAPI 应保持 true")
	}
}
