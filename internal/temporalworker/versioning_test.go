package temporalworker

import (
	"testing"

	"go.temporal.io/sdk/workflow"
)

func TestWorkerOptionsEnableDeploymentVersioning(t *testing.T) {
	options, err := WorkerOptions(VersioningConfig{Enabled: true, DeploymentName: "project-management", BuildID: "project-v2", Policy: "PINNED"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.DeploymentOptions.UseVersioning || options.DeploymentOptions.Version.DeploymentName != "project-management" || options.DeploymentOptions.Version.BuildID != "project-v2" || options.DeploymentOptions.DefaultVersioningBehavior != workflow.VersioningBehaviorPinned {
		t.Fatalf("worker options = %#v", options)
	}
}

func TestWorkerOptionsRejectInvalidPolicy(t *testing.T) {
	if _, err := WorkerOptions(VersioningConfig{Enabled: true, DeploymentName: "project-management", BuildID: "project-v2", Policy: "latest"}); err == nil {
		t.Fatal("invalid policy was accepted")
	}
}
