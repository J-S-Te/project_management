package background

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/temporalworker"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	schedulepb "go.temporal.io/api/schedule/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

var errUnpromoted = errors.New("background polling unpromoted")

func (r *Runtime) baseState() ControlState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := ControlState{Protocol: 1, Engine: r.options.Engine, TenantID: r.options.TenantID, Environment: r.options.Environment, BuildID: r.options.BuildID, DeploymentName: r.deployment, TaskQueue: r.taskQueue, BootstrapReady: r.bootstrap, ActiveActivities: r.active, ActiveLeases: -1, Schedules: []ScheduleState{}}
	if r.options.Engine == EngineTemporal {
		o := r.options
		s.RuntimeConfig = &RuntimeConfig{SLAInterval: o.SLAInterval, DispatchInterval: o.DispatchInterval, MaxItems: o.MaxItems, RunBudget: o.RunBudget, ActivityTimeout: o.ActivityTimeout, SLAActivityTimeout: o.SLAActivityTimeout, WorkflowRunTimeout: o.WorkflowRunTimeout}
	}
	return s
}

// Backlog aggregation and direct execution observations run only on an explicit
// inspect request. Polling health and each Activity never scan the backlog.
func (r *Runtime) observe(ctx context.Context, s *ControlState) {
	if stats, e := r.service.ReadBackgroundQueueStats(ctx, r.options.TenantID, time.Now().UTC()); e == nil {
		s.QueueStatsAvailable = true
		s.QueueStats = stats
	}
	if r.options.Engine != EngineTemporal {
		return
	}
	for i := range s.Schedules {
		v := &s.Schedules[i]
		d, e := r.schedules.GetHandle(ctx, v.ID).Describe(ctx)
		if e != nil || r.validateSchedule(v.Kind, d) != nil {
			continue
		}
		v.ExecutionHistoryAvailable = true
		for j := len(d.Info.RecentActions) - 1; j >= 0; j-- {
			a := d.Info.RecentActions[j]
			if a.ActualTime.IsZero() || a.StartWorkflowResult == nil {
				v.ExecutionHistoryAvailable = false
				continue
			}
			at := a.ActualTime.UTC()
			if v.LastExecutionAt == nil {
				v.LastExecutionAt = &at
			}
			x, e := r.temporal.DescribeWorkflowExecution(ctx, a.StartWorkflowResult.WorkflowID, a.StartWorkflowResult.FirstExecutionRunID)
			if e != nil || x == nil || x.WorkflowExecutionInfo == nil {
				v.ExecutionHistoryAvailable = false
				continue
			}
			status := x.WorkflowExecutionInfo.Status
			if status == enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED && v.LastSuccessAt == nil {
				t := x.WorkflowExecutionInfo.GetCloseTime()
				if t != nil {
					at := t.AsTime().UTC()
					v.LastSuccessAt = &at
				} else {
					v.ExecutionHistoryAvailable = false
				}
			}
			if j == len(d.Info.RecentActions)-1 && status != enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED && status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				v.LastErrorCode = "WORKFLOW_" + status.String()
			}
		}
		if d.Info.NumActions > len(d.Info.RecentActions) && v.LastSuccessAt == nil {
			v.ExecutionHistoryAvailable = false
		}
	}
}
func (r *Runtime) inspect(ctx context.Context) (ControlState, error) {
	s := r.baseState()
	operator, pending, bootstrap := false, false, false
	if r.options.Engine == EngineLegacy {
		s.RoutingReady = s.BootstrapReady
		r.mu.Lock()
		paused := r.blocked
		operation := r.legacyOperation
		r.mu.Unlock()
		if r.service.CheckBusinessLicense(ctx) != nil {
			s.PausedReason = "PAUSED_LICENSE"
		} else if r.service.Notifications == nil {
			s.PausedReason = "DEPENDENCY_UNAVAILABLE"
		} else if paused && operation != "" {
			s.PausedReason = "PAUSED_OPERATION"
		} else if paused {
			s.PausedReason = "PAUSED_OPERATOR"
		} else if s.BootstrapReady {
			s.PausedReason = "READY"
			s.ExecutionReady = true
		} else {
			s.PausedReason = "BOOTSTRAP_PENDING"
		}
		if n, e := r.service.CountActiveBackgroundLeases(ctx, r.options.TenantID, time.Now().UTC()); e == nil {
			s.ActiveLeases = n
		} else if s.PausedReason != "PAUSED_LICENSE" {
			s.ExecutionReady = false
			s.PausedReason = "DEPENDENCY_UNAVAILABLE"
		}
		return s, nil
	}
	for _, kind := range taskKinds {
		d, e := r.schedules.GetHandle(ctx, r.scheduleID(kind)).Describe(ctx)
		if e != nil {
			s.PausedReason = "DEPENDENCY_UNAVAILABLE"
			return s, errors.New("schedule unavailable")
		}
		if e = r.validateObservedSchedule(kind, d); e != nil {
			s.PausedReason = "DEPENDENCY_UNAVAILABLE"
			return s, e
		}
		state := ScheduleState{ID: r.scheduleID(kind), Kind: kind, Paused: d.Schedule.State.Paused, Spec: d.Schedule.Spec.Intervals[0].Every.String()}
		p, _ := readPause(d.Schedule.State)
		switch {
		case p != nil:
			state.ControlReason = "OPERATION"
			state.OperationID = p.OperationID
			pending = true
		case d.Schedule.State.Paused && d.Schedule.State.Note == bootstrapNote:
			state.ControlReason = "BOOTSTRAP"
			bootstrap = true
		case d.Schedule.State.Paused:
			state.ControlReason = "OPERATOR"
			operator = true
		default:
			state.ControlReason = "ACTIVE"
		}
		s.Schedules = append(s.Schedules, state)
	}
	if n, e := r.service.CountActiveBackgroundLeases(ctx, r.options.TenantID, time.Now().UTC()); e == nil {
		s.ActiveLeases = n
	}
	routingErr := r.checkRouting(ctx)
	r.mu.Lock()
	locallyBlocked := r.blocked
	r.mu.Unlock()
	s.RoutingReady = routingErr == nil
	switch {
	case r.service.CheckBusinessLicense(ctx) != nil:
		s.PausedReason = "PAUSED_LICENSE"
	case routingErr != nil:
		if errors.Is(routingErr, errUnpromoted) {
			s.PausedReason = "POLLING_UNPROMOTED"
		} else {
			s.PausedReason = "DEPENDENCY_UNAVAILABLE"
		}
	case s.ActiveLeases < 0 || r.service.Notifications == nil:
		s.PausedReason = "DEPENDENCY_UNAVAILABLE"
	case locallyBlocked:
		s.PausedReason = "PAUSED_OPERATION"
	case pending:
		s.PausedReason = "PAUSED_OPERATION"
	case bootstrap:
		s.PausedReason = "PAUSED_BOOTSTRAP"
	case operator:
		s.PausedReason = "PAUSED_OPERATOR"
	case !s.BootstrapReady:
		s.PausedReason = "BOOTSTRAP_PENDING"
	default:
		s.PausedReason = "READY"
		s.ExecutionReady = true
	}
	return s, nil
}
func (r *Runtime) routingEvidence(ctx context.Context) error {
	d, e := r.temporal.WorkerDeploymentClient().GetHandle(r.deployment).Describe(ctx, client.WorkerDeploymentDescribeOptions{})
	if e != nil {
		return errors.New("deployment unavailable")
	}
	current := d.Info.RoutingConfig.CurrentVersion
	if current == nil || current.BuildID != r.options.BuildID || current.DeploymentName != r.deployment {
		return errUnpromoted
	}
	for _, kind := range []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY} {
		resp, e := r.temporal.WorkflowService().DescribeTaskQueue(ctx, &workflowservice.DescribeTaskQueueRequest{Namespace: r.options.Namespace, TaskQueue: &taskqueuepb.TaskQueue{Name: r.taskQueue}, TaskQueueType: kind, ReportPollers: true})
		if e != nil || resp == nil {
			return errors.New("poller evidence unavailable")
		}
		found := false
		for _, p := range resp.Pollers {
			o := p.GetDeploymentOptions()
			if o.GetDeploymentName() == r.deployment && o.GetBuildId() == r.options.BuildID && o.GetWorkerVersioningMode() == enumspb.WORKER_VERSIONING_MODE_VERSIONED && p.GetLastAccessTime() != nil && time.Since(p.GetLastAccessTime().AsTime()) < 2*time.Minute {
				found = true
			}
		}
		if !found {
			return errUnpromoted
		}
	}
	return nil
}
func (r *Runtime) ensure(ctx context.Context, operation string) error {
	return r.ensureConfig(ctx, operation, false)
}
func (r *Runtime) ensureConfig(ctx context.Context, operation string, restore bool) error {
	if e := r.migrateSchedules(ctx, operation, restore); e != nil {
		return e
	}
	ensureCtx, cancel := context.WithTimeout(ctx, temporalworker.CurrentVersionEnsureTimeout)
	defer cancel()
	if e := temporalworker.EnsureCurrentVersion(ensureCtx, r.temporal.WorkerDeploymentClient().GetHandle(r.deployment), r.options.BuildID, "background-control:"+operation); e != nil {
		return errors.New("deployment promotion unavailable")
	}
	return r.checkRouting(ensureCtx)
}
func (r *Runtime) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if q.URL.Path != "/healthz" && q.URL.Path != "/health/bootstrap" && q.URL.Path != "/health/routing" && q.URL.Path != "/health/execution" {
			http.NotFound(w, q)
			return
		}
		ctx, cancel := context.WithTimeout(q.Context(), 5*time.Second)
		defer cancel()
		s := r.baseState()
		ready := s.BootstrapReady
		if q.URL.Path == "/health/routing" || q.URL.Path == "/health/execution" {
			var e error
			s, e = r.inspect(ctx)
			ready = e == nil && s.RoutingReady
			if q.URL.Path == "/health/execution" {
				ready = ready && s.ExecutionReady
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(s)
	})
}

