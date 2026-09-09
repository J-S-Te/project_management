package temporalworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

type deploymentHandleStub struct {
	current         client.WorkerDeploymentSetCurrentVersionOptions
	ramping         client.WorkerDeploymentSetRampingVersionOptions
	currentVersion  *worker.WorkerDeploymentVersion
	setCurrentErrs  []error
	setCurrentCalls int
}

func (stub *deploymentHandleStub) Describe(context.Context, client.WorkerDeploymentDescribeOptions) (client.WorkerDeploymentDescribeResponse, error) {
	response := client.WorkerDeploymentDescribeResponse{ConflictToken: []byte("conflict-1")}
	response.Info.RoutingConfig.CurrentVersion = stub.currentVersion
	return response, nil
}
func (stub *deploymentHandleStub) SetCurrentVersion(_ context.Context, options client.WorkerDeploymentSetCurrentVersionOptions) (client.WorkerDeploymentSetCurrentVersionResponse, error) {
	stub.current = options
	stub.setCurrentCalls++
	if stub.setCurrentCalls <= len(stub.setCurrentErrs) {
		return client.WorkerDeploymentSetCurrentVersionResponse{}, stub.setCurrentErrs[stub.setCurrentCalls-1]
	}
	return client.WorkerDeploymentSetCurrentVersionResponse{}, nil
}
func (stub *deploymentHandleStub) SetRampingVersion(_ context.Context, options client.WorkerDeploymentSetRampingVersionOptions) (client.WorkerDeploymentSetRampingVersionResponse, error) {
	stub.ramping = options
	return client.WorkerDeploymentSetRampingVersionResponse{}, nil
}

func TestPromoteCurrentUsesConflictTokenAndPollerProtection(t *testing.T) {
	stub := &deploymentHandleStub{}
	if err := PromoteCurrent(context.Background(), stub, "project-v2", "release-42"); err != nil {
		t.Fatal(err)
	}
	if stub.current.BuildID != "project-v2" || string(stub.current.ConflictToken) != "conflict-1" || stub.current.AllowNoPollers || stub.current.IgnoreMissingTaskQueues {
		t.Fatalf("promote options = %#v", stub.current)
	}
}

func TestRampVersionOnlyAcceptsReleaseSteps(t *testing.T) {
	for _, percentage := range []float32{5, 25, 50, 100} {
		stub := &deploymentHandleStub{}
		if err := RampVersion(context.Background(), stub, "project-v2", "release-42", percentage); err != nil || stub.ramping.Percentage != percentage || string(stub.ramping.ConflictToken) != "conflict-1" || stub.ramping.IgnoreMissingTaskQueues {
			t.Fatalf("percentage=%v options=%#v err=%v", percentage, stub.ramping, err)
		}
	}
	if err := RampVersion(context.Background(), &deploymentHandleStub{}, "project-v2", "release-42", 10); err == nil {
		t.Fatal("unsupported release percentage was accepted")
	}
}

func TestAbortRampClearsBuildID(t *testing.T) {
	stub := &deploymentHandleStub{}
	if err := RampVersion(context.Background(), stub, "", "release-42", 0); err != nil || stub.ramping.BuildID != "" || stub.ramping.Percentage != 0 {
		t.Fatalf("abort options=%#v err=%v", stub.ramping, err)
	}
}

func TestEnsureCurrentVersionSkipsWhenBuildIDAlreadyCurrent(t *testing.T) {
	stub := &deploymentHandleStub{currentVersion: &worker.WorkerDeploymentVersion{DeploymentName: "project-management", BuildID: "project-v2"}}
	if err := EnsureCurrentVersion(context.Background(), stub, "project-v2", "startup-auto-promote:project-v2"); err != nil {
		t.Fatal(err)
	}
	if stub.setCurrentCalls != 0 {
		t.Fatalf("already current version triggered %d promotions", stub.setCurrentCalls)
	}
}

func TestEnsureCurrentVersionPromotesMissingCurrent(t *testing.T) {
	stub := &deploymentHandleStub{}
	if err := EnsureCurrentVersion(context.Background(), stub, "project-v2", "startup-auto-promote:project-v2"); err != nil {
		t.Fatal(err)
	}
	if stub.current.BuildID != "project-v2" || string(stub.current.ConflictToken) != "conflict-1" || stub.current.AllowNoPollers || stub.current.IgnoreMissingTaskQueues {
		t.Fatalf("promote options = %#v", stub.current)
	}
	if stub.current.Identity != "startup-auto-promote:project-v2" {
		t.Fatalf("identity = %q", stub.current.Identity)
	}
}

func TestEnsureCurrentVersionRetriesUntilPollerRegistered(t *testing.T) {
	restoreRetryInterval := shortenCurrentVersionRetryInterval()
	defer restoreRetryInterval()
	stub := &deploymentHandleStub{setCurrentErrs: []error{errors.New("no pollers registered"), errors.New("no pollers registered")}}
	if err := EnsureCurrentVersion(context.Background(), stub, "project-v2", "startup-auto-promote:project-v2"); err != nil {
		t.Fatal(err)
	}
	if stub.setCurrentCalls != 3 {
		t.Fatalf("promote attempts = %d, want 3", stub.setCurrentCalls)
	}
}

func TestEnsureCurrentVersionReturnsLastErrorAfterExhaustingAttempts(t *testing.T) {
	restoreRetryInterval := shortenCurrentVersionRetryInterval()
	defer restoreRetryInterval()
	stub := &deploymentHandleStub{setCurrentErrs: make([]error, currentVersionAttempts)}
	for index := range stub.setCurrentErrs {
		stub.setCurrentErrs[index] = errors.New("no pollers registered")
	}
	if err := EnsureCurrentVersion(context.Background(), stub, "project-v2", "startup-auto-promote:project-v2"); err == nil {
		t.Fatal("expected failure when no poller ever registers")
	}
	if stub.setCurrentCalls != currentVersionAttempts {
		t.Fatalf("promote attempts = %d, want %d", stub.setCurrentCalls, currentVersionAttempts)
	}
}

func TestEnsureCurrentVersionRejectsMissingInputs(t *testing.T) {
	if err := EnsureCurrentVersion(context.Background(), nil, "project-v2", "startup"); err == nil {
		t.Fatal("nil deployment handle was accepted")
	}
	if err := EnsureCurrentVersion(context.Background(), &deploymentHandleStub{}, "  ", "startup"); err == nil {
		t.Fatal("empty build ID was accepted")
	}
	if err := EnsureCurrentVersion(context.Background(), &deploymentHandleStub{}, "project-v2", "  "); err == nil {
		t.Fatal("empty identity was accepted")
	}
}

func TestEnsureCurrentVersionOnStartupIgnoresDisabledOrUnconfigured(t *testing.T) {
	EnsureCurrentVersionOnStartup(context.Background(), nil, nil, VersioningConfig{Enabled: true, DeploymentName: "project-management", BuildID: "project-v2"})
	EnsureCurrentVersionOnStartup(context.Background(), nil, nil, VersioningConfig{})
}

func shortenCurrentVersionRetryInterval() func() {
	previous := currentVersionRetryInterval
	currentVersionRetryInterval = time.Millisecond
	return func() { currentVersionRetryInterval = previous }
}
