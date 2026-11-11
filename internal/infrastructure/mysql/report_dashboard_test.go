package mysql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
)

// TestReportDashboardCountsVersionsAndRealMedian 验证有效版本、作废和真实周期隔离。
// 参数：t为测试上下文。
// 返回值：无，统计口径或事务边界错误时失败。
func TestReportDashboardCountsVersionsAndRealMedian(t *testing.T) {
	repo, boundary, db := snapshotTestDB(t)
	if err := db.Exec(`CREATE TABLE pm_report_revision (id TEXT, tenant_id TEXT, status TEXT, validity_status TEXT, prepared_at DATETIME, issued_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		id, tenant, status, valid string
		days                      int
		complete                  bool
	}{
		{"1", "tenant", "ISSUED", "ACTIVE", 2, true}, {"2", "tenant", "ARCHIVED", "ACTIVE", 4, true},
		{"3", "tenant", "COMPILING", "ACTIVE", 0, false}, {"4", "tenant", "ISSUED", "VOID", 100, true},
		{"5", "tenant", "ISSUED", "SUPERSEDED", 100, true}, {"6", "other", "ISSUED", "ACTIVE", 100, true},
		{"7", "tenant", "ISSUED", "ACTIVE", -1, true},
	} {
		var issued any
		if row.complete {
			issued = at.AddDate(0, 0, row.days)
		}
		if err := db.Exec(`INSERT INTO pm_report_revision VALUES (?,?,?,?,?,?,?)`, row.id, row.tenant, row.status, row.valid, at, issued, at).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.ReadReportDashboard(context.Background(), "tenant")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.TenantID != "tenant" || got.Metrics.ActiveReports != 4 || got.Metrics.VoidReports != 1 || got.Metrics.CycleSamples != 2 || got.Metrics.CycleP50Days == nil || *got.Metrics.CycleP50Days != 3 || got.Metrics.ReviewedReports != 3 {
		t.Fatalf("unexpected snapshot: %+v metrics=%+v", got, got.Metrics)
	}
	if !boundary.committed || boundary.opts == nil || !boundary.opts.ReadOnly {
		t.Fatal("missing read-only snapshot commit")
	}
	empty, err := repo.ReadReportDashboard(context.Background(), "empty")
	if err != nil || !empty.Available || empty.Metrics.CycleP50Days != nil || empty.SourceUpdatedAt != "" {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	boundary.commitErr = errors.New("commit failed")
	if _, err = repo.ReadReportDashboard(context.Background(), "tenant"); err == nil {
		t.Fatal("commit failure published snapshot")
	}
	if _, err = repo.ReadReportDashboard(context.Background(), ""); !errors.Is(err, application.ErrValidation) {
		t.Fatalf("invalid tenant err=%v", err)
	}
}
