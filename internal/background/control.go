package background

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/j-s-te/project-management/internal/application"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

type ControlRequest struct {
	Action      string `json:"action"`
	OperationID string `json:"operation_id"`
}
type ScheduleState struct {
	ID                        string     `json:"id"`
	Kind                      TaskKind   `json:"kind"`
	Paused                    bool       `json:"paused"`
	Spec                      string     `json:"spec"`
	ControlReason             string     `json:"control_reason"`
	OperationID               string     `json:"operation_id,omitempty"`
	LastExecutionAt           *time.Time `json:"last_execution_at,omitempty"`
	LastSuccessAt             *time.Time `json:"last_success_at,omitempty"`
	LastErrorCode             string     `json:"last_error_code,omitempty"`
	ExecutionHistoryAvailable bool       `json:"execution_history_available"`
}

// RuntimeConfig contains only the seven non-secret, migratable tuning values.
// Nanoseconds round-trip exactly through the host's private rollback record.
type RuntimeConfig struct {
	SLAInterval        time.Duration `json:"sla_interval"`
	DispatchInterval   time.Duration `json:"dispatch_interval"`
	MaxItems           int           `json:"max_items"`
	RunBudget          time.Duration `json:"run_budget"`
	ActivityTimeout    time.Duration `json:"activity_timeout"`
	SLAActivityTimeout time.Duration `json:"sla_activity_timeout"`
	WorkflowRunTimeout time.Duration `json:"workflow_run_timeout"`
}
type ControlState struct {
	Protocol            int                                `json:"protocol"`
	Engine              Engine                             `json:"engine"`
	TenantID            string                             `json:"tenant_id"`
	Environment         string                             `json:"environment"`
	BuildID             string                             `json:"build_id"`
	DeploymentName      string                             `json:"deployment_name"`
	TaskQueue           string                             `json:"task_queue"`
	BootstrapReady      bool                               `json:"bootstrap_ready"`
	RoutingReady        bool                               `json:"routing_ready"`
	ExecutionReady      bool                               `json:"execution_ready"`
	PausedReason        string                             `json:"paused_reason"`
	ActiveActivities    int                                `json:"active_activities"`
	ActiveLeases        int64                              `json:"active_leases"`
	Schedules           []ScheduleState                    `json:"schedules"`
	Drained             bool                               `json:"drained"`
	ErrorCode           string                             `json:"error_code,omitempty"`
	QueueStatsAvailable bool                               `json:"queue_stats_available"`
	QueueStats          []application.BackgroundQueueStats `json:"queue_stats,omitempty"`
	RuntimeConfig       *RuntimeConfig                     `json:"runtime_config,omitempty"`
}

var operationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)

func safeOperationID(s string) bool { return operationPattern.MatchString(s) && s != "." && s != ".." }
func validateControlRequest(q ControlRequest) error {
	switch q.Action {
	case "inspect":
		if q.OperationID != "" && !safeOperationID(q.OperationID) {
			return errors.New("invalid operation ID")
		}
	case "restore", "ensure", "pause", "drain", "resume", "verify":
		if !safeOperationID(q.OperationID) {
			return errors.New("operation ID required")
		}
	default:
		return errors.New("invalid control action")
	}
	return nil
}

