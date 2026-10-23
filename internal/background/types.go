package background

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

type Engine string

const (
	EngineTemporal Engine = "temporal"
	EngineLegacy   Engine = "legacy"
)

type TaskKind string

const (
	TaskSLA           TaskKind = "sla"
	TaskEffects       TaskKind = "effects"
	TaskNotifications TaskKind = "notifications"
)

const (
	WorkflowName              = "project.background.tick.v1"
	SLAActivityName           = "project.background.sla.v1"
	EffectsActivityName       = "project.background.effects.v1"
	NotificationsActivityName = "project.background.notifications.v1"
)

type TickInput struct {
	TenantID           string
	Kind               TaskKind
	Limit              int
	Budget             time.Duration
	ActivityTimeout    time.Duration
	SLAActivityTimeout time.Duration
}
type TickResult struct {
	Processed    int
	PausedReason string
}

type Options struct {
	Namespace          string
	Engine             Engine
	TenantID           string
	Environment        string
	TaskQueue          string
	DeploymentName     string
	BuildID            string
	HealthAddress      string
	ControlSocket      string
	VersioningEnabled  bool
	SLAInterval        time.Duration
	DispatchInterval   time.Duration
	RunBudget          time.Duration
	ActivityTimeout    time.Duration
	SLAActivityTimeout time.Duration
	WorkflowRunTimeout time.Duration
	MaxItems           int
}

func (o Options) Validate() error {
	if o.Engine != EngineTemporal && o.Engine != EngineLegacy {
		return fmt.Errorf("invalid background engine")
	}
	if !validTenant(o.TenantID) || strings.TrimSpace(o.Environment) == "" {
		return fmt.Errorf("background tenant and environment are required")
	}
	if strings.TrimSpace(o.TaskQueue) == "" || o.TaskQueue == "project-management" || strings.TrimSpace(o.DeploymentName) == "" {
		return fmt.Errorf("dedicated background queue and deployment are required")
	}
	if strings.Contains(o.DeploymentName, ".") {
		return fmt.Errorf("Temporal deployment name must not contain reserved dot")
	}
	if o.Engine == EngineTemporal {
		if strings.TrimSpace(o.Namespace) == "" {
			return fmt.Errorf("Temporal background namespace is required")
		}
		build := strings.TrimSpace(o.BuildID)
		if !o.VersioningEnabled || build == "" || build == "latest" || build == "dev" || build == "local" || build == "placeholder" {
			return fmt.Errorf("Temporal background requires versioning and an immutable build ID")
		}
	}
	if _, port, err := net.SplitHostPort(o.HealthAddress); err != nil || port == "" {
		return fmt.Errorf("invalid background health address")
	}
	if !filepath.IsAbs(o.ControlSocket) || filepath.Clean(o.ControlSocket) != o.ControlSocket || o.ControlSocket == string(filepath.Separator) {
		return fmt.Errorf("background control socket must be an absolute file path")
	}
	if o.SLAInterval <= 0 || o.DispatchInterval <= 0 {
		return fmt.Errorf("background intervals must be positive")
	}
	if o.RunBudget <= 0 || o.RunBudget > 4*time.Minute || o.ActivityTimeout <= 0 || o.ActivityTimeout > 55*time.Second || o.SLAActivityTimeout <= 0 || o.SLAActivityTimeout > 2*time.Minute || o.WorkflowRunTimeout <= 0 || o.WorkflowRunTimeout > 6*time.Minute {
		return fmt.Errorf("background timeouts exceed supported bounds")
	}
	if o.MaxItems <= 0 || o.MaxItems > 50 {
		return fmt.Errorf("background max items must be between 1 and 50")
	}
	return nil
}

func validTenant(tenant string) bool {
	return strings.TrimSpace(tenant) != "" && strings.TrimSpace(tenant) == tenant && len(tenant) <= 64
}

func activityName(kind TaskKind) (string, error) {
	switch kind {
	case TaskSLA:
		return SLAActivityName, nil
	case TaskEffects:
		return EffectsActivityName, nil
	case TaskNotifications:
		return NotificationsActivityName, nil
	default:
		return "", fmt.Errorf("invalid background task kind")
	}
}

func normalizeInput(input TickInput) (TickInput, error) {
	if !validTenant(input.TenantID) {
		return input, fmt.Errorf("invalid background tenant")
	}
	if _, err := activityName(input.Kind); err != nil {
		return input, err
	}
	if input.Limit < 0 || input.Budget < 0 || input.ActivityTimeout < 0 || input.ActivityTimeout > 55*time.Second || input.SLAActivityTimeout < 0 || input.SLAActivityTimeout > 2*time.Minute {
		return input, fmt.Errorf("invalid background tick bounds")
	}
	if input.Limit == 0 || input.Limit > 50 {
		input.Limit = 50
	}
	if input.Budget == 0 || input.Budget > 4*time.Minute {
		input.Budget = 4 * time.Minute
	}
	if input.ActivityTimeout == 0 {
		input.ActivityTimeout = 55 * time.Second
	}
	if input.SLAActivityTimeout == 0 {
		input.SLAActivityTimeout = 2 * time.Minute
	}
	return input, nil
}