func (r *Runtime) scheduleDrainSnapshot(ctx context.Context, kind TaskKind) (*schedulepb.ScheduleInfo, error) {
	d, e := r.schedules.GetHandle(ctx, r.scheduleID(kind)).Describe(ctx)
	if e != nil {
		return nil, errors.New("schedule unavailable")
	}
	if e = r.validateObservedSchedule(kind, d); e != nil {
		return nil, e
	}
	if !d.Schedule.State.Paused {
		return nil, errors.New("schedule not paused")
	}
	raw, e := r.temporal.WorkflowService().DescribeSchedule(ctx, &workflowservice.DescribeScheduleRequest{Namespace: r.options.Namespace, ScheduleId: r.scheduleID(kind)})
	if e != nil || raw == nil || raw.Info == nil || raw.Schedule == nil || raw.Schedule.State == nil || !raw.Schedule.State.Paused {
		return nil, errors.New("authoritative schedule evidence unavailable")
	}
	if raw.Info.BufferSize != 0 {
		return nil, errors.New("schedule has buffered actions")
	}
	return raw.Info, nil
}
func (r *Runtime) runsSettled(ctx context.Context, id string, info *schedulepb.ScheduleInfo) error {
	executions := append([]*commonpb.WorkflowExecution{}, info.RunningWorkflows...)
	for _, a := range info.RecentActions {
		if a == nil || a.GetStartWorkflowResult() == nil {
			return errors.New("schedule action evidence incomplete")
		}
		executions = append(executions, a.GetStartWorkflowResult())
	}
	if info.ActionCount > 0 && len(executions) == 0 {
		return errors.New("schedule execution evidence incomplete")
	}
	for _, x := range executions {
		if x == nil || !strings.HasPrefix(x.WorkflowId, id+"-") || x.RunId == "" {
			return errors.New("foreign or incomplete schedule execution")
		}
		d, e := r.temporal.DescribeWorkflowExecution(ctx, x.WorkflowId, "")
		if e != nil || d == nil || d.WorkflowExecutionInfo == nil {
			return errors.New("execution evidence unavailable")
		}
		v := d.WorkflowExecutionInfo
		var owner string
		if v.GetType().GetName() != WorkflowName || v.TaskQueue != r.taskQueue || decodeValue(v.GetMemo().GetFields()[ownerKey], &owner) != nil || owner != boundIdentity(r.options.Environment, r.options.TenantID) {
			return errors.New("foreign workflow execution")
		}
		switch v.Status {
		case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		default:
			return errors.New("workflow still running or unknown")
		}
	}
	return nil
}

