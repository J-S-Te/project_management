package background

import (
	"context"
	"errors"
	"github.com/j-s-te/project-management/internal/platform"
	"testing"
	"time"

	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type deploymentHandle struct {
	client.WorkerDeploymentHandle
	current        *worker.WorkerDeploymentVersion
	promotions     int
	promoteOptions client.WorkerDeploymentSetCurrentVersionOptions
	name           string
}

func (h *deploymentHandle) Describe(context.Context, client.WorkerDeploymentDescribeOptions) (client.WorkerDeploymentDescribeResponse, error) {
	d := client.WorkerDeploymentDescribeResponse{ConflictToken: []byte("token")}
	d.Info.RoutingConfig.CurrentVersion = h.current
	return d, nil
}
func (h *deploymentHandle) SetCurrentVersion(_ context.Context, q client.WorkerDeploymentSetCurrentVersionOptions) (client.WorkerDeploymentSetCurrentVersionResponse, error) {
	h.promotions++
	h.promoteOptions = q
	h.current = &worker.WorkerDeploymentVersion{DeploymentName: h.name, BuildID: q.BuildID}
	return client.WorkerDeploymentSetCurrentVersionResponse{}, nil
}

type deploymentClient struct {
	client.WorkerDeploymentClient
	handle *deploymentHandle
}

func (c *deploymentClient) GetHandle(string) client.WorkerDeploymentHandle { return c.handle }

type routedClient struct {
	*evidenceClient
	deployment *deploymentClient
}

func (c *routedClient) WorkerDeploymentClient() client.WorkerDeploymentClient { return c.deployment }
func routingRuntime(t *testing.T) (*Runtime, *deploymentHandle, *rawService) {
	r, m, _ := testRuntime(t)
	h := &deploymentHandle{name: r.deployment, current: &worker.WorkerDeploymentVersion{DeploymentName: r.deployment, BuildID: r.options.BuildID}}
	raw := &rawService{pollers: func(q *workflowservice.DescribeTaskQueueRequest) (*workflowservice.DescribeTaskQueueResponse, error) {
		if q.Namespace != r.options.Namespace || q.TaskQueue.Name != r.taskQueue {
			t.Fatal("foreign queue", q)
		}
		return &workflowservice.DescribeTaskQueueResponse{Pollers: []*taskqueuepb.PollerInfo{{LastAccessTime: timestamppb.Now(), DeploymentOptions: &deploymentpb.WorkerDeploymentOptions{DeploymentName: r.deployment, BuildId: r.options.BuildID, WorkerVersioningMode: enumspb.WORKER_VERSIONING_MODE_VERSIONED}}}}, nil
	}}
	r.temporal = &routedClient{evidenceClient: &evidenceClient{runtimeClient: &runtimeClient{schedules: m}, raw: raw}, deployment: &deploymentClient{handle: h}}
	r.checkRouting = r.routingEvidence
	return r, h, raw
}
func TestRoutingRequiresCurrentAndBothLiveVersionedPollers(t *testing.T) {
	for _, name := range []string{"ready", "missing current", "foreign deployment", "foreign poller", "stale poller", "missing activity poller", "dependency"} {
		t.Run(name, func(t *testing.T) {
			r, h, raw := routingRuntime(t)
			base := raw.pollers
			switch name {
			case "missing current":
				h.current = nil
			case "foreign deployment":
				h.current.DeploymentName = "foreign"
			case "foreign poller", "stale poller", "missing activity poller", "dependency":
				raw.pollers = func(q *workflowservice.DescribeTaskQueueRequest) (*workflowservice.DescribeTaskQueueResponse, error) {
					if name == "dependency" {
						return nil, errors.New("private endpoint error")
					}
					d, e := base(q)
					if name == "foreign poller" {
						d.Pollers[0].DeploymentOptions.BuildId = "foreign"
					}
					if name == "stale poller" {
						d.Pollers[0].LastAccessTime = timestamppb.New(time.Now().Add(-3 * time.Minute))
					}
					if name == "missing activity poller" && q.TaskQueueType == enumspb.TASK_QUEUE_TYPE_ACTIVITY {
						d.Pollers = nil
					}
					return d, e
				}
			}
			e := r.routingEvidence(context.Background())
			if (name == "ready") != (e == nil) {
				t.Fatalf("routing %v", e)
			}
		})
	}
}
func TestEnsureCannotPromoteAcrossAnotherPendingOperation(t *testing.T) {
	r, h, _ := routingRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := r.changePause(context.Background(), "owner", true); e != nil {
		t.Fatal(e)
	}
	h.current = nil
	if e := r.ensure(context.Background(), "other"); e == nil {
		t.Fatal("foreign operation promoted")
	}
	if h.promotions != 0 {
		t.Fatal("foreign promotion side effect", h.promotions)
	}
}

func TestExecutionReadinessFailsWhenLeaseInspectionOrPublisherUnavailable(t *testing.T) {
	for _, name := range []string{"lease", "publisher"} {
		t.Run(name, func(t *testing.T) {
			r, m, _ := testRuntime(t)
			if e := r.reconcile(context.Background()); e != nil {
				t.Fatal(e)
			}
			r.bootstrap = true
			r.checkRouting = func(context.Context) error { return nil }
			for _, d := range m.rows {
				d.Schedule.State.Paused = false
				d.Schedule.State.Note = ""
			}
			r.service.Repo = &leaseRepo{}
			r.service.Notifications = activityPublisher(func(context.Context, platform.NotificationEvent) error { return nil })
			if name == "lease" {
				r.service.Repo = nil
			} else {
				r.service.Notifications = nil
			}
			s, _ := r.inspect(context.Background())
			if s.ExecutionReady || s.PausedReason != "DEPENDENCY_UNAVAILABLE" {
				t.Fatalf("%+v", s)
			}
		})
	}
}

func TestOnlyExplicitEnsurePromotesAndLeavesSchedulesPaused(t *testing.T) {
	r, h, _ := routingRuntime(t)
	h.current = nil
	if e := r.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	defer r.Stop(context.Background())
	if h.promotions != 0 {
		t.Fatal("startup promoted", h.promotions)
	}
	s, e := r.Control(context.Background(), ControlRequest{Action: "ensure", OperationID: "install"})
	if e != nil || !s.RoutingReady || s.ExecutionReady || h.promotions != 1 || h.promoteOptions.AllowNoPollers || h.promoteOptions.IgnoreMissingTaskQueues {
		t.Fatalf("%+v promotion=%+v err=%v", s, h.promoteOptions, e)
	}
	for _, v := range s.Schedules {
		if !v.Paused {
			t.Fatal("ensure implicitly activated schedule")
		}
	}
}

func TestLocalClaimGateIsNeverReportedAsExecutionReady(t *testing.T) {
	r, m, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		d.Schedule.State.Paused = false
		d.Schedule.State.Note = ""
	}
	r.bootstrap = true
	r.blocked = true
	r.checkRouting = func(context.Context) error { return nil }
	s, _ := r.inspect(context.Background())
	if s.ExecutionReady || s.PausedReason != "PAUSED_OPERATION" {
		t.Fatalf("local gate invisible %+v", s)
	}
}
