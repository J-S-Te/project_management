package background

import (
	"context"
	"encoding/json"
	"errors"
	"go.temporal.io/sdk/client"
	"testing"
	"time"
)

func TestControlledScheduleConfigCandidateBootstrap(t *testing.T) {
	old, m, _ := testRuntime(t)
	ctx := context.Background()
	if e := old.reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if e := old.changePause(ctx, "upgrade", true); e != nil {
		t.Fatal(e)
	}
	candidate, _, _ := testRuntime(t)
	candidate.schedules = m
	candidate.options.SLAInterval += time.Minute
	candidate.options.MaxItems--
	if e := candidate.reconcile(ctx); e != nil {
		t.Fatalf("controlled candidate cannot bootstrap: %v", e)
	}
	if _, e := candidate.inspect(ctx); e != nil {
		t.Fatalf("host pre-ensure inspect blocked: %v", e)
	}
	if e := candidate.validateSchedule(TaskSLA, m.rows[old.scheduleID(TaskSLA)]); e == nil {
		t.Fatal("business accepted old config")
	}
}

func TestControlledScheduleMigrationAndRestore(t *testing.T) {
	ctx := context.Background()
	old, _, _ := drainRuntime(t)
	m := old.schedules.(*memorySchedules)
	// Preserve a genuine pre-operation manual pause for rollback as well.
	p, _ := readPause(m.rows[old.scheduleID(TaskSLA)].Schedule.State)
	p.OriginalPaused = true
	p.OriginalNote = "manual maintenance"
	b, _ := json.Marshal(p)
	m.rows[old.scheduleID(TaskSLA)].Schedule.State.Note = controlPrefix + string(b)
	next, _, _ := testRuntime(t)
	next.schedules = m
	next.temporal = old.temporal
	next.options.BuildID = "candidate-build"
	next.options.SLAInterval += time.Minute
	next.options.MaxItems--
	if e := next.migrateSchedules(ctx, "foreign", false); e == nil {
		t.Fatal("foreign operation migrated")
	}
	if e := next.migrateSchedules(ctx, "deploy", false); e != nil {
		t.Fatal(e)
	}
	for _, kind := range taskKinds {
		d := m.rows[old.scheduleID(kind)]
		if e := next.validateSchedule(kind, d); e != nil {
			t.Fatal(e)
		}
		p, e := readPause(d.Schedule.State)
		if e != nil || p.TargetBuild != next.options.BuildID || p.OriginalConfig == nil {
			t.Fatal("lost migration evidence")
		}
	}
	if e := old.migrateSchedules(ctx, "deploy", false); e == nil {
		t.Fatal("old process overwrote candidate")
	}
	if e := next.migrateSchedules(ctx, "deploy", false); e != nil {
		t.Fatal("retry", e)
	}
	if e := old.migrateSchedules(ctx, "deploy", true); e != nil {
		t.Fatal("restore", e)
	}
	if e := next.migrateSchedules(ctx, "deploy", false); e == nil {
		t.Fatal("candidate overwrote restoring operation")
	}
	if e := old.changePause(ctx, "deploy", false); e != nil {
		t.Fatal(e)
	}
	if e := old.migrateSchedules(ctx, "deploy", true); e != nil {
		t.Fatal("restore verify retry", e)
	}
	d := m.rows[old.scheduleID(TaskSLA)]
	if !d.Schedule.State.Paused || d.Schedule.State.Note != "manual maintenance" {
		t.Fatal("manual pause lost")
	}
}

func TestControlledMigrationAfterSuccessRetainsRollbackSpec(t *testing.T) {
	ctx := context.Background()
	old, _, _ := drainRuntime(t)
	m := old.schedules.(*memorySchedules)
	next, _, _ := testRuntime(t)
	next.schedules = m
	next.temporal = old.temporal
	next.options.BuildID = "candidate-build"
	next.options.SLAInterval += time.Minute
	next.options.MaxItems--
	if e := next.migrateSchedules(ctx, "deploy", false); e != nil {
		t.Fatal(e)
	}
	if e := next.changePause(ctx, "deploy", false); e != nil {
		t.Fatal(e)
	}
	if e := next.changePause(ctx, "rollback", true); e != nil {
		t.Fatal(e)
	}
	if e := old.migrateSchedules(ctx, "rollback", true); e != nil {
		t.Fatal(e)
	}
	if e := old.changePause(ctx, "rollback", false); e != nil {
		t.Fatal(e)
	}
	for _, kind := range taskKinds {
		if e := old.validateSchedule(kind, m.rows[old.scheduleID(kind)]); e != nil {
			t.Fatal(e)
		}
	}
}

