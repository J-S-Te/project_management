package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	driver "github.com/go-sql-driver/mysql"
)

// TestReportRectifyingCheckResumesOnMySQL 验证整改约束升级、重跑和未记账恢复。
// 参数：t 为测试上下文；环境变量必须指向本任务专用空库。
// 返回值：无；状态集合、历史数据或失败关闭语义不符时失败。
func TestReportRectifyingCheckResumesOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PM_RECTIFY_MIGRATION_TEST_DSN"))
	if dsn == "" {
		t.Skip("dedicated rectifying migration test DSN required")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasSuffix(cfg.DBName, "_rectify_migration_test") {
		t.Fatal("requires a dedicated empty *_rectify_migration_test schema")
	}
	cfg.MultiStatements = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open isolated MySQL failed")
	}
	defer db.Close()
	ctx := context.Background()
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("refusing to change nonempty migration test schema")
	}
	body, err := Files.ReadFile("000041_report_rectifying.sql")
	if err != nil {
		t.Fatalf("rectifying migration unavailable: %v", err)
	}
	for _, state := range []string{"old", "missing", "DDL complete without ledger", "wrong definition", "not enforced"} {
		t.Run(state, func(t *testing.T) { verifyRectifyingConstraint(t, db, string(body), state) })
	}
}

// verifyRectifyingConstraint 在最小真实表上验证一种约束恢复起点。
// 参数：t 为测试上下文，db 为专用空库，body 为迁移原文，state 为起始结构。
// 返回值：无；只清理本用例创建的表和存储过程。
func verifyRectifyingConstraint(t *testing.T, db *sql.DB, body, state string) {
	t.Helper()
	const old = "'NONE','COMPILING','REVIEWED','DRAFTING','SUBMITTED','APPROVED','ISSUED','ARCHIVED'"
	const desired = "'NONE','RECTIFYING','COMPILING','REVIEWED','DRAFTING','SUBMITTED','APPROVED','ISSUED','ARCHIVED'"
	constraint := ", CONSTRAINT chk_pm_report_revision_status CHECK (status IN (" + old + "))"
	switch state {
	case "missing":
		constraint = ""
	case "DDL complete without ledger":
		constraint = ", CONSTRAINT chk_pm_report_revision_status CHECK (status IN (" + desired + "))"
	case "wrong definition":
		constraint = ", CONSTRAINT chk_pm_report_revision_status CHECK (status <> 'INVALID')"
	case "not enforced":
		constraint += " NOT ENFORCED"
	}
	if _, err := db.Exec("CREATE TABLE pm_report_revision(id INT PRIMARY KEY, status VARCHAR(16) NOT NULL, validity_status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE', CONSTRAINT chk_pm_report_revision_validity CHECK (validity_status IN ('ACTIVE','VOID','SUPERSEDED'))" + constraint + ") ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{"DROP PROCEDURE IF EXISTS pm_ensure_report_rectifying", "DROP TABLE pm_report_revision"} {
			if _, err := db.Exec(query); err != nil {
				t.Error(err)
			}
		}
	})
	statuses := []string{"NONE", "COMPILING", "REVIEWED", "DRAFTING", "SUBMITTED", "APPROVED", "ISSUED", "ARCHIVED"}
	for i, status := range statuses {
		if _, err := db.Exec("INSERT INTO pm_report_revision(id,status) VALUES(?,?)", i+1, status); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, err := db.Exec(body)
		if state == "wrong definition" || state == "not enforced" {
			var mysqlErr *driver.MySQLError
			if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1644 {
				t.Fatalf("expected fail-closed SIGNAL, got %v", err)
			}
		} else if err != nil {
			t.Fatalf("migration attempt %d: %v", attempt, err)
		}
	}
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM pm_report_revision").Scan(&rows); err != nil || rows != len(statuses) {
		t.Fatalf("history rows=%d err=%v", rows, err)
	}
	for i, want := range statuses {
		var got string
		if err := db.QueryRow("SELECT status FROM pm_report_revision WHERE id=?", i+1).Scan(&got); err != nil || got != want {
			t.Fatalf("historical status changed: got=%q want=%q err=%v", got, want, err)
		}
	}
	if state == "wrong definition" || state == "not enforced" {
		return
	}
	if _, err := db.Exec("INSERT INTO pm_report_revision(id,status) VALUES(20,'RECTIFYING')"); err != nil {
		t.Fatalf("RECTIFYING rejected: %v", err)
	}
	for _, query := range []string{"INSERT INTO pm_report_revision(id,status) VALUES(21,'UNKNOWN')", "UPDATE pm_report_revision SET validity_status='UNKNOWN' WHERE id=1"} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("invalid value accepted: %s", query)
		}
	}
}