// No visibility count is used as proof: it can lag a fresh Schedule action. We
// read the Schedule's own buffer and running/recent records, describe executions
// directly, and require a second unchanged authoritative snapshot.
func (r *Runtime) drain(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		settled := true
		for _, kind := range taskKinds {
			first, e := r.scheduleDrainSnapshot(ctx, kind)
			if e != nil {
				return e
			}
			if e = r.runsSettled(ctx, r.scheduleID(kind), first); e != nil {
				settled = false
			}
			second, e := r.scheduleDrainSnapshot(ctx, kind)
			if e != nil {
				return e
			}
			if second.ActionCount != first.ActionCount || len(second.RunningWorkflows) > 0 {
				settled = false
			}
		}
		// Visibility supplements the authoritative Schedule records: unknown/manual
		// executions on this dedicated queue must also be accounted for. An empty
		// visibility result alone is never used as a drain proof.
		if e := r.openExecutionsSettled(ctx); e != nil {
			settled = false
		}
		n, e := r.service.CountActiveBackgroundLeases(ctx, r.options.TenantID, time.Now().UTC())
		if e != nil {
			return e
		}
		r.mu.Lock()
		active := r.active
		r.mu.Unlock()
		if settled && n == 0 && active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("background drain deadline exceeded")
		case <-ticker.C:
		}
	}
}

