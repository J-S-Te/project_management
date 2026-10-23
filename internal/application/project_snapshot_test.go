package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type snapshotCollector struct {
	header domain.ProjectSnapshotHeader
	rows   []domain.ProjectProjection
	err    error
}

func (s *snapshotCollector) Begin(h domain.ProjectSnapshotHeader) error { s.header = h; return s.err }
func (s *snapshotCollector) Project(p domain.ProjectProjection) error {
	s.rows = append(s.rows, p)
	return s.err
}
func snapshotHeader() domain.ProjectSnapshotHeader {
	return domain.ProjectSnapshotHeader{Type: "header", SchemaVersion: 1, TenantID: "tenant", SnapshotID: "snapshot", SourceSnapshotAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
}
func snapshotProject(id string) domain.ProjectProjection {
	now := snapshotHeader().SourceSnapshotAt
	return domain.NewProjectProjection(domain.Project{TenantID: "tenant", ID: id, Name: "project", CreatedAt: now, UpdatedAt: now}, "snapshot", nil, now)
}

type snapshotRepository struct {
	Repository
	calls  int
	tenant string
	block  bool
}

func (r *snapshotRepository) StreamProjectSnapshot(ctx context.Context, tenant string, l ProjectSnapshotLimits, s ProjectSnapshotSink) (domain.ProjectSnapshotSummary, error) {
	r.calls++
	r.tenant = tenant
	if r.block {
		<-ctx.Done()
		return domain.ProjectSnapshotSummary{}, ctx.Err()
	}
	return domain.ProjectSnapshotSummary{}, nil
}

// Removing validation would allow one large tenant/request to bypass every bound.
func TestSnapshotLimitsValidate(t *testing.T) {
	d := DefaultProjectSnapshotLimits()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ProjectSnapshotLimits){func(l *ProjectSnapshotLimits) { l.BatchSize = 201 }, func(l *ProjectSnapshotLimits) { l.MaxProjects = 50001 }, func(l *ProjectSnapshotLimits) { l.MaxServiceItems = 250001 }, func(l *ProjectSnapshotLimits) { l.MaxBytes = 64*1024*1024 + 1 }, func(l *ProjectSnapshotLimits) { l.MaxLineBytes = 65537 }, func(l *ProjectSnapshotLimits) { l.Timeout = 31 * time.Second }, func(l *ProjectSnapshotLimits) { l.MaxProjects = 0 }, func(l *ProjectSnapshotLimits) { l.MaxLineBytes = l.MaxBytes + 1 }} {
		l := d
		mutate(&l)
		if l.Validate() == nil {
			t.Fatalf("accepted limits=%+v", l)
		}
	}
	if (ProjectSnapshotLimits{}).Validate() == nil {
		t.Fatal("zero is resolved by caller, never valid as an unbounded limit")
	}
}

func TestSnapshotEnforcesLimitsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*ProjectSnapshotLimits)
		rows      []domain.ProjectProjection
	}{
		{"projects", func(l *ProjectSnapshotLimits) { l.MaxProjects = 1 }, []domain.ProjectProjection{snapshotProject("P-1"), snapshotProject("P-2")}},
		{"items", func(l *ProjectSnapshotLimits) { l.MaxServiceItems = 1 }, []domain.ProjectProjection{func() domain.ProjectProjection { p := snapshotProject("P-1"); p.ServiceItems = 2; return p }()}},
		{"line", func(l *ProjectSnapshotLimits) { l.MaxLineBytes = 300 }, []domain.ProjectProjection{snapshotProject("P-1")}},
		{"bytes include end", func(l *ProjectSnapshotLimits) { l.MaxBytes = 700; l.MaxLineBytes = 700 }, []domain.ProjectProjection{snapshotProject("P-1")}},
		{"duplicate id", func(l *ProjectSnapshotLimits) {}, []domain.ProjectProjection{snapshotProject("P-1"), snapshotProject("P-1")}},
		{"reverse id", func(l *ProjectSnapshotLimits) {}, []domain.ProjectProjection{snapshotProject("P-2"), snapshotProject("P-1")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := DefaultProjectSnapshotLimits()
			tc.configure(&l)
			s := NewProjectSnapshotStream(context.Background(), "tenant", l, &snapshotCollector{})
			err := s.Begin(snapshotHeader())
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range tc.rows {
				if err = s.Project(p); err != nil {
					break
				}
			}
			if err == nil {
				_, err = s.Summary()
			}
			if err == nil {
				t.Fatal("incomplete/over-limit stream accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewProjectSnapshotStream(ctx, "tenant", DefaultProjectSnapshotLimits(), &snapshotCollector{})
	if !errors.Is(s.Begin(snapshotHeader()), context.Canceled) {
		t.Fatal("cancelled stream must stop")
	}
	bad := &snapshotCollector{err: errors.New("sink failed")}
	s = NewProjectSnapshotStream(context.Background(), "tenant", DefaultProjectSnapshotLimits(), bad)
	if s.Begin(snapshotHeader()) == nil {
		t.Fatal("sink error swallowed")
	}
	if _, err := s.Summary(); err == nil {
		t.Fatal("failed begin cannot complete")
	}
}

func TestProjectSnapshotServiceAuthorizationAndDeadline(t *testing.T) {
	full := platform.Principal{TenantID: "tenant", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	for _, p := range []platform.Principal{{}, {TenantID: "tenant", DataScopes: full.DataScopes}, {TenantID: "tenant", Permissions: full.Permissions}, {TenantID: "tenant", Permissions: full.Permissions, DataScopes: []platform.DataScope{{ScopeType: "ORG", ScopeID: "org"}}}} {
		r := &snapshotRepository{}
		_, err := (&Service{Repo: r}).ExportProjectSnapshot(context.Background(), p, DefaultProjectSnapshotLimits(), &snapshotCollector{})
		if !errors.Is(err, ErrForbidden) || r.calls != 0 {
			t.Fatalf("authorization=%+v err=%v calls=%d", p, err, r.calls)
		}
	}
	r := &snapshotRepository{}
	_, err := (&Service{Repo: r}).ExportProjectSnapshot(context.Background(), full, DefaultProjectSnapshotLimits(), &snapshotCollector{})
	if err != nil || r.tenant != "tenant" {
		t.Fatalf("tenant=%s err=%v", r.tenant, err)
	}
	r.block = true
	l := DefaultProjectSnapshotLimits()
	l.Timeout = time.Millisecond
	_, err = (&Service{Repo: r}).ExportProjectSnapshot(context.Background(), full, l, &snapshotCollector{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
}

func TestProjectSnapshotStreamRejectsMalformedRecords(t *testing.T) {
	for _, tc := range []string{"header type", "version", "header tenant", "header snapshot", "header time", "duplicate header", "row type", "row tenant", "row snapshot", "row time", "row progress", "row count", "missing header", "project sink", "cancel after begin"} {
		t.Run(tc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := &snapshotCollector{}
			s := NewProjectSnapshotStream(ctx, "tenant", DefaultProjectSnapshotLimits(), sink)
			h := snapshotHeader()
			p := snapshotProject("P-1")
			switch tc {
			case "header type":
				h.Type = "end"
			case "version":
				h.SchemaVersion = 2
			case "header tenant":
				h.TenantID = "other"
			case "header snapshot":
				h.SnapshotID = ""
			case "header time":
				h.SourceSnapshotAt = time.Time{}
			}
			err := error(nil)
			if tc != "missing header" {
				err = s.Begin(h)
			}
			if err == nil {
				switch tc {
				case "duplicate header":
					err = s.Begin(h)
				case "row type":
					p.Type = "header"
				case "row tenant":
					p.TenantID = "other"
				case "row snapshot":
					p.SnapshotID = "different"
				case "row time":
					p.SourceUpdatedAt = time.Time{}
				case "row progress":
					p.Progress = 101
				case "row count":
					p.ServiceItems = -1
				case "project sink":
					sink.err = errors.New("write failed")
				case "cancel after begin":
					cancel()
				}
			}
			if err == nil {
				err = s.Project(p)
			}
			if err == nil {
				t.Fatal("malformed record accepted")
			}
			if _, err := s.Summary(); err == nil {
				t.Fatal("failed stream yielded successful summary")
			}
		})
	}
}
