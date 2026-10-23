package background

import (
	"testing"
	"time"
)

func validOptions() Options {
	return Options{Namespace: "default", Engine: EngineTemporal, TenantID: "tenant-a", Environment: "test", TaskQueue: "project-management-background-v1", DeploymentName: "project-management-background", BuildID: "sha-123", HealthAddress: "127.0.0.1:9081", ControlSocket: "/tmp/project-background/control.sock", VersioningEnabled: true, SLAInterval: 15 * time.Minute, DispatchInterval: 15 * time.Second, RunBudget: 4 * time.Minute, ActivityTimeout: 55 * time.Second, SLAActivityTimeout: 2 * time.Minute, WorkflowRunTimeout: 6 * time.Minute, MaxItems: 50}
}

func TestOptionsValidate(t *testing.T) {
	if err := validOptions().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Options)
	}{
		{"namespace", func(o *Options) { o.Namespace = "" }},
		{"engine", func(o *Options) { o.Engine = "fallback" }},
		{"tenant", func(o *Options) { o.TenantID = "" }},
		{"environment", func(o *Options) { o.Environment = "" }},
		{"queue", func(o *Options) { o.TaskQueue = "project-management" }},
		{"deployment", func(o *Options) { o.DeploymentName = "" }},
		{"deployment reserved dot", func(o *Options) { o.DeploymentName = "project.background" }},
		{"build", func(o *Options) { o.BuildID = "" }},
		{"versioning", func(o *Options) { o.VersioningEnabled = false }},
		{"health", func(o *Options) { o.HealthAddress = "invalid" }},
		{"socket", func(o *Options) { o.ControlSocket = "relative.sock" }},
		{"sla interval", func(o *Options) { o.SLAInterval = 0 }},
		{"dispatch interval", func(o *Options) { o.DispatchInterval = -1 }},
		{"budget", func(o *Options) { o.RunBudget = 4*time.Minute + 1 }},
		{"activity timeout", func(o *Options) { o.ActivityTimeout = 56 * time.Second }},
		{"sla timeout", func(o *Options) { o.SLAActivityTimeout = 2*time.Minute + 1 }},
		{"run timeout", func(o *Options) { o.WorkflowRunTimeout = 6*time.Minute + 1 }},
		{"zero items", func(o *Options) { o.MaxItems = 0 }},
		{"too many items", func(o *Options) { o.MaxItems = 51 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := validOptions()
			tc.change(&o)
			if err := o.Validate(); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
	o := validOptions()
	o.Engine = EngineLegacy
	o.BuildID = ""
	o.VersioningEnabled = false
	if err := o.Validate(); err != nil {
		t.Fatalf("explicit legacy requires Temporal build: %v", err)
	}
}