func (r *Runtime) Control(ctx context.Context, q ControlRequest) (ControlState, error) {
	if e := validateControlRequest(q); e != nil {
		return r.baseState(), e
	}
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	var actionErr error
	if r.options.Engine == EngineLegacy {
		return r.controlLegacy(ctx, q)
	}
	switch q.Action {
	case "pause", "drain":
		r.mu.Lock()
		r.blocked = true
		r.mu.Unlock()
		actionErr = r.changePause(ctx, q.OperationID, true)
	case "ensure":
		actionErr = r.ensure(ctx, q.OperationID)
	case "restore":
		actionErr = r.ensureConfig(ctx, q.OperationID, true)
	case "resume":
		if actionErr = r.checkRouting(ctx); actionErr == nil {
			actionErr = r.changePause(ctx, q.OperationID, false)
		}
		if actionErr == nil {
			r.mu.Lock()
			r.blocked = false
			r.mu.Unlock()
		}
	}
	if actionErr == nil && q.Action == "drain" {
		actionErr = r.drain(ctx)
	}
	state, stateErr := r.inspect(ctx)
	if q.Action == "inspect" {
		r.observe(ctx, &state)
	}
	if actionErr == nil && stateErr != nil {
		actionErr = stateErr
	}
	if actionErr == nil && q.Action == "drain" {
		if state.ActiveLeases != 0 || state.ActiveActivities != 0 {
			actionErr = errors.New("drain evidence changed")
		} else {
			state.Drained = true
		}
	}
	if actionErr == nil && q.Action == "verify" && (!state.BootstrapReady || !state.RoutingReady || !state.ExecutionReady && state.PausedReason != "PAUSED_OPERATOR") {
		actionErr = errors.New("background verification pending")
	}
	if actionErr != nil {
		state.ErrorCode = "CONTROL_BLOCKED"
		return state, errors.New("background control blocked")
	}
	return state, nil
}
func (r *Runtime) controlLegacy(ctx context.Context, q ControlRequest) (ControlState, error) {
	r.mu.Lock()
	conflict := r.legacyOperation != "" && r.legacyOperation != q.OperationID && q.Action != "inspect" && q.Action != "verify"
	r.mu.Unlock()
	if conflict {
		s := r.baseState()
		s.ErrorCode = "CONTROL_BLOCKED"
		return s, errors.New("legacy controlled by another operation")
	}
	switch q.Action {
	case "pause", "drain":
		r.mu.Lock()
		r.blocked = true
		r.legacyOperation = q.OperationID
		r.mu.Unlock()
	case "resume":
		r.mu.Lock()
		r.blocked = false
		r.legacyOperation = ""
		r.mu.Unlock()
	}
	if q.Action == "drain" {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			n, e := r.service.CountActiveBackgroundLeases(ctx, r.options.TenantID, time.Now().UTC())
			if e != nil {
				return r.baseState(), e
			}
			r.mu.Lock()
			active := r.active
			r.mu.Unlock()
			if n == 0 && active == 0 {
				break
			}
			select {
			case <-ctx.Done():
				return r.baseState(), errors.New("legacy drain deadline exceeded")
			case <-ticker.C:
			}
		}
	}
	s, e := r.inspect(ctx)
	if q.Action == "inspect" {
		r.observe(ctx, &s)
	}
	if e == nil && q.Action == "drain" {
		if s.ActiveLeases != 0 || s.ActiveActivities != 0 {
			s.ErrorCode = "CONTROL_BLOCKED"
			return s, errors.New("legacy drain evidence changed")
		}
		s.Drained = true
	}
	if q.Action == "verify" && !s.ExecutionReady && s.PausedReason != "PAUSED_OPERATOR" {
		s.ErrorCode = "CONTROL_BLOCKED"
		return s, errors.New("legacy verification pending")
	}
	return s, e
}

func owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}

// Only this private leaf directory is writable by its owner. Every ancestor is
// checked for symbolic links so a configured path cannot redirect the socket.
func validateSocketPath(path string, create bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 100 || path == "/" {
		return errors.New("invalid control socket path")
	}
	dir := filepath.Dir(path)
	if create {
		if e := os.Mkdir(dir, 0700); e != nil && !os.IsExist(e) {
			return errors.New("control directory unavailable")
		}
	}
	for p := dir; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe control directory")
		}
		if p == dir && (!owned(info) || info.Mode().Perm() != 0700) {
			return errors.New("control directory must be private and owned")
		}
		if p == "/" {
			break
		}
	}
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		if create {
			return nil
		}
		return errors.New("control socket unavailable")
	}
	if e != nil || info.Mode()&os.ModeSocket == 0 || !owned(info) || info.Mode().Perm() != 0600 {
		return errors.New("unsafe or foreign control socket")
	}
	return nil
}

// A lifetime directory lock serializes recovery and listener replacement between
// cooperating processes. Never unlink this lock inode; process death releases it.
type controlListener struct {
	net.Listener
	directory *os.File
	path      string
	inode     os.FileInfo
	once      sync.Once
}

