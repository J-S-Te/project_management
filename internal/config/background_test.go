package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadBackgroundAndSnapshotDefaults(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Background.Engine != "legacy" || cfg.Background.TenantID != cfg.OIDCTenantID || cfg.Background.Environment != "dev" {
		t.Fatalf("background compatibility defaults = %+v", cfg.Background)
	}
	if cfg.Background.MaxItems != 50 || cfg.Background.RunBudget != 4*time.Minute || cfg.Background.ActivityTimeout != 55*time.Second || cfg.Background.WorkflowRunTimeout != 6*time.Minute {
		t.Fatalf("background execution bounds = %+v", cfg.Background)
	}
	if cfg.Snapshot.MaxProjects != 50000 || cfg.Snapshot.MaxServiceItems != 250000 || cfg.Snapshot.MaxBytes != 64<<20 || cfg.Snapshot.MaxLineBytes != 64<<10 || cfg.Snapshot.Timeout != 30*time.Second {
		t.Fatalf("snapshot protection defaults = %+v", cfg.Snapshot)
	}
}

func TestLoadTemporalBackgroundRequiresReleaseBuildAndTrustedTenant(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("PROJECT_BACKGROUND_ENGINE", "temporal")
	t.Setenv("PROJECT_BACKGROUND_BUILD_ID", "project-worker-v1")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PROJECT_BACKGROUND_BUILD_ID") {
		t.Fatalf("placeholder build accepted: %v", err)
	}
	t.Setenv("PROJECT_BACKGROUND_BUILD_ID", "project-background-sha256-"+strings.Repeat("a", 64))
	t.Setenv("SLA_SCAN_INTERVAL", "2m")
	t.Setenv("NOTIFICATION_DISPATCH_INTERVAL", "2s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SlaScanTenants) != 1 || cfg.SlaScanTenants[0] != cfg.OIDCTenantID || cfg.Background.SLAInterval != 2*time.Minute || cfg.Background.DispatchInterval != 2*time.Second {
		t.Fatalf("trusted tenant or configured interval lost: %+v", cfg.Background)
	}
	t.Setenv("SLA_SCAN_TENANTS", cfg.OIDCTenantID+",another-tenant")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SLA_SCAN_TENANTS") {
		t.Fatalf("cross-tenant background configuration accepted: %v", err)
	}
}

func TestLoadRejectsUnsafeBackgroundAndSnapshotConfiguration(t *testing.T) {
	cases := []struct{ key, value string }{
		{"PROJECT_BACKGROUND_ENGINE", "fallback"},
		{"PROJECT_BACKGROUND_MAX_ITEMS", "51"},
		{"PROJECT_BACKGROUND_RUN_BUDGET", "0s"},
		{"PROJECT_BACKGROUND_ACTIVITY_TIMEOUT", "2m"},
		{"PROJECT_BACKGROUND_SLA_ACTIVITY_TIMEOUT", "3m"},
		{"PROJECT_BACKGROUND_WORKFLOW_RUN_TIMEOUT", "7m"},
		{"PROJECT_BACKGROUND_HEALTH_ADDRESS", "http://localhost:9093"},
		{"PROJECT_BACKGROUND_CONTROL_SOCKET", "relative/control.sock"},
		{"PROJECT_BACKGROUND_TASK_QUEUE", "../old-queue"},
		{"PROJECT_BACKGROUND_DEPLOYMENT_NAME", "background.qa"},
		{"PROJECT_SNAPSHOT_MAX_PROJECTS", "0"},
		{"PROJECT_SNAPSHOT_MAX_SERVICE_ITEMS", "250001"},
		{"PROJECT_SNAPSHOT_MAX_BYTES", "-1"},
		{"PROJECT_SNAPSHOT_MAX_LINE_BYTES", "65537"},
		{"PROJECT_SNAPSHOT_TIMEOUT", "44s"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("unsafe config %s accepted or diagnosed incorrectly: %v", tc.key, err)
			}
		})
	}
}

func TestLoadAcceptsLowerSnapshotLimits(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("PROJECT_SNAPSHOT_MAX_PROJECTS", "20")
	t.Setenv("PROJECT_SNAPSHOT_MAX_SERVICE_ITEMS", "40")
	t.Setenv("PROJECT_SNAPSHOT_MAX_BYTES", "1048576")
	t.Setenv("PROJECT_SNAPSHOT_MAX_LINE_BYTES", "4096")
	t.Setenv("PROJECT_SNAPSHOT_TIMEOUT", "5s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Snapshot.MaxProjects != 20 || cfg.Snapshot.MaxServiceItems != 40 || cfg.Snapshot.MaxBytes != 1048576 || cfg.Snapshot.MaxLineBytes != 4096 || cfg.Snapshot.Timeout != 5*time.Second {
		t.Fatalf("configured snapshot bounds lost: %+v", cfg.Snapshot)
	}
}
