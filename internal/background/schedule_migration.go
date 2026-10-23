package background

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.temporal.io/sdk/client"
)

// Only the supported tuning knobs are migratable. Structural identity and every
// other Schedule policy remain subject to the original strict validator.
type scheduleConfig struct {
	Interval   time.Duration `json:"interval"`
	Input      TickInput     `json:"input"`
	RunTimeout time.Duration `json:"run_timeout"`
}

func configOf(d *client.ScheduleDescription) (*scheduleConfig, error) {
	if d == nil || d.Schedule.Spec == nil || len(d.Schedule.Spec.Intervals) != 1 {
		return nil, errors.New("schedule config incomplete")
	}
	a, ok := d.Schedule.Action.(*client.ScheduleWorkflowAction)
	if !ok || a == nil || len(a.Args) != 1 {
		return nil, errors.New("schedule action incomplete")
	}
	c := &scheduleConfig{Interval: d.Schedule.Spec.Intervals[0].Every, RunTimeout: a.WorkflowRunTimeout}
	if e := decodeValue(a.Args[0], &c.Input); e != nil {
		return nil, e
	}
	return c, nil
}
func (r *Runtime) desiredConfig(kind TaskKind) *scheduleConfig {
	d := r.desired(kind)
	c, _ := configOf(&client.ScheduleDescription{Schedule: client.Schedule{Spec: &d.Spec, Action: d.Action}})
	return c
}
func sameConfig(a, b *scheduleConfig) bool { return a != nil && b != nil && *a == *b }
func (r *Runtime) validateConfig(kind TaskKind, d *client.ScheduleDescription, c *scheduleConfig) error {
	if c == nil {
		return errors.New("missing canonical schedule config")
	}
	o := r.options
	o.SLAInterval = c.Interval
	o.DispatchInterval = c.Interval
	o.MaxItems = c.Input.Limit
	o.RunBudget = c.Input.Budget
	o.ActivityTimeout = c.Input.ActivityTimeout
	o.SLAActivityTimeout = c.Input.SLAActivityTimeout
	o.WorkflowRunTimeout = c.RunTimeout
	if e := o.Validate(); e != nil {
		return errors.New("invalid canonical schedule config")
	}
	check := &Runtime{options: o, taskQueue: r.taskQueue}
	return check.validateSchedule(kind, d)
}

// Bootstrap and inspection can observe a controlled, paused prior specification.
// Activities and resume retain strict validation against this process's config.
func (r *Runtime) validateObservedSchedule(kind TaskKind, d *client.ScheduleDescription) error {
	if e := r.validateSchedule(kind, d); e == nil {
		return nil
	}
	if d == nil {
		return errors.New("schedule unavailable")
	}
	p, e := readPause(d.Schedule.State)
	if e != nil || p == nil || p.OriginalConfig == nil {
		return errors.New("uncontrolled schedule drift")
	}
	c, e := configOf(d)
	if e != nil {
		return e
	}
	if !sameConfig(c, p.OriginalConfig) && !sameConfig(c, p.TargetConfig) && !sameConfig(c, p.PreviousConfig) {
		return errors.New("schedule differs from controlled evidence")
	}
	return r.validateConfig(kind, d, c)
}
func (r *Runtime) migrationAllowed(kind TaskKind, d *client.ScheduleDescription, operation string, restore bool) error {
	if e := r.validateObservedSchedule(kind, d); e != nil {
		return e
	}
	p, e := readPause(d.Schedule.State)
	if e != nil {
		return e
	}
	if p == nil {
		if restore {
			a, ok := d.Schedule.Action.(*client.ScheduleWorkflowAction)
			var history pauseRecord
			if !ok || decodeValue(a.Memo[historyKey], &history) != nil || !history.Restoring || history.OperationID != operation || history.TargetBuild != r.options.BuildID || !sameConfig(history.TargetConfig, r.desiredConfig(kind)) {
				return errors.New("restore requires an owned paused operation")
			}
		}
		return r.validateSchedule(kind, d)
	}
	if p.OperationID != operation {
		return errors.New("schedule controlled by another operation")
	}
	if p.OriginalConfig == nil || p.OriginalBuild == "" {
		return errors.New("operation lacks canonical original configuration")
	}

	want := r.desiredConfig(kind)
	if restore {
		if !(p.OriginalBuild == r.options.BuildID && sameConfig(want, p.OriginalConfig)) && !(p.PreviousBuild == r.options.BuildID && sameConfig(want, p.PreviousConfig)) {
			return errors.New("restore does not match original build and config")
		}
	} else {
		if p.Restoring {
			return errors.New("operation is restoring")
		}
		if p.TargetBuild != "" && (p.TargetBuild != r.options.BuildID || !sameConfig(p.TargetConfig, want)) {
			return errors.New("operation already bound to another candidate")
		}
	}
	return nil
}
func (r *Runtime) migrateSchedules(ctx context.Context, operation string, restore bool) error {
	controlled := false
	// Preflight all schedules before any mutation or deployment promotion.
	for _, kind := range taskKinds {
		d, e := r.schedules.GetHandle(ctx, r.scheduleID(kind)).Describe(ctx)
		if e != nil {
			return e
		}
		if e = r.migrationAllowed(kind, d, operation, restore); e != nil {
			return e
		}
		p, _ := readPause(d.Schedule.State)
		controlled = controlled || p != nil
	}
	if !controlled {
		return nil
	}
	// Every member must be paused under this operation, including partial retries.
	for _, kind := range taskKinds {
		d, e := r.schedules.GetHandle(ctx, r.scheduleID(kind)).Describe(ctx)
		if e != nil {
			return e
		}
		p, e := readPause(d.Schedule.State)
		if e != nil || p == nil || p.OperationID != operation {
			return errors.New("operation pause set incomplete")
		}
	}
	if e := r.drain(ctx); e != nil {
		return e
	}
	for _, kind := range taskKinds {
		kind := kind
		e := r.schedules.GetHandle(ctx, r.scheduleID(kind)).Update(ctx, client.ScheduleUpdateOptions{DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			d := in.Description
			if e := r.migrationAllowed(kind, &d, operation, restore); e != nil {
				return nil, e
			}
			p, e := readPause(d.Schedule.State)
			if e != nil || p == nil {
				return nil, errors.New("operation pause lost")
			}
			if restore {
				p.Restoring = true
			} else {
				p.TargetBuild = r.options.BuildID
				p.TargetConfig = r.desiredConfig(kind)
			}
			want := r.desired(kind)
			s := d.Schedule
			s.Spec = &want.Spec
			s.Action = want.Action
			history := *p
			history.TargetBuild = r.options.BuildID
			history.TargetConfig = r.desiredConfig(kind)
			if restore {
				history.OriginalBuild = p.OriginalBuild
				history.OriginalConfig = p.OriginalConfig
				if p.TargetConfig != nil {
					history.OriginalBuild = p.TargetBuild
					history.OriginalConfig = p.TargetConfig
				}
			}
			s.Action.(*client.ScheduleWorkflowAction).Memo[historyKey] = history
			state := *s.State
			s.State = &state
			b, _ := json.Marshal(p)
			state.Note = controlPrefix + string(b)
			return &client.ScheduleUpdate{Schedule: &s}, nil
		}})
		if e != nil {
			return e
		}
	}
	return nil
}
