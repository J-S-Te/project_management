package mysql

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/migrations"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestExpenseSpendMySQLConcurrency 仅在显式全新隔离MySQL中核验迁移重跑与同单并发幂等。
// 参数：t 为测试上下文，DSN仅从PM_REIMBURSEMENT_TEST_DSN读取且拒绝默认业务端口。
// 返回值：无；未配置时跳过且不建立连接，真实迁移或并发不符时失败。
func TestExpenseSpendMySQLConcurrency(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PM_REIMBURSEMENT_TEST_DSN"))
	if dsn == "" {
		t.Skip("requires explicit isolated PM_REIMBURSEMENT_TEST_DSN; no connection attempted")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "pm_reimbursement_test_") || cfg.Net != "tcp" || (!strings.HasPrefix(cfg.Addr, "127.0.0.1:") && !strings.HasPrefix(cfg.Addr, "localhost:")) || strings.HasSuffix(cfg.Addr, ":3306") {
		t.Fatal("requires entirely empty high-port local pm_reimbursement_test_ schema")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal("isolated MySQL unavailable")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var count int64
	if err = db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&count).Error; err != nil || count != 0 {
		t.Fatal("requires entirely empty isolated schema")
	}
	for range 2 {
		if err = migration.Run(context.Background(), dsn, migrations.Files); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Create(&projectRecord{ID: "p", TenantID: "t", Name: "QA", Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	snapshot := expenseSpendFixture()
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := repo.ApplyExpenseSpend(context.Background(), snapshot)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"pm_expense_inbox", "pm_expense_snapshot", "pm_expense_effect", "pm_expense_allocation", "pm_expense_projection"} {
		if err = db.Table(table).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}
