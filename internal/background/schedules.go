package background

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

const bootstrapNote = "project-background/bootstrap/v1"
const controlPrefix = "project-background/control/v1:"
const historyKey = "project_background_config_history_v1"
const ownerKey = "project_background_owner_v1"

var taskKinds = []TaskKind{TaskSLA, TaskEffects, TaskNotifications}

// The hash binds the entire identity tuple without truncating/escaping tenant input
// into a queue name. Build is deliberately excluded so an upgrade owns the same schedules.
func boundIdentity(environment, tenant string) string {
	b, _ := json.Marshal([]string{"project-background-v1", environment, tenant})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (r *Runtime) scheduleID(kind TaskKind) string {
	return "project-background-" + boundIdentity(r.options.Environment, r.options.TenantID) + "-" + string(kind)
}
func (r *Runtime) desired(kind TaskKind) client.ScheduleOptions {
	interval := r.options.DispatchInterval
	if kind == TaskSLA {
		interval = r.options.SLAInterval
	}
	id := r.scheduleID(kind)
	return client.ScheduleOptions{ID: id, Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: interval}}}, Action: &client.ScheduleWorkflowAction{ID: id, Workflow: WorkflowName, TaskQueue: r.taskQueue, Args: []interface{}{TickInput{TenantID: r.options.TenantID, Kind: kind, Limit: r.options.MaxItems, Budget: r.options.RunBudget, ActivityTimeout: r.options.ActivityTimeout, SLAActivityTimeout: r.options.SLAActivityTimeout}}, WorkflowTaskTimeout: 10 * time.Second, WorkflowRunTimeout: r.options.WorkflowRunTimeout, WorkflowExecutionTimeout: r.options.WorkflowRunTimeout, Memo: map[string]interface{}{ownerKey: boundIdentity(r.options.Environment, r.options.TenantID)}}, Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: time.Minute, Paused: true, Note: bootstrapNote}
}
func decodeValue(value interface{}, out interface{}) error {
	if payload, ok := value.(*commonpb.Payload); ok {
		return converter.GetDefaultDataConverter().FromPayload(payload, out)
	}
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func (r *Runtime) validateSchedule(kind TaskKind, d *client.ScheduleDescription) error {
	if d == nil || d.Schedule.Spec == nil || d.Schedule.Policy == nil || d.Schedule.State == nil {
		return errors.New("schedule evidence incomplete")
	}
	want := r.desired(kind)
	a, ok := d.Schedule.Action.(*client.ScheduleWorkflowAction)
	w := want.Action.(*client.ScheduleWorkflowAction)
	if !ok || a == nil || a.ID != w.ID || a.Workflow != WorkflowName || a.TaskQueue != w.TaskQueue || a.WorkflowRunTimeout != w.WorkflowRunTimeout || a.WorkflowExecutionTimeout != w.WorkflowExecutionTimeout || a.WorkflowTaskTimeout != w.WorkflowTaskTimeout || a.VersioningOverride != nil || a.RetryPolicy != nil || len(a.Args) != 1 {
		return errors.New("schedule action drift")
	}
	var input TickInput
	var owner string
	if decodeValue(a.Args[0], &input) != nil || !reflect.DeepEqual(input, w.Args[0]) || decodeValue(a.Memo[ownerKey], &owner) != nil || owner != boundIdentity(r.options.Environment, r.options.TenantID) {
		return errors.New("schedule ownership or input drift")
	}
	s := d.Schedule.Spec
	p := d.Schedule.Policy
	if len(s.Intervals) != 1 || s.Intervals[0] != want.Spec.Intervals[0] || len(s.Calendars) != 0 || len(s.CronExpressions) != 0 || len(s.Skip) != 0 || !s.StartAt.IsZero() || !s.EndAt.IsZero() || s.Jitter != 0 || (s.TimeZoneName != "" && s.TimeZoneName != "UTC") || p.Overlap != want.Overlap || p.CatchupWindow != time.Minute || p.PauseOnFailure || d.Schedule.State.LimitedActions || d.Schedule.State.RemainingActions != 0 {
		return errors.New("schedule specification drift")
	}
	_, e := readPause(d.Schedule.State)
	return e
}
func (r *Runtime) reconcile(ctx context.Context) error {
	for _, kind := range taskKinds {
		h := r.schedules.GetHandle(ctx, r.scheduleID(kind))
		d, e := h.Describe(ctx)
		var missing *serviceerror.NotFound
		if errors.As(e, &missing) {
			_, e = r.schedules.Create(ctx, r.desired(kind))
			var exists *serviceerror.AlreadyExists
			if e != nil && !errors.As(e, &exists) {
				return errors.New("schedule create unavailable")
			}
			d, e = h.Describe(ctx)
		}
		if e != nil {
			return errors.New("schedule describe unavailable")
		}
		if e = r.validateObservedSchedule(kind, d); e != nil {
			return e
		}
	}
	return nil
}

type pauseRecord struct {
	PreviousConfig *scheduleConfig `json:"previous_config,omitempty"`
	PreviousBuild  string          `json:"previous_build,omitempty"`
	OriginalConfig *scheduleConfig `json:"original_config,omitempty"`
	TargetConfig   *scheduleConfig `json:"target_config,omitempty"`
	OriginalBuild  string          `json:"original_build,omitempty"`
	TargetBuild    string          `json:"target_build,omitempty"`
	Restoring      bool            `json:"restoring,omitempty"`
	Protocol       int             `json:"protocol"`
	OperationID    string          `json:"operation_id"`
	OriginalPaused bool            `json:"original_paused"`
	OriginalNote   string          `json:"original_note"`
}

func readPause(s *client.ScheduleState) (*pauseRecord, error) {
	if s == nil {
		return nil, errors.New("schedule state incomplete")
	}
	if !strings.HasPrefix(s.Note, controlPrefix) {
		return nil, nil
	}
	var p pauseRecord
	if json.Unmarshal([]byte(strings.TrimPrefix(s.Note, controlPrefix)), &p) != nil || p.Protocol != 1 || !safeOperationID(p.OperationID) || !s.Paused || strings.HasPrefix(p.OriginalNote, controlPrefix) {
		return nil, errors.New("schedule control metadata invalid")
	}
	return &p, nil
}
func (r *Runtime) changePause(ctx context.Context, operation string, pause bool) error {
	// Validate all before changing any. Update validates again under SDK conflict retries.
	for _, kind := range taskKinds {
		d, e := r.schedules.GetHandle(ctx, r.scheduleID(kind)).Describe(ctx)
		if e != nil {
			return errors.New("schedule unavailable")
		}
		if e = r.validatePauseChange(kind, d, operation, pause); e != nil {
			return e
		}
		p, e := readPause(d.Schedule.State)
		if e != nil {
			return e
		}
		if p != nil && p.OperationID != operation {
			return errors.New("schedule controlled by another operation")
		}
	}
	for _, kind := range taskKinds {
		kind := kind
		e := r.schedules.GetHandle(ctx, r.scheduleID(kind)).Update(ctx, client.ScheduleUpdateOptions{DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			d := in.Description
			if e := r.validatePauseChange(kind, &d, operation, pause); e != nil {
				return nil, e
			}
			p, e := readPause(d.Schedule.State)
			if e != nil {
				return nil, e
			}
			if p != nil && p.OperationID != operation {
				return nil, errors.New("schedule controlled by another operation")
			}
			s := d.Schedule
			state := *s.State
			s.State = &state
			if pause {
				if p == nil {
					p = &pauseRecord{Protocol: 1, OperationID: operation, OriginalPaused: state.Paused, OriginalNote: state.Note, OriginalBuild: r.options.BuildID, OriginalConfig: r.desiredConfig(kind)}
					if a, ok := s.Action.(*client.ScheduleWorkflowAction); ok {
						var history pauseRecord
						if value, exists := a.Memo[historyKey]; exists {
							if decodeValue(value, &history) != nil || history.Protocol != 1 || !sameConfig(history.TargetConfig, r.desiredConfig(kind)) || history.TargetBuild != r.options.BuildID {
								return nil, errors.New("schedule config history invalid")
							}
							p.PreviousConfig = history.OriginalConfig
							p.PreviousBuild = history.OriginalBuild
						}
					}
					b, _ := json.Marshal(p)
					state.Note = controlPrefix + string(b)
					state.Paused = true
				}
			} else if p != nil {
				state.Paused = p.OriginalPaused
				state.Note = p.OriginalNote
				if state.Note == bootstrapNote {
					state.Paused = false
					state.Note = ""
				}
			} else if state.Note == bootstrapNote {
				state.Paused = false
				state.Note = ""
			}
			return &client.ScheduleUpdate{Schedule: &s}, nil
		}})
		if e != nil {
			return fmt.Errorf("schedule control blocked: %w", e)
		}
	}
	return nil
}