func (l *controlListener) Close() error {
	var err error
	l.once.Do(func() {
		err = l.Listener.Close()
		if current, e := os.Lstat(l.path); e == nil && os.SameFile(current, l.inode) {
			_ = os.Remove(l.path)
		}
		_ = l.directory.Close()
	})
	return err
}
func listenControl(path string) (net.Listener, error) {
	if e := validateSocketPath(path, true); e != nil {
		return nil, e
	}
	fd, e := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, errors.New("control directory unavailable")
	}
	dir := os.NewFile(uintptr(fd), filepath.Dir(path))
	keep := false
	defer func() {
		if !keep {
			_ = dir.Close()
		}
	}()
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return nil, errors.New("control listener already owned")
	}
	locked, e := dir.Stat()
	if e != nil {
		return nil, e
	}
	current, e := os.Lstat(filepath.Dir(path))
	if e != nil || !os.SameFile(locked, current) {
		return nil, errors.New("control directory replaced")
	}
	if e = validateSocketPath(path, true); e != nil {
		return nil, e
	}
	if stale, e := os.Lstat(path); e == nil {
		conn, probe := net.DialTimeout("unix", path, 250*time.Millisecond)
		if probe == nil {
			_ = conn.Close()
			return nil, errors.New("control listener still alive")
		}
		// Only ECONNREFUSED proves a socket has no listener. Timeout, permission,
		// missing or replaced paths are unknown and must never authorize removal.
		if !errors.Is(probe, syscall.ECONNREFUSED) {
			return nil, errors.New("control listener status unknown")
		}
		checked, e := os.Lstat(path)
		if e != nil || !os.SameFile(stale, checked) {
			return nil, errors.New("control socket replaced")
		}
		if e = validateSocketPath(path, false); e != nil {
			return nil, e
		}
		if e = os.Remove(path); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return nil, errors.New("control socket listen unavailable")
	}
	l.SetUnlinkOnClose(false)
	inode, e := os.Lstat(path)
	if e != nil {
		_ = l.Close()
		return nil, e
	}
	wrapped := &controlListener{Listener: l, directory: dir, path: path, inode: inode}
	if e = os.Chmod(path, 0600); e != nil {
		_ = wrapped.Close()
		return nil, errors.New("control socket permissions unavailable")
	}
	keep = true
	return wrapped, nil
}
func (r *Runtime) controlHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "POST" || req.URL.Path != "/control" {
			http.Error(w, `{"error_code":"INVALID_REQUEST"}`, 400)
			return
		}
		defer req.Body.Close()
		d := json.NewDecoder(http.MaxBytesReader(w, req.Body, 2048))
		d.DisallowUnknownFields()
		var q ControlRequest
		if e := d.Decode(&q); e != nil {
			http.Error(w, `{"error_code":"INVALID_REQUEST"}`, 400)
			return
		}
		if e := d.Decode(&struct{}{}); e != io.EOF {
			http.Error(w, `{"error_code":"INVALID_REQUEST"}`, 400)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 7*time.Minute)
		defer cancel()
		state, e := r.Control(ctx, q)
		if e != nil {
			if state.ErrorCode == "" {
				state.ErrorCode = "INVALID_REQUEST"
			}
			w.WriteHeader(http.StatusConflict)
		}
		_ = json.NewEncoder(w).Encode(state)
	})
}

// SendControl never loads environment files or accepts tenant/queue overrides.
func SendControl(ctx context.Context, path string, q ControlRequest) (ControlState, error) {
	s := ControlState{ActiveActivities: -1, ActiveLeases: -1, Schedules: []ScheduleState{}}
	if e := validateControlRequest(q); e != nil {
		return s, e
	}
	if e := validateSocketPath(path, false); e != nil {
		return s, e
	}
	b, _ := json.Marshal(q)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if e := validateSocketPath(path, false); e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, "POST", "http://unix/control", bytes.NewReader(b))
	if e != nil {
		return s, errors.New("control request invalid")
	}
	resp, e := (&http.Client{Transport: transport}).Do(req)
	if e != nil {
		return s, errors.New("control unavailable")
	}
	defer resp.Body.Close()
	d := json.NewDecoder(io.LimitReader(resp.Body, 128<<10))
	if e = d.Decode(&s); e != nil || s.Protocol != 1 {
		return s, errors.New("control response invalid")
	}
	if resp.StatusCode != 200 || s.ErrorCode != "" {
		return s, errors.New("background control blocked")
	}
	return s, nil
}
