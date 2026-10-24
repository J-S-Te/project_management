package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type snapshotDBBoundary struct {
	gorm.ConnPool
	opts                  *sql.TxOptions
	commitErr             error
	committed, rolledBack bool
}

func (b *snapshotDBBoundary) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	b.opts = opts
	tx, err := b.ConnPool.(gorm.TxBeginner).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &snapshotTxBoundary{ConnPool: tx, tx: tx, parent: b}, nil
}

type snapshotTxBoundary struct {
	gorm.ConnPool
	tx     *sql.Tx
	parent *snapshotDBBoundary
}

func (b *snapshotTxBoundary) Commit() error {
	if b.parent.commitErr != nil {
		return b.parent.commitErr
	}
	err := b.tx.Commit()
	b.parent.committed = err == nil
	return err
}
func (b *snapshotTxBoundary) Rollback() error { b.parent.rolledBack = true; return b.tx.Rollback() }

type dbSnapshotSink struct {
	header domain.ProjectSnapshotHeader
	rows   []domain.ProjectProjection
	err    error
}

func (s *dbSnapshotSink) Begin(h domain.ProjectSnapshotHeader) error { s.header = h; return s.err }
func (s *dbSnapshotSink) Project(p domain.ProjectProjection) error {
	s.rows = append(s.rows, p)
	return s.err
}
func snapshotTestDB(t *testing.T) (*Repository, *snapshotDBBoundary, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err = db.AutoMigrate(&projectRecord{}, &serviceItemRecord{}); err != nil {
		t.Fatal(err)
	}
	boundary := &snapshotDBBoundary{ConnPool: pool}
	copied := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	copied.ConnPool = boundary
	copied.Statement.ConnPool = boundary
	return NewRepository(copied), boundary, db
}
func seedSnapshotProjects(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	for _, p := range []projectRecord{{TenantID: "tenant", ID: "P-2", Name: "second", CreatedAt: now, UpdatedAt: now}, {TenantID: "tenant", ID: "P-1", Name: "first", CreatedAt: now, UpdatedAt: now}, {TenantID: "other", ID: "P-3", Name: "private", CreatedAt: now, UpdatedAt: now}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []serviceItemRecord{{TenantID: "tenant", ID: "S-1", ProjectID: "P-1", Status: domain.ProjectStatusFieldCompleted, ReportStatus: "ARCHIVED", CreatedAt: now, UpdatedAt: now}, {TenantID: "tenant", ID: "S-2", ProjectID: "P-1", Status: domain.ProjectStatusException, CreatedAt: now, UpdatedAt: now, ArchivedAt: gorm.DeletedAt{Time: later, Valid: true}}, {TenantID: "other", ID: "S-3", ProjectID: "P-1", Status: domain.ProjectStatusTerminated, CreatedAt: now, UpdatedAt: later.Add(time.Hour)}} {
		if err := db.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// A success summary can leave the repository only after successful Commit.
func TestProjectSnapshotRepositoryCommitAndActiveProjection(t *testing.T) {
	r, b, db := snapshotTestDB(t)
	seedSnapshotProjects(t, db)
	sink := &dbSnapshotSink{}
	limits := application.DefaultProjectSnapshotLimits()
	limits.BatchSize = 1
	summary, err := r.StreamProjectSnapshot(context.Background(), "tenant", limits, sink)
	if err != nil {
		t.Fatal(err)
	}
	if b.opts == nil || !b.opts.ReadOnly || b.opts.Isolation != sql.LevelRepeatableRead || !b.committed {
		t.Fatalf("transaction opts=%+v committed=%v", b.opts, b.committed)
	}
	if summary.Total != 2 || len(sink.rows) != 2 || sink.rows[0].ID != "P-1" || sink.rows[1].ID != "P-2" {
		t.Fatalf("summary=%+v rows=%+v", summary, sink.rows)
	}
	first := sink.rows[0]
	if first.Status != domain.ProjectStatusCompleted || first.Progress != 100 || first.Risk || first.ServiceItems != 1 || first.SourceUpdatedAt.Format(time.RFC3339) != "2026-10-23T01:00:00Z" || first.UpdatedAt.Format(time.RFC3339) != "2026-10-23T00:00:00Z" {
		t.Fatalf("active/archive/tenant metrics=%+v", first)
	}
}
func TestProjectSnapshotRepositoryFailuresHaveNoSuccessSummary(t *testing.T) {
	for _, tc := range []string{"commit", "sink", "projects", "items", "cancelled"} {
		t.Run(tc, func(t *testing.T) {
			r, b, db := snapshotTestDB(t)
			seedSnapshotProjects(t, db)
			sink := &dbSnapshotSink{}
			l := application.DefaultProjectSnapshotLimits()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch tc {
			case "commit":
				b.commitErr = errors.New("commit failed")
			case "sink":
				sink.err = errors.New("sink failed")
			case "projects":
				l.MaxProjects = 1
			case "items":
				l.MaxServiceItems = 1
				row := serviceItemRecord{ID: "S-4", TenantID: "tenant", ProjectID: "P-2", Status: "待分配"}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			summary, err := r.StreamProjectSnapshot(ctx, "tenant", l, sink)
			if err == nil || summary.Type != "" || b.committed {
				t.Fatalf("summary=%+v err=%v committed=%v", summary, err, b.committed)
			}
			if tc != "cancelled" && !b.rolledBack {
				t.Fatal("failed transaction not rolled back")
			}
		})
	}
}

// Database default unicode_ci order is not the byte order required by NDJSON.
func TestProjectSnapshotCursorUsesBinaryMySQLOrder(t *testing.T) {
	var rows []projectRecord
	q := snapshotProjectsQuery(dryRunDB(t), "tenant", "P-a", 200).Find(&rows).Statement
	sql := q.SQL.String()
	if !strings.Contains(sql, "BINARY id > BINARY ?") || !strings.Contains(sql, "ORDER BY BINARY id ASC") {
		t.Fatalf("cursor is not byte ordered: %s", sql)
	}
	if len(q.Vars) != 3 || q.Vars[0] != "tenant" || q.Vars[1] != "P-a" || q.Vars[2] != 200 {
		t.Fatalf("cursor args=%v", q.Vars)
	}
}
