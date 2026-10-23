package background

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	schedulepb "go.temporal.io/api/schedule/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/grpc"
)

type leaseRepo struct {
	application.Repository
	n     int64
	err   error
	calls int
}

func (l *leaseRepo) CountActiveBackgroundLeases(context.Context, string, time.Time) (int64, error) {
	l.calls++
	return l.n, l.err
}

type rawService struct {
	workflowservice.WorkflowServiceClient
	snapshot func(*workflowservice.DescribeScheduleRequest) (*workflowservice.DescribeScheduleResponse, error)
	pollers  func(*workflowservice.DescribeTaskQueueRequest) (*workflowservice.DescribeTaskQueueResponse, error)
}

func (s *rawService) DescribeSchedule(_ context.Context, q *workflowservice.DescribeScheduleRequest, _ ...grpc.CallOption) (*workflowservice.DescribeScheduleResponse, error) {
	return s.snapshot(q)
}
func (s *rawService) DescribeTaskQueue(_ context.Context, q *workflowservice.DescribeTaskQueueRequest, _ ...grpc.CallOption) (*workflowservice.DescribeTaskQueueResponse, error) {
	return s.pollers(q)
}

type evidenceClient struct {
	*runtimeClient
	raw       *rawService
	execution func(string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	listing   func(*workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error)
}

func (c *evidenceClient) ListWorkflow(_ context.Context, q *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	if c.listing != nil {
		return c.listing(q)
	}
	return &workflowservice.ListWorkflowExecutionsResponse{}, nil
}

func (c *evidenceClient) WorkflowService() workflowservice.WorkflowServiceClient { return c.raw }
func (c *evidenceClient) DescribeWorkflowExecution(_ context.Context, id, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return c.execution(id, run)
}
func drainRuntime(t *testing.T) (*Runtime, *rawService, *leaseRepo) {
	r, m, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := r.changePause(context.Background(), "deploy", true); e != nil {
		t.Fatal(e)
	}
	raw := &rawService{snapshot: func(q *workflowservice.DescribeScheduleRequest) (*workflowservice.DescribeScheduleResponse, error) {
		if q.Namespace != "default" {
			t.Fatal(q.Namespace)
		}
		return &workflowservice.DescribeScheduleResponse{Info: &schedulepb.ScheduleInfo{}, Schedule: &schedulepb.Schedule{State: &schedulepb.ScheduleState{Paused: true}}}, nil
	}}
	r.temporal = &evidenceClient{runtimeClient: &runtimeClient{schedules: m}, raw: raw}
	repo := &leaseRepo{}
	r.service.Repo = repo
	return r, raw, repo
}
func TestDrainRequiresAuthoritativeScheduleAndTenantLeaseEvidence(t *testing.T) {
	for _, name := range []string{"settled", "missing info", "buffered action", "unpaused", "missing lease capability", "lease failure", "live lease", "active activity", "running execution", "unavailable execution", "foreign execution", "changed action count"} {
		t.Run(name, func(t *testing.T) {
			r, raw, repo := drainRuntime(t)
			base := raw.snapshot
			id := r.scheduleID(TaskSLA)
			switch name {
			case "missing info":
				raw.snapshot = func(*workflowservice.DescribeScheduleRequest) (*workflowservice.DescribeScheduleResponse, error) {
					return &workflowservice.DescribeScheduleResponse{}, nil
				}
			case "buffered action":
				raw.snapshot = func(q *workflowservice.DescribeScheduleRequest) (*workflowservice.DescribeScheduleResponse, error) {
					d, e := base(q)
					d.Info.BufferSize = 1
					return d, e
				}
			case "unpaused":
				raw.snapshot = func(q *workflowservice.DescribeScheduleRequest) (*workflowservice.DescribeScheduleResponse, error) {
					d, e := base(q)
					d.Schedule.State.Paused = false
					return d, e
				}
			case "missing lease capability":
				r.service.Repo = nil
			case "lease failure":
				repo.err = errors.New("db unavailable")
			case "live lease":
				repo.n = 1
			case "active activity":
				r.active = 1
			case "running execution", "unavailable execution", "foreign execution":
				raw.snapshot = func(q *workflowservice.DescribeScheduleRequest) (*workflowservice.DescribeScheduleResponse, error) {
					d, e := base(q)
					d.Info.ActionCount = 1
					d.Info.RecentActions = []*schedulepb.ScheduleActionResult{{StartWorkflowResult: &commonpb.WorkflowExecution{WorkflowId: id + "-tick", RunId: "run"}}}
					return d, e
				}
				r.temporal.(*evidenceClient).execution = func(wid, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
					if name == "unavailable execution" {
						return nil, errors.New("not found")
					}
					payload, _ := converter.GetDefaultDataConverter().ToPayload(boundIdentity(r.options.Environment, r.options.TenantID))
					queue := r.taskQueue
					if name == "foreign execution" {
						queue = "foreign"
					}
					return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Type: &commonpb.WorkflowType{Name: WorkflowName}, TaskQueue: queue, Memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{ownerKey: payload}}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING}}, nil
				}
			case "changed action count":
				n := int64(0)
				raw.snapshot = func(q *workflowservice.DescribeScheduleRequest) (*workflowservice.DescribeScheduleResponse, error) {
					d, e := base(q)
					n++
					d.Info.ActionCount = n
					return d, e
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			e := r.drain(ctx)
			if (name == "settled") != (e == nil) {
				t.Fatalf("drain err=%v", e)
			}
		})
	}
}
func TestControlVerifyDistinguishesLicenseAndOperatorPause(t *testing.T) {
	r, m, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	r.bootstrap = true
	r.checkRouting = func(context.Context) error { return nil }
	for _, d := range m.rows {
		d.Schedule.State.Note = "original maintenance"
	}
	s, e := r.Control(context.Background(), ControlRequest{Action: "verify", OperationID: "deploy"})
	if e != nil || s.ExecutionReady || s.PausedReason != "PAUSED_OPERATOR" {
		t.Fatalf("%+v %v", s, e)
	}
	r.service.CheckBusinessLicense = func(context.Context) error { return errors.New("private license detail") }
	s, e = r.Control(context.Background(), ControlRequest{Action: "verify", OperationID: "deploy"})
	if e == nil || s.PausedReason != "PAUSED_LICENSE" || s.ExecutionReady {
		t.Fatalf("%+v %v", s, e)
	}
}
func TestControlBootstrapActivationOnlyExplicitResume(t *testing.T) {
	r, m, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	r.checkRouting = func(context.Context) error { return nil }
	if e := r.changePause(context.Background(), "install", true); e != nil {
		t.Fatal(e)
	}
	other, _, _ := testRuntime(t)
	other.schedules = m
	if e := other.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		if !d.Schedule.State.Paused {
			t.Fatal("startup activated schedule")
		}
	}
	if e := other.changePause(context.Background(), "install", false); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		if d.Schedule.State.Paused {
			t.Fatal("bootstrap not activated")
		}
	}
}
func TestSocketControlRoundTripAndReadOnlyHealth(t *testing.T) {
	r, _, _ := testRuntime(t)
	if e := r.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	defer r.Stop(context.Background())
	s, e := SendControl(context.Background(), r.options.ControlSocket, ControlRequest{Action: "inspect"})
	if e != nil || s.Protocol != 1 || len(s.Schedules) != 3 {
		t.Fatalf("%+v %v", s, e)
	}
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/control", strings.NewReader(`{"action":"pause"}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatal(rec.Code)
	}
	for _, body := range []string{`{"action":"inspect","tenant":"foreign"}`, `{"action":"inspect"} {}`, `{"action":"pause","operation_id":"../"}`} {
		rec = httptest.NewRecorder()
		r.controlHandler().ServeHTTP(rec, httptest.NewRequest("POST", "/control", strings.NewReader(body)))
		if rec.Code == 200 {
			t.Fatal("unsafe request accepted", body)
		}
	}
}
func TestPausedRuntimeDoesNotClaimOrConsumeLicenseAttempt(t *testing.T) {
	r, _, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	calls := 0
	r.service.CheckBusinessLicense = func(context.Context) error { calls++; return nil }
	v, e := r.runOne(context.Background(), TickInput{TenantID: r.options.TenantID, Kind: TaskEffects})
	if e != nil || v.PausedReason != "OPERATOR" || calls != 0 {
		t.Fatalf("%+v %v calls=%d", v, e, calls)
	}
}