type failingScheduleClient struct {
	client.ScheduleClient
	failID string
	fail   bool
}

func (f *failingScheduleClient) GetHandle(ctx context.Context, id string) client.ScheduleHandle {
	return &failingScheduleHandle{ScheduleHandle: f.ScheduleClient.GetHandle(ctx, id), owner: f, id: id}
}

type failingScheduleHandle struct {
	client.ScheduleHandle
	owner *failingScheduleClient
	id    string
}

func (f *failingScheduleHandle) Update(ctx context.Context, o client.ScheduleUpdateOptions) error {
	if f.owner.fail && f.id == f.owner.failID {
		return errors.New("update unavailable")
	}
	// Exercise SDK conflict retry behavior: callbacks may run more than once.
	d, e := f.Describe(ctx)
	if e != nil {
		return e
	}
	if _, e = o.DoUpdate(client.ScheduleUpdateInput{Description: *d}); e != nil {
		return e
	}
	return f.ScheduleHandle.Update(ctx, o)
}
func TestControlledMigrationPartialUpdateRetryAndIdentity(t *testing.T) {
	ctx := context.Background()
	old, _, _ := drainRuntime(t)
	m := old.schedules.(*memorySchedules)
	next, _, _ := testRuntime(t)
	next.temporal = old.temporal
	next.options.BuildID = "candidate-build"
	next.options.SLAInterval += time.Minute
	f := &failingScheduleClient{ScheduleClient: m, failID: old.scheduleID(TaskEffects), fail: true}
	next.schedules = f
	if e := next.migrateSchedules(ctx, "deploy", false); e == nil {
		t.Fatal("failed update hidden")
	}
	for _, d := range m.rows {
		if !d.Schedule.State.Paused {
			t.Fatal("partial update activated")
		}
	}
	if e := old.migrateSchedules(ctx, "deploy", false); e == nil {
		t.Fatal("old process overwrote partial update")
	}
	f.fail = false
	if e := next.changePause(ctx, "deploy", true); e != nil {
		t.Fatal("rollback cannot pause partially migrated set", e)
	}
	f.fail = false
	if e := next.migrateSchedules(ctx, "deploy", false); e != nil {
		t.Fatal(e)
	}
	a := m.rows[old.scheduleID(TaskNotifications)].Schedule.Action.(*client.ScheduleWorkflowAction)
	a.TaskQueue = "foreign-queue"
	if e := old.migrateSchedules(ctx, "deploy", true); e == nil {
		t.Fatal("foreign queue restored")
	}
	if e := old.reconcile(ctx); e == nil {
		t.Fatal("foreign queue bootstrapped")
	}
}

func TestScheduleConfigDriftRequiresOwnedPauseAndDrain(t *testing.T) {
	ctx := context.Background()
	old, m, _ := testRuntime(t)
	if e := old.reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	next, _, _ := testRuntime(t)
	next.schedules = m
	next.options.SLAInterval += time.Minute
	if e := next.reconcile(ctx); e == nil {
		t.Fatal("uncontrolled config drift bootstrapped")
	}
	if e := next.migrateSchedules(ctx, "deploy", false); e == nil {
		t.Fatal("unpaused migration")
	}
	old, _, leases := drainRuntime(t)
	m = old.schedules.(*memorySchedules)
	next.schedules = m
	next.temporal = old.temporal
	next.service = old.service
	leases.n = 1
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if e := next.migrateSchedules(short, "deploy", false); e == nil {
		t.Fatal("migration with live lease")
	}
	for _, kind := range taskKinds {
		p, _ := readPause(m.rows[old.scheduleID(kind)].Schedule.State)
		if p.TargetConfig != nil {
			t.Fatal("mutated before drain")
		}
	}
}
