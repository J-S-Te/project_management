package config

import "testing"

// TestNativeProjectConfigurationRequiresExplicitEnable 验证原生建项默认关闭并严格解析显式开关。
// 参数：t 为测试上下文。
// 返回值：无；配置语义不符时标记失败。
func TestNativeProjectConfigurationRequiresExplicitEnable(t *testing.T) {
	setValidEnvironment(t)
	for _, tc := range []struct {
		raw     string
		enabled bool
		invalid bool
	}{{"", false, false}, {"true", true, false}, {"false", false, false}, {"yes", false, true}} {
		t.Setenv("PROJECT_NATIVE_ENABLED", tc.raw)
		cfg, err := Load()
		if tc.invalid {
			if err == nil {
				t.Fatal("invalid native flag accepted")
			}
			continue
		}
		if err != nil || cfg.ProjectNativeEnabled != tc.enabled {
			t.Fatalf("flag=%q enabled=%v err=%v", tc.raw, cfg.ProjectNativeEnabled, err)
		}
	}
}
