package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
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

// TestProjectSnapshotStreamEmitsReportStageWithSchemaOne 守护 PM→DA 的增量契约：
// project 行始终带 report_stage 键（空串表示未进入报告段，不是缺失/nulls），
// header/end 仍为 schema_version=1，end 的 checksum 仍按实际发出的行字节计算。
func TestProjectSnapshotStreamEmitsReportStageWithSchemaOne(t *testing.T) {
	sink := &snapshotCollector{}
	stream := NewProjectSnapshotStream(context.Background(), "tenant", DefaultProjectSnapshotLimits(), sink)
	header := snapshotHeader()
	if err := stream.Begin(header); err != nil {
		t.Fatal(err)
	}
	now := header.SourceSnapshotAt
	for _, tc := range []struct{ id, reportStatus, want, status string }{
		{"P-1", domain.ReportPhaseRectifying, domain.ReportPhaseRectifying, domain.ProjectStatusReporting},
		{"P-2", domain.ReportPhaseArchived, domain.ReportPhaseArchived, domain.ProjectStatusCompleted},
		{"P-3", "", "", domain.ProjectStatusPendingDecomposition},
	} {
		items := []domain.ProjectSnapshotItem{}
		if tc.reportStatus != "" {
			items = []domain.ProjectSnapshotItem{{Status: domain.ProjectStatusFieldCompleted, ReportStatus: tc.reportStatus}}
		}
		p := domain.NewProjectProjection(domain.Project{TenantID: "tenant", ID: tc.id, Name: "project", CreatedAt: now, UpdatedAt: now}, header.SnapshotID, items, now)
		if p.Status != tc.status || p.ReportStage != tc.want {
			t.Fatalf("%s projection status=%q report_stage=%q", tc.id, p.Status, p.ReportStage)
		}
		if err := stream.Project(p); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := stream.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if sink.header.SchemaVersion != 1 || summary.SchemaVersion != 1 || summary.Type != "end" || summary.Total != len(sink.rows) {
		t.Fatalf("header=%+v end=%+v", sink.header, summary)
	}
	digest := sha256.New()
	for _, row := range sink.rows {
		line, err := domain.ProjectSnapshotLine(row)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(line), `"report_stage":"`+row.ReportStage+`"`) {
			t.Fatalf("行缺少 report_stage: %s", line)
		}
		_, _ = digest.Write(line)
	}
	if summary.ChecksumSHA256 != hex.EncodeToString(digest.Sum(nil)) {
		t.Fatalf("checksum=%s，期望按含 report_stage 的行字节计算", summary.ChecksumSHA256)
	}
	// header/end 行不携带 report_stage：新字段只属于 type=project 行。
	for name, value := range map[string]any{"header": sink.header, "end": summary} {
		line, err := domain.ProjectSnapshotLine(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["report_stage"]; exists {
			t.Fatalf("%s 行不应出现 report_stage: %s", name, line)
		}
	}
}
