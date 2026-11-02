package config

import (
	"strings"
	"testing"
)

// 现场签到策略必须可配置：阈值决定"多远算偏离"，站点缺失策略决定历史/未登记站点
// 还能不能签到。默认值要让未配置的环境保持可用（1000 米 + 要求偏离原因）。
func TestLoadFieldCheckInDefaults(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("PROJECT_FIELD_CHECKIN_DISTANCE_METERS", "")
	t.Setenv("PROJECT_FIELD_CHECKIN_SITE_MISSING_POLICY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FieldCheckIn.SiteDistanceMeters != 1000 {
		t.Fatalf("默认距离阈值 = %v, want 1000", cfg.FieldCheckIn.SiteDistanceMeters)
	}
	if cfg.FieldCheckIn.SiteMissingPolicy != "REQUIRE_REASON" {
		t.Fatalf("默认站点缺失策略 = %q, want REQUIRE_REASON", cfg.FieldCheckIn.SiteMissingPolicy)
	}
}

func TestLoadFieldCheckInOverrides(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("PROJECT_FIELD_CHECKIN_DISTANCE_METERS", "250.5")
	t.Setenv("PROJECT_FIELD_CHECKIN_SITE_MISSING_POLICY", "block")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FieldCheckIn.SiteDistanceMeters != 250.5 {
		t.Fatalf("距离阈值 = %v, want 250.5", cfg.FieldCheckIn.SiteDistanceMeters)
	}
	if cfg.FieldCheckIn.SiteMissingPolicy != "BLOCK" {
		t.Fatalf("站点缺失策略 = %q, want BLOCK（大小写不敏感归一）", cfg.FieldCheckIn.SiteMissingPolicy)
	}
}

// 非法配置必须让启动失败：把阈值写成 0 等于关闭校验，把策略写错不能让服务
// 悄悄退化成"不校验"。
func TestLoadFieldCheckInRejectsInvalidValues(t *testing.T) {
	for _, testCase := range []struct{ name, key, value, messagePart string }{
		{"零阈值", "PROJECT_FIELD_CHECKIN_DISTANCE_METERS", "0", "PROJECT_FIELD_CHECKIN_DISTANCE_METERS"},
		{"负阈值", "PROJECT_FIELD_CHECKIN_DISTANCE_METERS", "-10", "PROJECT_FIELD_CHECKIN_DISTANCE_METERS"},
		{"非数字阈值", "PROJECT_FIELD_CHECKIN_DISTANCE_METERS", "abc", "PROJECT_FIELD_CHECKIN_DISTANCE_METERS"},
		{"过大阈值", "PROJECT_FIELD_CHECKIN_DISTANCE_METERS", "100001", "PROJECT_FIELD_CHECKIN_DISTANCE_METERS"},
		{"未知策略", "PROJECT_FIELD_CHECKIN_SITE_MISSING_POLICY", "WARN", "PROJECT_FIELD_CHECKIN_SITE_MISSING_POLICY"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(testCase.key, testCase.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), testCase.messagePart) {
				t.Fatalf("error = %v, want mention of %s", err, testCase.messagePart)
			}
		})
	}
}