func (r *Runtime) validatePauseChange(kind TaskKind, d *client.ScheduleDescription, operation string, pause bool) error {
	if pause {
		if e := r.validateObservedSchedule(kind, d); e != nil {
			return e
		}
		p, e := readPause(d.Schedule.State)
		if e != nil {
			return e
		}
		if p != nil {
			if p.OperationID != operation {
				return errors.New("foreign operation")
			}
			return nil
		}
	}
	if e := r.validateSchedule(kind, d); e != nil {
		return e
	}
	if pause {
		a := d.Schedule.Action.(*client.ScheduleWorkflowAction)
		if value, exists := a.Memo[historyKey]; exists {
			var history pauseRecord
			if decodeValue(value, &history) != nil || history.Protocol != 1 || !sameConfig(history.TargetConfig, r.desiredConfig(kind)) || history.TargetBuild != r.options.BuildID {
				return errors.New("schedule config history invalid")
			}
		}
	}
	p, e := readPause(d.Schedule.State)
	if e != nil {
		return e
	}
	if p != nil && !pause {
		if p.Restoring {
			if !(p.OriginalBuild == r.options.BuildID && sameConfig(p.OriginalConfig, r.desiredConfig(kind))) && !(p.PreviousBuild == r.options.BuildID && sameConfig(p.PreviousConfig, r.desiredConfig(kind))) {
				return errors.New("restore owner mismatch")
			}
		} else if p.TargetBuild != "" && (p.TargetBuild != r.options.BuildID || !sameConfig(p.TargetConfig, r.desiredConfig(kind))) {
			return errors.New("candidate owner mismatch")
		}
	}
	return nil
}
