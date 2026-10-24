package background

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/temporalworker"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type workerRunner interface {
	RegisterWorkflowWithOptions(interface{}, workflow.RegisterOptions)
	RegisterActivityWithOptions(interface{}, activity.RegisterOptions)
	Start() error
	Stop()
}
type Runtime struct {
	options                     Options
	service                     *application.Service
	temporal                    client.Client
	schedules                   client.ScheduleClient
	logger                      *slog.Logger
	taskQueue, deployment       string
	mu                          sync.Mutex
	controlMu                   sync.Mutex
	active                      int
	bootstrap, blocked, started bool
	worker                      workerRunner
	cancel                      context.CancelFunc
	activityCtx                 context.Context
	legacyDone                  chan struct{}
	legacyOperation             string
	socket                      net.Listener
	controlServer               *http.Server
	newWorker                   func() workerRunner
	checkRouting                func(context.Context) error
}

func NewRuntime(o Options, service *application.Service, c client.Client, logger *slog.Logger) (*Runtime, error) {
	if e := o.Validate(); e != nil {
		return nil, e
	}
	if service == nil || service.CheckBusinessLicense == nil {
		return nil, errors.New("background service and license gate required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if o.Engine == EngineTemporal && c == nil {
		return nil, errors.New("Temporal client required")
	}
	r := &Runtime{options: o, service: service, temporal: c, logger: logger, taskQueue: runtimeName("queue", o.TaskQueue, o.Environment, o.TenantID), deployment: runtimeName("deployment", o.DeploymentName, o.Environment, o.TenantID)}
	if c != nil {
		r.schedules = c.ScheduleClient()
	}
	var configuredWorker worker.Options
	if o.Engine == EngineTemporal {
		var err error
		configuredWorker, err = temporalworker.WorkerOptions(temporalworker.VersioningConfig{Enabled: true, DeploymentName: r.deployment, BuildID: o.BuildID, Policy: "PINNED"})
		if err != nil {
			return nil, errors.New("background versioning configuration invalid")
		}
	}
	r.newWorker = func() workerRunner {
		opts := configuredWorker
		opts.WorkerStopTimeout = o.WorkflowRunTimeout + time.Minute
		opts.BackgroundActivityContext = r.activityContext()
		return worker.New(c, r.taskQueue, opts)
	}
	r.checkRouting = r.routingEvidence
	return r, nil
}

// Worker cancellation is explicit; Stop waits for the SDK's bounded graceful stop.
func (r *Runtime) activityContext() context.Context { return r.activityCtx }
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("background already started")
	}
	r.started = true
	r.activityCtx, r.cancel = context.WithCancel(context.WithoutCancel(ctx))
	r.mu.Unlock()
	if r.options.Engine == EngineTemporal {
		if e := r.reconcile(ctx); e != nil {
			return e
		}
		w := r.newWorker()
		w.RegisterWorkflowWithOptions(RunTick, workflow.RegisterOptions{Name: WorkflowName})
		for _, kind := range taskKinds {
			kind := kind
			name, _ := activityName(kind)
			w.RegisterActivityWithOptions(func(ctx context.Context, input TickInput) (TickResult, error) {
				if input.Kind != kind {
					return TickResult{}, errors.New("activity kind mismatch")
				}
				return r.runOne(ctx, input)
			}, activity.RegisterOptions{Name: name})
		}
		r.worker = w
		if e := w.Start(); e != nil {
			return errors.New("background poller start unavailable")
		}
	}
	listener, e := listenControl(r.options.ControlSocket)
	if e != nil {
		if r.worker != nil {
			r.worker.Stop()
			r.worker = nil
		}
		return e
	}
	r.socket = listener
	r.controlServer = &http.Server{Handler: r.controlHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second}
	go func() {
		if e := r.controlServer.Serve(listener); e != nil && !errors.Is(e, http.ErrServerClosed) {
			r.mu.Lock()
			r.bootstrap = false
			r.mu.Unlock()
			r.logger.Error("background control server stopped", "error_code", "CONTROL_UNAVAILABLE")
		}
	}()
	r.mu.Lock()
	r.bootstrap = true
	r.mu.Unlock()
	if r.options.Engine == EngineLegacy {
		r.legacyDone = make(chan struct{})
		go r.runLegacy()
	}
	return nil
}
func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.bootstrap = false
	r.blocked = true
	cancel := r.cancel
	w := r.worker
	r.mu.Unlock()
	if r.controlServer != nil {
		_ = r.controlServer.Close()
	}
	done := make(chan struct{})
	go func() {
		if w != nil {
			w.Stop()
		}
		if cancel != nil {
			cancel()
		}
		if r.legacyDone != nil {
			<-r.legacyDone
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if cancel != nil {
			cancel()
		}
		return errors.New("background stop deadline exceeded")
	}
}
func (r *Runtime) runLegacy() {
	defer close(r.legacyDone)
	sla := time.NewTicker(r.options.SLAInterval)
	dispatch := time.NewTicker(r.options.DispatchInterval)
	defer sla.Stop()
	defer dispatch.Stop()
	r.legacyTick(TaskSLA)
	r.legacyTick(TaskEffects)
	r.legacyTick(TaskNotifications)
	for {
		select {
		case <-r.activityCtx.Done():
			return
		case <-sla.C:
			r.legacyTick(TaskSLA)
		case <-dispatch.C:
			r.legacyTick(TaskEffects)
			r.legacyTick(TaskNotifications)
		}
	}
}
func (r *Runtime) legacyTick(kind TaskKind) {
	ctx, cancel := context.WithTimeout(r.activityCtx, r.options.WorkflowRunTimeout)
	defer cancel()
	start := time.Now()
	for i := 0; i < r.options.MaxItems && time.Since(start) < r.options.RunBudget; i++ {
		r.mu.Lock()
		if r.blocked {
			r.mu.Unlock()
			return
		}
		r.active++
		r.mu.Unlock()
		a := Activities{Service: r.service, TenantID: r.options.TenantID}
		result, e := a.RunOne(ctx, TickInput{TenantID: r.options.TenantID, Kind: kind})
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
		if e != nil {
			r.logger.Warn("legacy background activity failed", "kind", string(kind), "error_code", "ACTIVITY_FAILED")
			return
		}
		if kind == TaskSLA || result.Processed == 0 || result.PausedReason != "" {
			return
		}
	}
}
func (r *Runtime) runOne(ctx context.Context, input TickInput) (TickResult, error) {
	if ctx.Err() != nil {
		return TickResult{}, ctx.Err()
	}
	if input.TenantID != r.options.TenantID {
		return TickResult{}, errors.New("activity tenant mismatch")
	}
	// Read durable pause state before admitting a new Activity. Already admitted work
	// keeps its original license gate, so deployment pause cannot invalidate an ACK.
	d, e := r.schedules.GetHandle(ctx, r.scheduleID(input.Kind)).Describe(ctx)
	if e != nil {
		return TickResult{}, errors.New("schedule state unavailable")
	}
	if e = r.validateSchedule(input.Kind, d); e != nil {
		return TickResult{}, e
	}
	r.mu.Lock()
	if r.blocked || d.Schedule.State.Paused {
		r.mu.Unlock()
		return TickResult{PausedReason: "OPERATOR"}, nil
	}
	r.active++
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.active--; r.mu.Unlock() }()
	a := Activities{Service: r.service, TenantID: r.options.TenantID}
	return a.RunOne(ctx, input)
}

// Never pass Temporal SDK diagnostics (which may contain payloads) to application logs.
type quietTemporalLogger struct{ l *slog.Logger }

func (q quietTemporalLogger) Debug(string, ...interface{}) {}
func (q quietTemporalLogger) Info(string, ...interface{})  {}
func (q quietTemporalLogger) Warn(string, ...interface{}) {
	q.l.Warn("Temporal SDK warning", "error_code", "TEMPORAL_WARNING")
}
func (q quietTemporalLogger) Error(string, ...interface{}) {
	q.l.Error("Temporal SDK error", "error_code", "TEMPORAL_ERROR")
}
