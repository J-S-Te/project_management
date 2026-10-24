package config

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BackgroundConfig keeps the background executor separate from the legacy
// project-management deployment, including its controlled rollout identity.
type BackgroundConfig struct {
	Engine, TenantID, Environment                           string
	TaskQueue, DeploymentName, BuildID                      string
	HealthAddress, ControlSocket                            string
	VersioningEnabled                                       bool
	SLAInterval, DispatchInterval, RunBudget                time.Duration
	ActivityTimeout, SLAActivityTimeout, WorkflowRunTimeout time.Duration
	MaxItems                                                int
}

// ProjectSnapshotConfig bounds a complete export; it never permits publishing
// a truncated set of projects as a successful snapshot.
type ProjectSnapshotConfig struct {
	MaxProjects, MaxServiceItems, MaxBytes, MaxLineBytes int
	Timeout                                              time.Duration
}

var backgroundName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var backgroundBuild = regexp.MustCompile(`^project-background-sha256-[0-9a-f]{64}$`)

func (c *Config) loadFeatures() error {
	c.Background = BackgroundConfig{
		Engine:   strings.TrimSpace(env("PROJECT_BACKGROUND_ENGINE", "legacy")),
		TenantID: c.OIDCTenantID, Environment: c.PlatformEnvironmentCode,
		TaskQueue:         strings.TrimSpace(env("PROJECT_BACKGROUND_TASK_QUEUE", "project-management-background-v1")),
		DeploymentName:    strings.TrimSpace(env("PROJECT_BACKGROUND_DEPLOYMENT_NAME", "project-management-background")),
		BuildID:           strings.TrimSpace(env("PROJECT_BACKGROUND_BUILD_ID", "")),
		HealthAddress:     strings.TrimSpace(env("PROJECT_BACKGROUND_HEALTH_ADDRESS", "127.0.0.1:9093")),
		ControlSocket:     strings.TrimSpace(env("PROJECT_BACKGROUND_CONTROL_SOCKET", "/tmp/project-background/control.sock")),
		VersioningEnabled: true, SLAInterval: c.SlaScanInterval, DispatchInterval: c.NotificationDispatchInterval,
	}
	for _, item := range []struct {
		key               string
		value             *time.Duration
		fallback, maximum time.Duration
	}{
		{"PROJECT_BACKGROUND_RUN_BUDGET", &c.Background.RunBudget, 4 * time.Minute, 4 * time.Minute},
		{"PROJECT_BACKGROUND_ACTIVITY_TIMEOUT", &c.Background.ActivityTimeout, 55 * time.Second, 55 * time.Second},
		{"PROJECT_BACKGROUND_SLA_ACTIVITY_TIMEOUT", &c.Background.SLAActivityTimeout, 2 * time.Minute, 2 * time.Minute},
		{"PROJECT_BACKGROUND_WORKFLOW_RUN_TIMEOUT", &c.Background.WorkflowRunTimeout, 6 * time.Minute, 6 * time.Minute},
		{"PROJECT_SNAPSHOT_TIMEOUT", &c.Snapshot.Timeout, 30 * time.Second, 30 * time.Second},
	} {
		value, err := duration(item.key, item.fallback)
		if err != nil {
			return err
		}
		if value <= 0 || value > item.maximum {
			return fmt.Errorf("%s must be positive and not exceed %s", item.key, item.maximum)
		}
		*item.value = value
	}
	for _, item := range []struct {
		key     string
		value   *int
		maximum int
	}{
		{"PROJECT_BACKGROUND_MAX_ITEMS", &c.Background.MaxItems, 50},
		{"PROJECT_SNAPSHOT_MAX_PROJECTS", &c.Snapshot.MaxProjects, 50000},
		{"PROJECT_SNAPSHOT_MAX_SERVICE_ITEMS", &c.Snapshot.MaxServiceItems, 250000},
		{"PROJECT_SNAPSHOT_MAX_BYTES", &c.Snapshot.MaxBytes, 64 << 20},
		{"PROJECT_SNAPSHOT_MAX_LINE_BYTES", &c.Snapshot.MaxLineBytes, 64 << 10},
	} {
		value, err := strconv.Atoi(env(item.key, strconv.Itoa(item.maximum)))
		if err != nil || value <= 0 || value > item.maximum {
			return fmt.Errorf("%s must be between 1 and %d", item.key, item.maximum)
		}
		*item.value = value
	}
	if c.Background.Engine == "temporal" && len(c.SlaScanTenants) == 0 {
		c.SlaScanTenants = []string{c.OIDCTenantID}
	}
	return nil
}

func (c Config) validateFeatures() error {
	b := c.Background
	if b.Engine != "temporal" && b.Engine != "legacy" {
		return fmt.Errorf("PROJECT_BACKGROUND_ENGINE must be temporal or legacy")
	}
	if len(b.TenantID) > 64 || strings.TrimSpace(b.TenantID) != b.TenantID {
		return fmt.Errorf("OIDC_TENANT_ID must be a non-blank tenant ID of at most 64 bytes")
	}
	for _, item := range []struct{ key, value string }{
		{"PROJECT_BACKGROUND_TASK_QUEUE", b.TaskQueue}, {"PROJECT_BACKGROUND_DEPLOYMENT_NAME", b.DeploymentName},
	} {
		if !backgroundName.MatchString(item.value) || strings.Contains(item.value, "..") || item.value == "project-management" {
			return fmt.Errorf("%s must be a dedicated background name using letters, digits, underscore, dash or dot", item.key)
		}
	}
	if strings.Contains(b.DeploymentName, ".") {
		return fmt.Errorf("PROJECT_BACKGROUND_DEPLOYMENT_NAME must not contain a dot reserved by Temporal")
	}
	if b.Engine == "temporal" && !backgroundBuild.MatchString(b.BuildID) {
		return fmt.Errorf("PROJECT_BACKGROUND_BUILD_ID must be project-background-sha256- followed by the immutable image digest's 64 lowercase hex characters")
	}
	host, port, err := net.SplitHostPort(b.HealthAddress)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || (host != "" && host != "localhost" && net.ParseIP(host) == nil) {
		return fmt.Errorf("PROJECT_BACKGROUND_HEALTH_ADDRESS must be an IP or localhost and numeric TCP port")
	}
	if !filepath.IsAbs(b.ControlSocket) || filepath.Clean(b.ControlSocket) != b.ControlSocket || b.ControlSocket == "/" || len(b.ControlSocket) > 100 || strings.ContainsRune(b.ControlSocket, 0) {
		return fmt.Errorf("PROJECT_BACKGROUND_CONTROL_SOCKET must be a clean absolute file path of at most 100 bytes")
	}
	for _, tenant := range c.SlaScanTenants {
		if tenant != c.OIDCTenantID {
			return fmt.Errorf("SLA_SCAN_TENANTS must contain only OIDC_TENANT_ID")
		}
	}
	if b.RunBudget >= b.WorkflowRunTimeout || b.ActivityTimeout >= b.WorkflowRunTimeout || b.SLAActivityTimeout >= b.WorkflowRunTimeout {
		return fmt.Errorf("PROJECT_BACKGROUND_WORKFLOW_RUN_TIMEOUT must exceed run budget and activity timeouts")
	}
	return nil
}
