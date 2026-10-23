package mysql

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/migrations"
	"github.com/oklog/ulid/v2"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type realSnapshotBoundary struct {
	gorm.ConnPool
	readonlyObserved bool
	invalidateCommit bool
}

func (b *realSnapshotBoundary) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := b.ConnPool.(gorm.TxBeginner).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if opts == nil || !opts.ReadOnly || opts.Isolation != sql.LevelRepeatableRead {
		tx.Rollback()
		return nil, errors.New("source did not request read-only RR")
	}
	_, err = tx.ExecContext(ctx, "UPDATE pm_project SET name=name WHERE tenant_id='snapshot-readonly-probe'")
	var mysqlError *drivermysql.MySQLError
	if !errors.As(err, &mysqlError) || mysqlError.Number != 1792 {
		tx.Rollback()
		return nil, errors.New("MySQL did not enforce read-only transaction")
	}
	b.readonlyObserved = true
	return &realSnapshotTx{ConnPool: tx, tx: tx, invalidate: b.invalidateCommit}, nil
}

type realSnapshotTx struct {
	gorm.ConnPool
	tx         *sql.Tx
	invalidate bool
}

func (x *realSnapshotTx) Commit() error {
	if x.invalidate {
		_ = x.tx.Rollback()
	}
	return x.tx.Commit()
}
func (x *realSnapshotTx) Rollback() error { return x.tx.Rollback() }

type realSnapshotSink struct {
	dbSnapshotSink
	onFirst func() error
}

func (s *realSnapshotSink) Project(p domain.ProjectProjection) error {
	if err := s.dbSnapshotSink.Project(p); err != nil {
		return err
	}
	if len(s.rows) == 1 && s.onFirst != nil {
		return s.onFirst()
	}
	return nil
}
func snapshotMySQLFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	path := os.Getenv("PM_SNAPSHOT_TEST_DSN_FILE")
	if path == "" {
		t.Skip("set dedicated PM_SNAPSHOT_TEST_DSN_FILE for isolated MySQL snapshot verification")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("test DSN file must be private and regular")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read isolated test DSN")
	}
	parsed, err := drivermysql.ParseDSN(strings.TrimSpace(string(raw)))
	if err != nil || !strings.HasSuffix(parsed.DBName, "_test") || parsed.Net != "tcp" || !strings.HasPrefix(parsed.Addr, "127.0.0.1:") {
		t.Fatal("isolated loopback _test schema required")
	}
	dsn := strings.TrimSpace(string(raw))
	if err := migration.Run(context.Background(), dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated test database")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(6)
	t.Cleanup(func() { pool.Close() })
	tenant := "SNAP-" + ulid.Make().String()
	t.Cleanup(func() {
		for _, table := range []string{"pm_service_item", "pm_project"} {
			if err := db.Unscoped().Table(table).Where("tenant_id IN ?", []string{tenant, tenant + "-other"}).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
		}
	})
	return db, tenant
}
func TestProjectSnapshotRealMySQLRRReadOnlyAndCommit(t *testing.T) {
	db, tenant := snapshotMySQLFixture(t)
	ctx := context.Background()
	suffix := ulid.Make().String()
	firstID, secondID := "P-B"+suffix, "P-a"+suffix
	old := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	changed := old.Add(time.Hour)
	for _, p := range []projectRecord{{ID: firstID, TenantID: tenant, ContractID: firstID, Name: "first", CreatedAt: old, UpdatedAt: old}, {ID: secondID, TenantID: tenant, ContractID: secondID, Name: "second-old", Status: domain.ProjectStatusPendingExecution, CreatedAt: old, UpdatedAt: old}, {ID: "P-X" + suffix, TenantID: tenant + "-other", ContractID: "P-X" + suffix, Name: "private", CreatedAt: old, UpdatedAt: old}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	item := serviceItemRecord{ID: "S-" + suffix, TenantID: tenant, ProjectID: secondID, Status: domain.ProjectStatusFieldCompleted, ReportStatus: "ARCHIVED", CreatedAt: old, UpdatedAt: old}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	boundary := &realSnapshotBoundary{ConnPool: pool}
	copy := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	copy.ConnPool = boundary
	copy.Statement.ConnPool = boundary
	repo := NewRepository(copy)
	limits := application.DefaultProjectSnapshotLimits()
	limits.BatchSize = 1
	sink := &realSnapshotSink{onFirst: func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&projectRecord{}).Where("id=?", secondID).Updates(map[string]any{"name": "second-new", "updated_at": changed}).Error; err != nil {
				return err
			}
			return tx.Model(&serviceItemRecord{}).Where("id=?", item.ID).UpdateColumn("archived_at", changed).Error
		})
	}}
	summary, err := repo.StreamProjectSnapshot(ctx, tenant, limits, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !boundary.readonlyObserved || summary.Total != 2 || len(sink.rows) != 2 || sink.rows[0].ID != firstID || sink.rows[1].ID != secondID {
		t.Fatal("read-only/binary-order/tenant snapshot failed")
	}
	second := sink.rows[1]
	if second.Name != "second-old" || second.ServiceItems != 1 || second.Status != domain.ProjectStatusCompleted || !second.SourceUpdatedAt.Equal(old) {
		t.Fatalf("mixed source snapshot: %+v", second)
	}
	next := &dbSnapshotSink{}
	summary, err = repo.StreamProjectSnapshot(ctx, tenant, limits, next)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || next.rows[1].Name != "second-new" || next.rows[1].ServiceItems != 0 || !next.rows[1].SourceUpdatedAt.Equal(changed) {
		t.Fatal("next snapshot missed committed update/archive")
	}
	boundary.invalidateCommit = true
	failed := &dbSnapshotSink{}
	result, err := repo.StreamProjectSnapshot(ctx, tenant, limits, failed)
	if err == nil || result.Type != "" || len(failed.rows) != 2 {
		t.Fatal("invalidated real SQL transaction returned success summary")
	}
	boundary.invalidateCommit = false
	cancelled, cancel := context.WithCancel(ctx)
	cancelSink := &realSnapshotSink{onFirst: func() error { cancel(); return nil }}
	if result, err = repo.StreamProjectSnapshot(cancelled, tenant, limits, cancelSink); err == nil || result.Type != "" {
		t.Fatal("cancelled transaction returned success")
	}
	limits.MaxProjects = 1
	if result, err = repo.StreamProjectSnapshot(ctx, tenant, limits, &dbSnapshotSink{}); err == nil || result.Type != "" {
		t.Fatal("overflow returned success")
	}
}
