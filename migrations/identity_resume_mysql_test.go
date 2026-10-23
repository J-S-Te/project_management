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

// 由集成 Agent 显式启用。仅接受空的、明确专用且名称以
// _identity_resume_test 结尾的数据库。原生运行绝不连接。
func TestContractIdentityDDLInterruptedBeforeLedgerOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PM_IDENTITY_MIGRATION_TEST_DSN"))
	if dsn == "" {
		t.Skip("PM_IDENTITY_MIGRATION_TEST_DSN is required; no connection attempted")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasSuffix(cfg.DBName, "_identity_resume_test") {
		t.Fatal("requires a dedicated empty *_identity_resume_test schema")
	}
	cfg.MultiStatements = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open isolated MySQL failed")
	}
	defer db.Close()
	ctx := context.Background()
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&tables); err != nil {
		t.Fatal("inspect isolated schema failed")
	}
	if tables != 0 {
		t.Fatal("refusing to modify a nonempty schema")
	}
	body, err := Files.ReadFile("000033_contract_project_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"old index", "both indexes", "DDL complete without ledger", "duplicates", "wrong index definition"} {
		t.Run(state, func(t *testing.T) {
			_, err := db.ExecContext(ctx, `CREATE TABLE pm_project (
 id VARCHAR(32) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
 contract_id VARCHAR(64) NOT NULL DEFAULT '', contract VARCHAR(64) NOT NULL,
 contract_version VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
 contract_identity_key VARCHAR(64) GENERATED ALWAYS AS (CASE WHEN contract_id <> '' THEN contract_id ELSE contract END) STORED,
 UNIQUE KEY uq_pm_project_contract_version (tenant_id, contract_identity_key, contract_version)
 ) ENGINE=InnoDB`)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, statement := range []string{"DROP PROCEDURE IF EXISTS pm_guard_contract_project_identity", "DROP PROCEDURE IF EXISTS pm_ensure_contract_project_identity", "DROP TABLE pm_project"} {
					if _, err := db.ExecContext(ctx, statement); err != nil {
						t.Error(err)
					}
				}
			})
			if _, err := db.ExecContext(ctx, `INSERT INTO pm_project(id,tenant_id,contract_id,contract,contract_version,created_at) VALUES('legacy','tenant','','HT-1','1',UTC_TIMESTAMP(3))`); err != nil {
				t.Fatal(err)
			}
			if state == "duplicates" {
				if _, err := db.ExecContext(ctx, `INSERT INTO pm_project(id,tenant_id,contract_id,contract,contract_version,created_at) VALUES('canonical','tenant','C-1','HT-1','2',UTC_TIMESTAMP(3))`); err != nil {
					t.Fatal(err)
				}
			}
			if state == "both indexes" || state == "DDL complete without ledger" {
				if _, err := db.ExecContext(ctx, "ALTER TABLE pm_project ADD UNIQUE KEY uq_pm_project_contract_identity (tenant_id, contract_identity_key)"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "DDL complete without ledger" {
				if _, err := db.ExecContext(ctx, "ALTER TABLE pm_project DROP INDEX uq_pm_project_contract_version"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "wrong index definition" {
				if _, err := db.ExecContext(ctx, "ALTER TABLE pm_project ADD KEY uq_pm_project_contract_identity (tenant_id, contract_identity_key)"); err != nil {
					t.Fatal(err)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				_, err := db.ExecContext(ctx, string(body))
				if state == "duplicates" || state == "wrong index definition" {
					var mysqlErr *driver.MySQLError
					if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1644 {
						t.Fatalf("expected fail-closed SIGNAL, got %v", err)
					}
				} else if err != nil {
					t.Fatalf("replay %d failed: %v", attempt, err)
				}
			}
			var rows int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pm_project").Scan(&rows); err != nil {
				t.Fatal(err)
			}
			wantRows := 1
			if state == "duplicates" {
				wantRows = 2
			}
			if rows != wantRows {
				t.Fatalf("rows=%d, want %d", rows, wantRows)
			}
			var oldColumns, newColumns int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='pm_project' AND index_name='uq_pm_project_contract_version'").Scan(&oldColumns); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='pm_project' AND index_name='uq_pm_project_contract_identity'").Scan(&newColumns); err != nil {
				t.Fatal(err)
			}
			if state == "duplicates" || state == "wrong index definition" {
				if oldColumns != 3 {
					t.Fatalf("fail-closed preflight changed old index: %d", oldColumns)
				}
			} else if oldColumns != 0 || newColumns != 2 {
				t.Fatalf("unexpected final indexes old=%d new=%d", oldColumns, newColumns)
			}
		})
	}
}
