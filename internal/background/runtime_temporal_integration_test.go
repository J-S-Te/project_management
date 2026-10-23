package background

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/platform"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/durationpb"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The business gate/publisher are explicit test callbacks, not signed license or
// ACK evidence. Temporal, schedules, PINNED execution, SQL lease reads and RPC are real.
func TestBackgroundRuntimeRealTemporalUpgradeAndRollback(t *testing.T) {
	address, dsnFile := os.Getenv("PM_BACKGROUND_TEMPORAL_TEST_ADDRESS"), os.Getenv("PM_SNAPSHOT_TEST_DSN_FILE")
	if address == "" || dsnFile == "" {
		t.Skip("explicit isolated Temporal address and private MySQL DSN file required")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("isolated loopback Temporal required")
	}
	info, err := os.Lstat(dsnFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private regular test DSN file required")
	}
	raw, err := os.ReadFile(dsnFile)
	if err != nil {
		t.Fatal("read isolated test DSN")
	}
	cfg, err := mysqlDriver.ParseDSN(strings.TrimSpace(string(raw)))
	if err != nil || !strings.HasSuffix(cfg.DBName, "_test") || cfg.Net != "tcp" || !strings.HasPrefix(cfg.Addr, "127.0.0.1:") {
		t.Fatal("isolated loopback _test database required")
	}
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated background database")
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ns := "uip-qa-pmbi-" + time.Now().UTC().Format("20060102t150405.000000000")
	nc, err := client.NewNamespaceClient(client.Options{HostPort: address, Logger: quietTemporalLogger{log}})
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	if err = nc.Register(ctx, &workflowservice.RegisterNamespaceRequest{Namespace: ns, WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	tc, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: ns, Logger: quietTemporalLogger{log}})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	var licensed, blockPublisher atomic.Bool
	var publishCalls atomic.Int64
	entered, release := make(chan struct{}, 1), make(chan struct{})
	licensed.Store(true)
	service := &application.Service{Repo: store.NewRepository(db), Notifications: activityPublisher(func(c context.Context, _ platform.NotificationEvent) error {
		publishCalls.Add(1)
		if blockPublisher.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-c.Done():
				return c.Err()
			}
		}
		return nil
	}), CheckBusinessLicense: func(context.Context) error {
		if !licensed.Load() {
			return errors.New("fixture gate expired")
		}
		return nil
	}}
	base, err := os.MkdirTemp("/tmp", "uip-qa-pmbi-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	options := validOptions()
	options.Namespace = ns
	options.TenantID = "uip-qa-tenant"
	options.Environment = "test"
	options.BuildID = "project-background-sha256-" + strings.Repeat("a", 64)
	options.SLAInterval = 4 * time.Second
	options.DispatchInterval = 2 * time.Second
	options.RunBudget = 2 * time.Second
	options.ActivityTimeout = 4 * time.Second
	options.SLAActivityTimeout = time.Second
	options.WorkflowRunTimeout = 10 * time.Second
	create := func(build, socket string) *Runtime {
		t.Helper()
		o := options
		o.BuildID = build
		if socket == "newer" {
			o.SLAInterval = 3 * time.Second
			o.DispatchInterval = 3 * time.Second
			o.MaxItems = 3
			o.RunBudget = 3 * time.Second
			o.ActivityTimeout = 5 * time.Second
		}
		o.ControlSocket = filepath.Join(base, socket, "control.sock")
		// Separate clients model distinct process identities while retaining the actual
		// production Worker factory and all versioning/poller checks unchanged.
		workerClient, e := client.DialContext(ctx, client.Options{HostPort: address, Namespace: ns, Identity: "uip-qa-" + socket, Logger: quietTemporalLogger{log}})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(workerClient.Close)
		deadline := time.Now().Add(15 * time.Second)
		for {
			r, e := NewRuntime(o, service, workerClient, log)
			if e != nil {
				t.Fatal(e)
			}
			if e = r.Start(ctx); e == nil {
				return r
			}
			// Fresh isolated namespace caches can lag registration. Bootstrap retries
			// remain bounded; control/promotion/verification are never retried by this helper.
			if time.Now().After(deadline) {
				t.Fatal(e)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	first := create(options.BuildID, "first")
	defer func() {
		if first != nil {
			_ = first.Stop(context.Background())
		}
	}()
	second := create(options.BuildID, "second")
	if err := second.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, kind := range taskKinds {
		d, e := tc.ScheduleClient().GetHandle(ctx, first.scheduleID(kind)).Describe(ctx)
		if e != nil || !d.Schedule.State.Paused || d.Schedule.Policy.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_SKIP {
			t.Fatalf("initial schedule is not paused/SKIP: %v", e)
		}
	}
	state, err := first.Control(ctx, ControlRequest{Action: "inspect"})
	if err != nil || !state.BootstrapReady || state.RoutingReady || state.ExecutionReady {
		t.Fatal("startup promoted or failed bootstrap")
	}
	control := func(r *Runtime, action, operation string) ControlState {
		t.Helper()
		s, e := SendControl(ctx, r.options.ControlSocket, ControlRequest{Action: action, OperationID: operation})
		if e != nil {
			t.Fatalf("control %s: %s / %v", action, s.PausedReason, e)
		}
		return s
	}
	// Persist the same non-secret canonical values captured by host prepare. The
	// rollback below destroys the old Runtime and loads these values afresh.
	configPath := filepath.Join(base, "original-tuning.json")
	originalState := control(first, "inspect", "initial")
	if originalState.RuntimeConfig == nil {
		t.Fatal("old process did not export canonical tuning")
	}
	configBytes, err := json.Marshal(originalState.RuntimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	waitRuns := func(r *Runtime, kinds []TaskKind) {
		t.Helper()
		previous := map[TaskKind]map[string]bool{}
		for _, kind := range kinds {
			h := tc.ScheduleClient().GetHandle(ctx, r.scheduleID(kind))
			d, e := h.Describe(ctx)
			if e != nil {
				t.Fatal(e)
			}
			previous[kind] = map[string]bool{}
			for _, a := range d.Info.RecentActions {
				if a.StartWorkflowResult != nil {
					previous[kind][a.StartWorkflowResult.FirstExecutionRunID] = true
				}
			}
			if e = h.Trigger(ctx, client.ScheduleTriggerOptions{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP}); e != nil {
				t.Fatal(e)
			}
		}
		deadline := time.Now().Add(25 * time.Second)
		for {
			completed := 0
			for _, kind := range kinds {
				d, e := tc.ScheduleClient().GetHandle(ctx, r.scheduleID(kind)).Describe(ctx)
				if e != nil {
					t.Fatal(e)
				}
				for _, a := range d.Info.RecentActions {
					if a.StartWorkflowResult == nil || previous[kind][a.StartWorkflowResult.FirstExecutionRunID] {
						continue
					}
					x, e := tc.DescribeWorkflowExecution(ctx, a.StartWorkflowResult.WorkflowID, a.StartWorkflowResult.FirstExecutionRunID)
					if e == nil && x.WorkflowExecutionInfo.Status == enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED {
						v := x.WorkflowExecutionInfo.GetVersioningInfo()
						if v.GetBehavior() != enumspb.VERSIONING_BEHAVIOR_PINNED || v.GetDeploymentVersion().GetBuildId() != r.options.BuildID {
							t.Fatal("completed run is not PINNED to expected immutable build")
						}
						completed++
						break
					}
				}
			}
			if completed == len(kinds) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("only %d/%d task kinds completed fresh real runs", completed, len(kinds))
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	// SDK Start launches pollers asynchronously. Wait for real matching evidence,
	// without retrying promotion or weakening production routing checks.
	deadline := time.Now().Add(20 * time.Second)
	for {
		ready := 0
		for _, kind := range []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY} {
			response, e := tc.WorkflowService().DescribeTaskQueue(ctx, &workflowservice.DescribeTaskQueueRequest{Namespace: ns, TaskQueue: &taskqueuepb.TaskQueue{Name: first.taskQueue}, TaskQueueType: kind, ReportPollers: true})
			if e != nil {
				t.Fatal(e)
			}
			for _, poller := range response.Pollers {
				if poller.GetDeploymentOptions().GetBuildId() == first.options.BuildID && poller.GetIdentity() == "uip-qa-first" {
					ready++
					break
				}
			}
		}
		if ready == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d/2 first-process versioned pollers registered", ready)
		}
		time.Sleep(100 * time.Millisecond)
	}
	control(first, "ensure", "initial")
	control(first, "resume", "initial")
	control(first, "verify", "initial")
	waitRuns(first, taskKinds)
	licensed.Store(false)
	s, e := SendControl(ctx, first.options.ControlSocket, ControlRequest{Action: "verify", OperationID: "expired"})
	if e == nil || s.ExecutionReady || !s.BootstrapReady || s.PausedReason != "PAUSED_LICENSE" {
		t.Fatal("expired gate reported business readiness")
	}
	// A fresh real Workflow/Activity must reject expiry before claiming SQL intent.
	eventID := time.Now().UTC().Format("20060102150405.000000000")
	if _, err = store.NewRepository(db).EnqueueNotification(ctx, options.TenantID, domain.NotificationMessage{EventID: eventID, EventType: "QA", Scope: platform.NotificationScopeCrossSystem, Priority: "NORMAL", Title: "synthetic", Content: "synthetic", Recipients: []string{"qa-user"}, OccurredAt: time.Now().UTC(), IdempotencyKey: eventID}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DELETE FROM pm_notification_outbox WHERE tenant_id=? AND event_id=?", options.TenantID, eventID)
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "qa-expired-" + eventID, TaskQueue: first.taskQueue, WorkflowRunTimeout: 10 * time.Second}, WorkflowName, TickInput{TenantID: options.TenantID, Kind: TaskNotifications, Limit: 1, Budget: 2 * time.Second, ActivityTimeout: 4 * time.Second, SLAActivityTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var result TickResult
	if err = run.Get(ctx, &result); err != nil || result.PausedReason != "LICENSE" || result.Processed != 0 || publishCalls.Load() != 0 {
		t.Fatalf("expired real activity was not paused: %+v / %v", result, err)
	}
	var persisted struct {
		Status   string
		LockedBy string
	}
	if err = db.Table("pm_notification_outbox").Select("status,COALESCE(locked_by,'') AS locked_by").Where("tenant_id=? AND event_id=?", options.TenantID, eventID).Take(&persisted).Error; err != nil || persisted.Status != "PENDING" || persisted.LockedBy != "" {
		t.Fatal("expired workflow claimed pending intent")
	}
	// Hold an actual publisher Activity with a live SQL lease: drain must fail closed.
	blockPublisher.Store(true)
	licensed.Store(true)
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("real notification activity did not enter publisher")
	}
	control(first, "pause", "busy")
	short, shortCancel := context.WithTimeout(ctx, 150*time.Millisecond)
	busy, busyErr := first.Control(short, ControlRequest{Action: "drain", OperationID: "busy"})
	shortCancel()
	if busyErr == nil || busy.Drained {
		t.Fatal("drain accepted in-flight real activity")
	}
	var leases int64
	if leases, err = service.CountActiveBackgroundLeases(ctx, options.TenantID, time.Now().UTC()); err != nil || leases != 1 {
		t.Fatal("held activity lacks actual SQL lease")
	}
	close(release)
	blockPublisher.Store(false)
	control(first, "drain", "busy")
	control(first, "resume", "busy")
	if err = tc.ScheduleClient().GetHandle(ctx, first.scheduleID(TaskSLA)).Pause(ctx, client.SchedulePauseOptions{Note: "qa operator pause"}); err != nil {
		t.Fatal(err)
	}
	control(first, "pause", "upgrade")
	s = control(first, "drain", "upgrade")
	if !s.Drained || s.ActiveLeases != 0 || s.ActiveActivities != 0 {
		t.Fatal("missing real drain evidence")
	}
	newer := create("project-background-sha256-"+strings.Repeat("b", 64), "newer")
	defer newer.Stop(context.Background())
	control(newer, "inspect", "upgrade") // host activation reads before ensure
	control(newer, "ensure", "upgrade")
	for _, kind := range taskKinds {
		d, e := tc.ScheduleClient().GetHandle(ctx, newer.scheduleID(kind)).Describe(ctx)
		if e != nil || newer.validateSchedule(kind, d) != nil {
			t.Fatal("new config not applied", e)
		}
	}
	if _, e := first.Control(ctx, ControlRequest{Action: "ensure", OperationID: "upgrade"}); e == nil {
		t.Fatal("old runtime overwrote candidate config")
	}

	control(newer, "resume", "upgrade")
	s = control(newer, "verify", "upgrade")
	if s.PausedReason != "PAUSED_OPERATOR" || s.ExecutionReady {
		t.Fatal("operator pause lost after upgrade")
	}
	waitRuns(newer, []TaskKind{TaskEffects, TaskNotifications})
	control(newer, "pause", "rollback")
	control(newer, "drain", "rollback")
	if err = first.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	first = nil
	var restored RuntimeConfig
	configBytes, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(configBytes, &restored); err != nil {
		t.Fatal(err)
	}
	oldBuild := options.BuildID
	options = newer.options // a cold rollback must replace the candidate's tuning
	options.SLAInterval, options.DispatchInterval = restored.SLAInterval, restored.DispatchInterval
	options.MaxItems, options.RunBudget = restored.MaxItems, restored.RunBudget
	options.ActivityTimeout, options.SLAActivityTimeout = restored.ActivityTimeout, restored.SLAActivityTimeout
	options.WorkflowRunTimeout = restored.WorkflowRunTimeout
	options.BuildID = oldBuild
	first = create(oldBuild, "cold-rollback")
	control(first, "restore", "rollback")
	control(first, "resume", "rollback")
	s = control(first, "verify", "rollback")
	if s.BuildID != options.BuildID || s.PausedReason != "PAUSED_OPERATOR" {
		t.Fatal("rollback did not restore original build/pause")
	}
	waitRuns(first, []TaskKind{TaskEffects, TaskNotifications})
	t.Log("real schedules, two Runtime registration, explicit promotion, PINNED task runs, gate pause, drain, upgrade, operator pause and rollback verified; fixture business gate only")
}
