package temporalworker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func probeWorkflow(ctx workflow.Context) (string, error) {
	return "ok", nil
}

// TestLocalTemporalAutoPromote 用真实 Temporal 复现生产故障模式并验证修复：
// 版本路由开启而 Current 版本为空时，新工作流以 UNVERSIONED 入队且无人领取；
// EnsureCurrentVersion 提升后，既有的未版本化工作流与后续新工作流都能被消费完成。
func TestLocalTemporalAutoPromote(t *testing.T) {
	address := os.Getenv("PM_TEMPORAL_E2E_ADDRESS")
	if address == "" {
		t.Skip("set PM_TEMPORAL_E2E_ADDRESS to run the local Temporal end-to-end check")
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	deploymentName := "e2e-auto-promote-" + suffix
	buildID := "build-" + suffix
	taskQueue := "e2e-auto-promote-" + suffix

	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	defer cancelDial()
	temporalClient, err := client.DialContext(dialCtx, client.Options{
		HostPort: address, Namespace: "default",
		ConnectionOptions: client.ConnectionOptions{TLSDisabled: true},
	})
	if err != nil {
		t.Fatalf("dial temporal: %v", err)
	}
	defer temporalClient.Close()

	options, err := WorkerOptions(VersioningConfig{Enabled: true, DeploymentName: deploymentName, BuildID: buildID, Policy: "PINNED"})
	if err != nil {
		t.Fatalf("worker options: %v", err)
	}
	w := worker.New(temporalClient, taskQueue, options)
	w.RegisterWorkflowWithOptions(probeWorkflow, workflow.RegisterOptions{Name: "e2e.probe.v1"})
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer w.Stop()

	// 提升之前：工作流以 UNVERSIONED 入队，没有任何 Poller 消费，因此不会完成。
	beforeRun, err := temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "e2e-before-" + suffix, TaskQueue: taskQueue, WorkflowExecutionTimeout: 60 * time.Second,
	}, "e2e.probe.v1")
	if err != nil {
		t.Fatalf("start workflow before promotion: %v", err)
	}
	if err := waitForRun(ctx, beforeRun, 3*time.Second); err == nil {
		t.Fatal("workflow completed before promotion; expected it to stay undispatched")
	} else {
		t.Logf("before promotion (expected still running): %v", err)
	}

	handle := temporalClient.WorkerDeploymentClient().GetHandle(deploymentName)
	if err := EnsureCurrentVersion(ctx, handle, buildID, "e2e-auto-promote:"+buildID); err != nil {
		t.Fatalf("ensure current version: %v", err)
	}
	description, err := handle.Describe(ctx, client.WorkerDeploymentDescribeOptions{})
	if err != nil {
		t.Fatalf("describe deployment: %v", err)
	}
	if current := description.Info.RoutingConfig.CurrentVersion; current == nil || current.BuildID != buildID {
		t.Fatalf("current version = %#v, want build %q", description.Info.RoutingConfig.CurrentVersion, buildID)
	}

	// 提升之后：既有的未版本化工作流被路由到 Current 版本并被 Poller 消费。
	if err := waitForRun(ctx, beforeRun, 20*time.Second); err != nil {
		t.Fatalf("existing unversioned workflow was not picked up after promotion: %v", err)
	}

	// 提升之后新建的工作流同样正常。
	afterRun, err := temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "e2e-after-" + suffix, TaskQueue: taskQueue, WorkflowExecutionTimeout: 20 * time.Second,
	}, "e2e.probe.v1")
	if err != nil {
		t.Fatalf("start workflow after promotion: %v", err)
	}
	if err := waitForRun(ctx, afterRun, 20*time.Second); err != nil {
		t.Fatalf("new workflow did not complete after promotion: %v", err)
	}
}

func waitForRun(ctx context.Context, run client.WorkflowRun, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result string
	return run.Get(waitCtx, &result)
}
