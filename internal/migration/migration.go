package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	driver "github.com/go-sql-driver/mysql"
)

func Run(ctx context.Context, dsn string, files embed.FS) error {
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return err
	}
	cfg.MultiStatements = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS pm_schema_migration (name VARCHAR(255) PRIMARY KEY, checksum BINARY(32) NOT NULL, applied_at DATETIME(3) NOT NULL) ENGINE=InnoDB`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		var existing []byte
		err = db.QueryRowContext(ctx, "SELECT checksum FROM pm_schema_migration WHERE name = ?", entry.Name()).Scan(&existing)
		if err == nil {
			if string(existing) != string(sum[:]) {
				return fmt.Errorf("migration %s checksum changed", entry.Name())
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", entry.Name(), err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO pm_schema_migration(name,checksum,applied_at) VALUES(?,?,UTC_TIMESTAMP(3))", entry.Name(), sum[:]); err != nil {
			return err
		}
	}
	return nil
}

// Pending 返回尚未应用到目标库的迁移文件名（按名称升序）。
//
// 用途：进程启动与就绪检查。代码先上线、迁移没跑是真实发生过的故障模式——
// 新列只存在于代码里，任何写路径都会因 "Unknown column" 报 500，而对外只表现为
// "服务暂不可用"，排查成本很高。这里让"库结构落后于代码"变成显式状态：
// /readyz 直接 503 并列出待执行迁移，部署流水线能在放量前拦住。
func Pending(ctx context.Context, dsn string, files embed.FS) ([]string, error) {
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	// 迁移表不存在说明一次都没跑过：全部迁移都算未应用。
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'pm_schema_migration'`).Scan(&tableCount); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if tableCount == 0 {
		return names, nil
	}
	applied := map[string]bool{}
	rows, err := db.QueryContext(ctx, "SELECT name FROM pm_schema_migration")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pending := make([]string, 0, 4)
	for _, name := range names {
		if !applied[name] {
			pending = append(pending, name)
		}
	}
	return pending, nil
}
