package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/J-S-Te/license-core"
	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

const snapshotPath = "/internal/v1/dashboard/projects/snapshot"

type httpSnapshotRepository struct {
	application.Repository
	tenant         string
	committed      bool
	fail           string
	onBeforeCommit func()
}

func (r *httpSnapshotRepository) StreamProjectSnapshot(ctx context.Context, tenant string, l application.ProjectSnapshotLimits, sink application.ProjectSnapshotSink) (domain.ProjectSnapshotSummary, error) {
	r.tenant = tenant
	stream := application.NewProjectSnapshotStream(ctx, tenant, l, sink)
	now := time.Date(2026, 10, 23, 0, 0, 0, 123, time.UTC)
	h := domain.ProjectSnapshotHeader{Type: "header", SchemaVersion: 1, TenantID: tenant, SnapshotID: "synthetic-snapshot", SourceSnapshotAt: now}
	if err := stream.Begin(h); err != nil {
		return domain.ProjectSnapshotSummary{}, err
	}
	if r.fail == "timeout" {
		<-ctx.Done()
		return domain.ProjectSnapshotSummary{}, ctx.Err()
	}
	if r.fail != "empty" {
		p := domain.NewProjectProjection(domain.Project{TenantID: tenant, ID: "P-1", Name: "中文项目", CreatedAt: now, UpdatedAt: now}, h.SnapshotID, nil, now)
		if r.fail == "huge" {
			p.Name = strings.Repeat("x", 70000)
		}
		if r.fail == "invalid UTF-8" {
			p.Name = string([]byte{0xff})
		}
		if err := stream.Project(p); err != nil {
			return domain.ProjectSnapshotSummary{}, err
		}
	}
	if r.onBeforeCommit != nil {
		r.onBeforeCommit()
	}
	if r.fail == "commit" {
		return domain.ProjectSnapshotSummary{}, errors.New("commit rejected")
	}
	r.committed = true
	summary, err := stream.Summary()
	if r.fail == "summary" {
		summary.Total++
	}
	return summary, err
}
func snapshotRouter(repo *httpSnapshotRepository, l application.ProjectSnapshotLimits, gate CommercialLicenseGate) *gin.Engine {
	return NewRouter(&application.Service{Repo: repo}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{CommercialLicense: gate, ProjectSnapshotLimits: l, DashboardIntegration: &DashboardIntegrationOptions{Enabled: true, BearerVerifier: handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "trusted-tenant"}}}})
}
func snapshotRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, snapshotPath, nil)
	r.Header.Set("Authorization", "Bearer synthetic-machine-token")
	r.Header.Set("X-Tenant-ID", "attacker")
	return r
}

// End must be missing until the source transaction succeeds, even after rows streamed.
func TestProjectSnapshotProtocolCompletesOnlyAfterCommit(t *testing.T) {
	for _, failure := range []string{"", "empty", "commit", "huge", "invalid UTF-8", "summary"} {
		t.Run(failure, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := &httpSnapshotRepository{fail: failure}
			r.onBeforeCommit = func() {
				if strings.Contains(w.Body.String(), `"type":"end"`) {
					t.Fatal("end published before commit")
				}
			}
			router := snapshotRouter(r, application.ProjectSnapshotLimits{}, nil)
			router.ServeHTTP(w, snapshotRequest())
			success := failure == "" || failure == "empty"
			hasEnd := strings.Contains(w.Body.String(), `"type":"end"`)
			if hasEnd != success {
				t.Fatalf("success=%v status=%d body=%s", success, w.Code, w.Body.String())
			}
			if !success {
				return
			}
			if !r.committed || r.tenant != "trusted-tenant" || w.Code != 200 || w.Header().Get("Content-Type") != "application/x-ndjson" {
				t.Fatalf("committed=%v tenant=%s status=%d content=%s", r.committed, r.tenant, w.Code, w.Header().Get("Content-Type"))
			}
			lines := strings.Split(strings.TrimSuffix(w.Body.String(), "\n"), "\n")
			want := 3
			if failure == "empty" {
				want = 2
			}
			if len(lines) != want {
				t.Fatalf("lines=%d", len(lines))
			}
			var end domain.ProjectSnapshotSummary
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &end); err != nil {
				t.Fatal(err)
			}
			digest := sha256.New()
			for _, line := range lines[1 : len(lines)-1] {
				_, _ = digest.Write([]byte(line + "\n"))
			}
			if end.Total != want-2 || end.ChecksumSHA256 != hex.EncodeToString(digest.Sum(nil)) {
				t.Fatalf("end=%+v", end)
			}
		})
	}
}

