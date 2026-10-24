package background

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func tickEnvironment() *testsuite.TestWorkflowEnvironment {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(RunTick, workflow.RegisterOptions{Name: "project.background.tick.v1"})
	return env
}

// Removing the bounded loop or checking time only after dispatch must fail here.
func TestRunTickStopsAtLimitOrBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limit         int
		budget, delay time.Duration
		want          int
	}{
		{"explicit limit", 3, 4 * time.Minute, 0, 3},
		{"hard cap", 80, 4 * time.Minute, 0, 50},
		{"default limit", 0, 0, 0, 50},
		{"budget equality", 50, 40 * time.Second, 20 * time.Second, 2},
		{"budget exceeded", 50, 30 * time.Second, 20 * time.Second, 2},
		{"hard budget cap", 50, 10 * time.Minute, 40 * time.Second, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := tickEnvironment()
			calls := 0
			fn := func(context.Context, TickInput) (TickResult, error) { return TickResult{Processed: 1}, nil }
			env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: "project.background.effects.v1"})
			env.OnActivity("project.background.effects.v1", mock.Anything, mock.MatchedBy(func(in TickInput) bool {
				return in.Kind == TaskEffects && in.TenantID == "tenant-a" && in.Limit == 1
			})).Return(func(context.Context, TickInput) (TickResult, error) { calls++; return TickResult{Processed: 1}, nil }).After(tc.delay)
			env.ExecuteWorkflow("project.background.tick.v1", TickInput{TenantID: "tenant-a", Kind: TaskEffects, Limit: tc.limit, Budget: tc.budget})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var got TickResult
			if err := env.GetWorkflowResult(&got); err != nil {
				t.Fatal(err)
			}
			if calls != tc.want || got.Processed != tc.want {
				t.Fatalf("calls/result=%d/%+v want %d", calls, got, tc.want)
			}
		})
	}
}

func TestRunTickNoAutomaticActivityRetry(t *testing.T) {
	env := tickEnvironment()
	calls := 0
	env.RegisterActivityWithOptions(func(context.Context, TickInput) (TickResult, error) {
		calls++
		return TickResult{}, errors.New("repository unavailable")
	}, activity.RegisterOptions{Name: "project.background.notifications.v1"})
	env.ExecuteWorkflow(RunTick, TickInput{TenantID: "tenant-a", Kind: TaskNotifications})
	if env.GetWorkflowError() == nil || calls != 1 {
		t.Fatalf("error=%v attempts=%d", env.GetWorkflowError(), calls)
	}
}

func TestRunTickLicensePauseDoesNotBecomeBusinessFailure(t *testing.T) {
	env := tickEnvironment()
	calls := 0
	env.RegisterActivityWithOptions(func(context.Context, TickInput) (TickResult, error) {
		calls++
		return TickResult{PausedReason: "LICENSE"}, nil
	}, activity.RegisterOptions{Name: "project.background.effects.v1"})
	env.ExecuteWorkflow(RunTick, TickInput{TenantID: "tenant-a", Kind: TaskEffects})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var got TickResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Processed != 0 || got.PausedReason != "LICENSE" {
		t.Fatalf("calls/result=%d/%+v", calls, got)
	}
}

func TestRunTickSlaScansOnceAndEmptyOutboxStops(t *testing.T) {
	for _, kind := range []TaskKind{TaskSLA, TaskNotifications} {
		t.Run(string(kind), func(t *testing.T) {
			env := tickEnvironment()
			name := "project.background.sla.v1"
			processed := 57 // SLA is one scan; its result counts generated intents.
			if kind == TaskNotifications {
				name = "project.background.notifications.v1"
				processed = 0
			}
			calls := 0
			env.RegisterActivityWithOptions(func(context.Context, TickInput) (TickResult, error) {
				calls++
				return TickResult{Processed: processed}, nil
			}, activity.RegisterOptions{Name: name})
			env.ExecuteWorkflow(RunTick, TickInput{TenantID: "tenant-a", Kind: kind})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var got TickResult
			if err := env.GetWorkflowResult(&got); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.Processed != processed {
				t.Fatalf("calls/result=%d/%+v", calls, got)
			}
		})
	}
}

func TestRunTickRejectsInvalidInput(t *testing.T) {
	for _, in := range []TickInput{{Kind: TaskEffects}, {TenantID: "tenant-a", Kind: "unknown"}, {TenantID: "tenant-a", Kind: TaskEffects, Limit: -1}, {TenantID: "tenant-a", Kind: TaskEffects, Budget: -1}, {TenantID: "tenant-a", Kind: TaskEffects, ActivityTimeout: 56 * time.Second}, {TenantID: "tenant-a", Kind: TaskSLA, SLAActivityTimeout: -1}} {
		env := tickEnvironment()
		env.ExecuteWorkflow(RunTick, in)
		if env.GetWorkflowError() == nil {
			t.Fatalf("invalid input accepted: %+v", in)
		}
	}
}

func TestRunTickConfiguredActivityTimeout(t *testing.T) {
	for _, tc := range []struct {
		kind    TaskKind
		name    string
		timeout time.Duration
	}{
		{TaskEffects, "project.background.effects.v1", 3 * time.Second},
		{TaskSLA, "project.background.sla.v1", 5 * time.Second},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			env := tickEnvironment()
			var observed activity.Info
			fn := func(ctx context.Context, _ TickInput) (TickResult, error) {
				observed = activity.GetInfo(ctx)
				return TickResult{}, nil
			}
			env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: tc.name})
			env.ExecuteWorkflow(RunTick, TickInput{TenantID: "tenant-a", Kind: tc.kind, ActivityTimeout: 3 * time.Second, SLAActivityTimeout: 5 * time.Second})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			if observed.StartToCloseTimeout != tc.timeout || observed.ScheduleToCloseTimeout != tc.timeout || observed.Attempt != 1 {
				t.Fatalf("incorrect Activity boundary: %+v", observed)
			}
		})
	}
}
