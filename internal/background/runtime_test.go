package background

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
)

type memorySchedules struct {
	client.ScheduleClient
	mu      sync.Mutex
	rows    map[string]*client.ScheduleDescription
	creates int
}

func (m *memorySchedules) Create(_ context.Context, o client.ScheduleOptions) (client.ScheduleHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows[o.ID] != nil {
		return nil, serviceerror.NewAlreadyExists("exists")
	}
	m.creates++
	m.rows[o.ID] = &client.ScheduleDescription{Schedule: client.Schedule{Action: o.Action, Spec: &o.Spec, Policy: &client.SchedulePolicies{Overlap: o.Overlap, CatchupWindow: o.CatchupWindow, PauseOnFailure: o.PauseOnFailure}, State: &client.ScheduleState{Paused: o.Paused, Note: o.Note}}}
	return &memoryHandle{m: m, id: o.ID}, nil
}
func (m *memorySchedules) GetHandle(_ context.Context, id string) client.ScheduleHandle {
	return &memoryHandle{m: m, id: id}
}

type memoryHandle struct {
	client.ScheduleHandle
	m  *memorySchedules
	id string
}

func (h *memoryHandle) Describe(context.Context) (*client.ScheduleDescription, error) {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	d := h.m.rows[h.id]
	if d == nil {
		return nil, serviceerror.NewNotFound("missing")
	}
	copy := *d
	state := *d.Schedule.State
	copy.Schedule.State = &state
	return &copy, nil
}
func (h *memoryHandle) Update(_ context.Context, o client.ScheduleUpdateOptions) error {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	u, e := o.DoUpdate(client.ScheduleUpdateInput{Description: *h.m.rows[h.id]})
	if e != nil {
		return e
	}
	if u != nil {
		h.m.rows[h.id].Schedule = *u.Schedule
	}
	return nil
}

type runtimeClient struct {
	client.Client
	schedules *memorySchedules
}

func (c *runtimeClient) ScheduleClient() client.ScheduleClient { return c.schedules }

type recordWorker struct {
	names    []string
	started  bool
	stopped  bool
	startErr error
}

func (w *recordWorker) RegisterWorkflowWithOptions(_ interface{}, o workflow.RegisterOptions) {
	w.names = append(w.names, o.Name)
}
func (w *recordWorker) RegisterActivityWithOptions(_ interface{}, o activity.RegisterOptions) {
	w.names = append(w.names, o.Name)
}
func (w *recordWorker) Start() error {
	if len(w.names) != 4 {
		return errors.New("registration incomplete")
	}
	w.started = true
	return w.startErr
}
func (w *recordWorker) Stop() { w.stopped = true }
func testRuntime(t *testing.T) (*Runtime, *memorySchedules, *recordWorker) {
	t.Helper()
	o := validOptions()
	root, e := os.MkdirTemp("/private/tmp", "pm-t3-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	o.ControlSocket = filepath.Join(root, "private", "control.sock")
	m := &memorySchedules{rows: map[string]*client.ScheduleDescription{}}
	r, e := NewRuntime(o, &application.Service{Repo: &leaseRepo{}, Notifications: activityPublisher(func(context.Context, platform.NotificationEvent) error { return nil }), CheckBusinessLicense: func(context.Context) error { return nil }}, &runtimeClient{schedules: m}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	w := &recordWorker{}
	r.newWorker = func() workerRunner { return w }
	r.checkRouting = func(context.Context) error { return errUnpromoted }
	return r, m, w
}
func TestRuntimeRegistersAllBusinessTypesBeforeHealthy(t *testing.T) {
	r, m, w := testRuntime(t)
	if e := r.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	defer r.Stop(context.Background())
	if !w.started || len(w.names) != 4 || m.creates != 3 {
		t.Fatalf("worker=%+v creates=%d", w, m.creates)
	}
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/health/bootstrap", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}
func TestBootstrapHealthyWhileUnpromotedButExecutionNotReady(t *testing.T) {
	r, _, _ := testRuntime(t)
	if e := r.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	defer r.Stop(context.Background())
	s, e := r.Control(context.Background(), ControlRequest{Action: "inspect"})
	if e != nil || !s.BootstrapReady || s.RoutingReady || s.ExecutionReady || s.PausedReason != "POLLING_UNPROMOTED" {
		t.Fatalf("%+v %v", s, e)
	}
}
func TestScheduleReconcilePreservesOperatorPause(t *testing.T) {
	r, m, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		d.Schedule.State.Note = "maintenance"
		d.Schedule.State.Paused = true
	}
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		if !d.Schedule.State.Paused || d.Schedule.State.Note != "maintenance" {
			t.Fatal("operator pause overwritten")
		}
	}
}
func TestTwoRuntimesUseOneOwnedSchedule(t *testing.T) {
	r, m, _ := testRuntime(t)
	other, _, _ := testRuntime(t)
	other.schedules = m
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := other.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if m.creates != 3 {
		t.Fatal(m.creates)
	}
}
func TestScheduleDriftFailsClosed(t *testing.T) {
	r, m, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		d.Schedule.Action.(*client.ScheduleWorkflowAction).TaskQueue = "foreign"
		break
	}
	if e := r.reconcile(context.Background()); e == nil {
		t.Fatal("drift accepted")
	}
}
func TestControlPauseSurvivesRestartAndRestoresOperatorPause(t *testing.T) {
	r, m, _ := testRuntime(t)
	if e := r.reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		d.Schedule.State.Note = "operator maintenance"
		d.Schedule.State.Paused = true
	}
	if e := r.changePause(context.Background(), "deploy-123", true); e != nil {
		t.Fatal(e)
	}
	other, _, _ := testRuntime(t)
	other.schedules = m
	if e := other.changePause(context.Background(), "other", true); e == nil {
		t.Fatal("operation stolen")
	}
	if e := other.changePause(context.Background(), "deploy-123", true); e != nil {
		t.Fatal(e)
	}
	if e := other.changePause(context.Background(), "deploy-123", false); e != nil {
		t.Fatal(e)
	}
	for _, d := range m.rows {
		if !d.Schedule.State.Paused || d.Schedule.State.Note != "operator maintenance" {
			t.Fatal("operator state lost")
		}
	}
}
func TestControlRejectsForeignOrUnsafeSocket(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "private")
	if e := os.Mkdir(private, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(private, "control.sock")
	listener, err := listenControl(p)
	if err != nil {
		t.Fatalf("normal private socket must work before testing rejection: %v", err)
	}
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("normal socket did not receive mode0600")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if e := os.Symlink(filepath.Join(root, "other"), p); e != nil {
		t.Fatal(e)
	}
	if _, e := listenControl(p); e == nil {
		t.Fatal("symlink accepted")
	}
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(private, 0755); e != nil {
		t.Fatal(e)
	}
	if _, e := listenControl(p); e == nil {
		t.Fatal("public parent accepted")
	}
	if e := validateControlRequest(ControlRequest{Action: "exec", OperationID: "x"}); e == nil {
		t.Fatal("arbitrary action accepted")
	}
	if e := validateControlRequest(ControlRequest{Action: "pause", OperationID: "../x"}); e == nil {
		t.Fatal("unsafe operation ID accepted")
	}
}