func TestSnapshotHTTPEnforcesLimitsCancellationAndSinkFailure(t *testing.T) {
	for _, tc := range []string{"cancelled", "line", "sink", "timeout"} {
		t.Run(tc, func(t *testing.T) {
			r := &httpSnapshotRepository{}
			limits := application.DefaultProjectSnapshotLimits()
			req := snapshotRequest()
			w := httptest.NewRecorder()
			var writer http.ResponseWriter = w
			switch tc {
			case "cancelled":
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			case "line":
				limits.MaxLineBytes = 300
			case "timeout":
				r.fail = "timeout"
				limits.Timeout = time.Millisecond
			case "sink":
				writer = &snapshotFailWriter{ResponseRecorder: w}
			}
			snapshotRouter(r, limits, nil).ServeHTTP(writer, req)
			if strings.Contains(w.Body.String(), `"type":"end"`) {
				t.Fatalf("failure completed: %s", w.Body.String())
			}
		})
	}
}

type snapshotFailWriter struct{ *httptest.ResponseRecorder }

func (w *snapshotFailWriter) Write(b []byte) (int, error) { return 0, io.ErrClosedPipe }

// Removing the exact route, machine-only guard, or READ_HISTORY breaks this contract.
func TestSnapshotMachineTenantAndReadLicensePolicy(t *testing.T) {
	actor := platform.Principal{TenantID: "cookie-tenant"}
	for _, tc := range []struct {
		name, token, tenant string
		tokenErr            error
		want                int
	}{
		{name: "cookie cannot authenticate", want: 401},
		{name: "invalid token", token: "bad", tokenErr: errors.New("invalid"), want: 401},
		{name: "empty trusted tenant", token: "ok", want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &recordingCommercialGate{}
			router := NewRouter(&application.Service{}, projectLicenseIdentity{&actor}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{CommercialLicense: gate, DashboardIntegration: &DashboardIntegrationOptions{Enabled: true, BearerVerifier: handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: tc.tenant}, err: tc.tokenErr}}})
			req := httptest.NewRequest(http.MethodGet, snapshotPath, nil)
			req.AddCookie(&http.Cookie{Name: "session", Value: "cookie"})
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.want || gate.operation != core.READ_HISTORY {
				t.Fatalf("status=%d op=%s body=%s", w.Code, gate.operation, w.Body.String())
			}
		})
	}
	// Real registered route under an expired license still streams via READ_HISTORY.
	r := &httpSnapshotRepository{}
	w := httptest.NewRecorder()
	snapshotRouter(r, application.ProjectSnapshotLimits{}, expiredCommercialGate{}).ServeHTTP(w, snapshotRequest())
	if w.Code != 200 || !r.committed {
		t.Fatalf("expired history=%d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ method, path string }{{"GET", snapshotPath + "/other"}, {"GET", "/api/v1/dashboard/projects/snapshot"}, {"POST", snapshotPath}, {"GET", snapshotPath + "/"}} {
		if op := commercialOperation(tc.method, tc.path); op != core.MUTATE_BUSINESS {
			t.Fatalf("unreviewed %s %s classified %s", tc.method, tc.path, op)
		}
	}
}
func TestSnapshotOptionsCannotOpenLimits(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("invalid nonzero router limits must fail startup")
		}
	}()
	l := application.DefaultProjectSnapshotLimits()
	l.MaxProjects++
	snapshotRouter(&httpSnapshotRepository{}, l, nil)
}
