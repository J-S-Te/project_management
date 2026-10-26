package platform

import "testing"

// 统一审计方案 §2.2：机器事件必须显式标注，不能伪装成人工操作；
// 未知/空值按 USER 兜底，避免平台拒收。
func TestActorTypeOrDefault(t *testing.T) {
	cases := map[string]string{
		"":         "USER",
		"user":     "USER",
		"SYSTEM":   "SYSTEM",
		"machine":  "MACHINE",
		"unknown":  "USER",
		"  system": "SYSTEM",
	}
	for input, want := range cases {
		if got := actorTypeOrDefault(input); got != want {
			t.Errorf("actorTypeOrDefault(%q) = %q, want %q", input, got, want)
		}
	}
}
