package config

import (
	"strings"
	"testing"
)

// BIZ-PROJECT-06：缺项说明门禁必须是可配置的过渡开关。
// 未配置（存量部署常见）默认 ADVISORY，避免旧前端窗口误判与新后端证据口径形成死锁；
// 全新部署或前端跟进后显式配 enforce；无法识别的取值必须拒绝启动，不能悄悄退化成"不校验"。
func TestLoadFieldIncompleteReasonGateDefaultsToAdvisory(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("FIELD_INCOMPLETE_REASON_GATE", "")
	t.Setenv("PROJECT_FIELD_INCOMPLETE_REASON_GATE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FieldIncompleteReasonGate != FieldIncompleteReasonGateAdvisory {
		t.Fatalf("默认门禁 = %q, want ADVISORY", cfg.FieldIncompleteReasonGate)
	}
}

func TestLoadFieldIncompleteReasonGateOverrides(t *testing.T) {
	for _, testCase := range []struct{ key, value, want string }{
		{key: "FIELD_INCOMPLETE_REASON_GATE", value: "advisory", want: FieldIncompleteReasonGateAdvisory},
		{key: "FIELD_INCOMPLETE_REASON_GATE", value: " ENFORCE ", want: FieldIncompleteReasonGateEnforce},
		// 兼容本仓 PROJECT_ 前缀命名习惯的别名键。
		{key: "PROJECT_FIELD_INCOMPLETE_REASON_GATE", value: "advisory", want: FieldIncompleteReasonGateAdvisory},
	} {
		t.Run(testCase.key+"="+testCase.value, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("FIELD_INCOMPLETE_REASON_GATE", "")
			t.Setenv("PROJECT_FIELD_INCOMPLETE_REASON_GATE", "")
			t.Setenv(testCase.key, testCase.value)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.FieldIncompleteReasonGate != testCase.want {
				t.Fatalf("门禁 = %q, want %q", cfg.FieldIncompleteReasonGate, testCase.want)
			}
		})
	}
}

func TestLoadFieldIncompleteReasonGateRejectsUnknownValue(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("FIELD_INCOMPLETE_REASON_GATE", "log-only")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "FIELD_INCOMPLETE_REASON_GATE") {
		t.Fatalf("unknown gate value error = %v", err)
	}
}
