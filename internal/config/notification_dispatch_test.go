package config

import (
	"testing"
	"time"
)

func TestNotificationDispatchIntervalDefaultAndValidation(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("NOTIFICATION_DISPATCH_INTERVAL", "")
	cfg, err := Load()
	if err != nil || cfg.NotificationDispatchInterval != 15*time.Second {
		t.Fatalf("default=%v/%v", cfg.NotificationDispatchInterval, err)
	}
	for _, value := range []string{"0s", "-1s", "bad"} {
		t.Setenv("NOTIFICATION_DISPATCH_INTERVAL", value)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	t.Setenv("NOTIFICATION_DISPATCH_INTERVAL", "30s")
	cfg, err = Load()
	if err != nil || cfg.NotificationDispatchInterval != 30*time.Second {
		t.Fatal("valid configured interval rejected")
	}
}