func TestPauseAllowsAlreadyAdmittedActivityToFinishAndAcknowledge(t *testing.T) {
	r, m, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		d.Schedule.State.Paused = false
		d.Schedule.State.Note = ""
	}
	repo := &activityRepo{notification: &domain.NotificationOutboxItem{ID: 7}}
	r.service = enabledService(repo)
	publishing, release := make(chan struct{}), make(chan struct{})
	r.service.Notifications = activityPublisher(func(context.Context, platform.NotificationEvent) error { close(publishing); <-release; return nil })
	done := make(chan error, 1)
	go func() {
		v, e := r.runOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: TaskNotifications})
		if e == nil && v.Processed != 1 {
			e = errors.New("not acknowledged")
		}
		done <- e
	}()
	<-publishing
	r.mu.Lock()
	r.blocked = true
	r.mu.Unlock()
	if e := r.changePause(context.Background(), "deploy", true); e != nil {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if repo.completed != 1 || repo.retried != 0 {
		t.Fatalf("completed=%d retry=%d", repo.completed, repo.retried)
	}
}

func TestUnavailableControlNeverReportsZeroActivitiesOrLeases(t *testing.T) {
	s, e := SendControl(context.Background(), "/private/tmp/project-background-absent/control.sock", ControlRequest{Action: "inspect"})
	if e == nil || s.ActiveActivities != -1 || s.ActiveLeases != -1 {
		t.Fatalf("unavailable endpoint invented counts %+v %v", s, e)
	}
}