func (r *Runtime) openExecutionsSettled(ctx context.Context) error {
	request := &workflowservice.ListWorkflowExecutionsRequest{Namespace: r.options.Namespace, PageSize: 100, Query: "TaskQueue = '" + strings.ReplaceAll(r.taskQueue, "'", "''") + "' AND WorkflowType = '" + WorkflowName + "' AND ExecutionStatus = 'Running'"}
	for page := 0; page < 100; page++ {
		response, e := r.temporal.ListWorkflow(ctx, request)
		if e != nil || response == nil {
			return errors.New("owned workflow listing unavailable")
		}
		for _, v := range response.Executions {
			if v == nil || v.Execution == nil {
				return errors.New("owned workflow listing incomplete")
			}
			d, e := r.temporal.DescribeWorkflowExecution(ctx, v.Execution.WorkflowId, "")
			if e != nil || d == nil || d.WorkflowExecutionInfo == nil {
				return errors.New("owned workflow evidence unavailable")
			}
			info := d.WorkflowExecutionInfo
			var owner string
			if info.GetType().GetName() != WorkflowName || info.TaskQueue != r.taskQueue || decodeValue(info.GetMemo().GetFields()[ownerKey], &owner) != nil || owner != boundIdentity(r.options.Environment, r.options.TenantID) {
				return errors.New("unknown workflow on dedicated queue")
			}
			switch info.Status {
			case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
			default:
				return errors.New("owned workflow still running")
			}
		}
		if len(response.NextPageToken) == 0 {
			return nil
		}
		request.NextPageToken = response.NextPageToken
	}
	return errors.New("owned workflow listing bound exceeded")
}
