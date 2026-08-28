package temporalworker

import (
	"context"
	"testing"

	"go.temporal.io/sdk/client"
)

type deploymentHandleStub struct {
	current client.WorkerDeploymentSetCurrentVersionOptions
	ramping client.WorkerDeploymentSetRampingVersionOptions
}

func (stub *deploymentHandleStub) Describe(context.Context, client.WorkerDeploymentDescribeOptions) (client.WorkerDeploymentDescribeResponse, error) {
	return client.WorkerDeploymentDescribeResponse{ConflictToken: []byte("conflict-1")}, nil
}
func (stub *deploymentHandleStub) SetCurrentVersion(_ context.Context, options client.WorkerDeploymentSetCurrentVersionOptions) (client.WorkerDeploymentSetCurrentVersionResponse, error) {
	stub.current = options
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
