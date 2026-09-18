package migration

import (
	"context"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/migrations"
)

// TestApplyAllMigrationsToIsolatedDatabase 是从空库开始的发布前迁移门禁。
// PM_TEST_DSN 必须只指向一次性隔离库；测试重复执行 Run，覆盖首次升级与幂等重跑。
func TestApplyAllMigrationsToIsolatedDatabase(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to an isolated writable database to apply migrations")
	}
	ctx := context.Background()
	if err := Run(ctx, dsn, migrations.Files); err != nil {
		t.Fatalf("apply migrations to isolated database: %v", err)
	}
	if err := Run(ctx, dsn, migrations.Files); err != nil {
		t.Fatalf("reapply migrations idempotently: %v", err)
	}
	pending, err := Pending(ctx, dsn, migrations.Files)
	if err != nil {
		t.Fatalf("check pending migrations: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending migrations after apply: %v", pending)
	}

	want := migrationNames(t, migrations.Files)
	if len(want) == 0 {
		t.Fatal("embedded migration set is empty")
	}
}

func migrationNames(t *testing.T, files fs.FS) []string {
	t.Helper()
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}
