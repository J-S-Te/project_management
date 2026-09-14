package migration

import (
	"context"
	"os"
	"testing"

	"github.com/j-s-te/project-management/migrations"
)

// Pending 是部署安全网：代码先上线、迁移没跑时，写路径只会报 500「服务暂不可用」，
// 排查成本极高。这里保证两件事：结构已就绪时不误报；迁移表缺失时全部算未应用。
func TestPendingReportsUnappliedMigrations(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to exercise the pending migration check")
	}
	pending, err := Pending(context.Background(), dsn, migrations.Files)
	if err != nil {
		t.Fatalf("check pending migrations: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("测试库已应用全部迁移，不应报告待执行：%v", pending)
	}
	// 非法 DSN 必须返回错误：未知的库结构状态不能被当成"已就绪"。
	if _, err := Pending(context.Background(), "invalid-dsn", migrations.Files); err == nil {
		t.Fatal("非法 DSN 应返回错误，而不是把未知状态当成已就绪")
	}
}
