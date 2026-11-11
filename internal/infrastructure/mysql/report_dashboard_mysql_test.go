package mysql

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/migrations"
	"github.com/oklog/ulid/v2"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// reportDashboardMySQL 打开显式文件指定的回环隔离测试库并执行真实迁移。
// 参数：t为测试上下文；只读取PM_REPORT_DASHBOARD_TEST_DSN_FILE。
// 返回值：隔离GORM连接；未配置时跳过，边界非法时失败且不连接。
func reportDashboardMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	path := os.Getenv("PM_REPORT_DASHBOARD_TEST_DSN_FILE")
	if path == "" {
		t.Skip("PM_REPORT_DASHBOARD_TEST_DSN_FILE required; no business database accessed")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private regular DSN file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read test DSN failed")
	}
	dsn := strings.TrimSpace(string(raw))
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || host != "127.0.0.1" || !strings.HasPrefix(cfg.DBName, "pm_test_") {
		t.Fatal("explicit loopback pm_test_* database required")
	}
	if err = migration.Run(t.Context(), dsn, migrations.Files); err != nil {
		t.Fatal("isolated migrations failed")
	}
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated test database failed")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

// TestReportDashboardMySQL 验证实际MySQL版本聚合及零样本、奇偶P50。
// 参数：t为测试上下文，连接仅来自显式隔离测试DSN文件。
// 返回值：无，主体漏计、有效性分类或真实周期错误时失败。
func TestReportDashboardMySQL(t *testing.T) {
	db := reportDashboardMySQL(t)
	tenant := "dashboard-" + ulid.Make().String()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	repo := NewRepository(db)
	empty, err := repo.ReadReportDashboard(context.Background(), tenant)
	if err != nil || !empty.Available || empty.Metrics.CycleP50Days != nil || empty.SourceUpdatedAt != "" {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	for i, entry := range []struct {
		valid, status, subject string
		days                   int
		complete               bool
	}{
		{"ACTIVE", "ISSUED", "SERVICE_ITEM", 2, true},
		{"ACTIVE", "ARCHIVED", "PENETRATION_WORK_PACKAGE", 4, true},
		{"VOID", "ISSUED", "SERVICE_ITEM", 100, true},
		{"SUPERSEDED", "ISSUED", "SERVICE_ITEM", 200, true},
		{"ACTIVE", "COMPILING", "SERVICE_ITEM", 0, false},
		{"ACTIVE", "ISSUED", "SERVICE_ITEM", -1, true},
	} {
		id := ulid.Make().String()
		var issued *time.Time
		if entry.complete {
			value := at.AddDate(0, 0, entry.days)
			issued = &value
		}
		row := reportRevisionRecord{TenantID: tenant, ServiceItemID: id, SubjectID: id, SubjectType: entry.subject, Revision: uint64(i), Status: entry.status, ValidityStatus: entry.valid, PreparedAt: &at, IssuedAt: issued, CreatedAt: at, UpdatedAt: at}
		if err = db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.ReadReportDashboard(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metrics.ActiveReports != 4 || got.Metrics.VoidReports != 1 || got.Metrics.CycleSamples != 2 || got.Metrics.CycleP50Days == nil || *got.Metrics.CycleP50Days != 3 || got.Metrics.ReviewedReports != 3 || got.SourceUpdatedAt == "" {
		t.Fatalf("metrics=%+v source=%s", got.Metrics, got.SourceUpdatedAt)
	}
	issued := at.AddDate(0, 0, 9)
	id := ulid.Make().String()
	if err = db.Create(&reportRevisionRecord{TenantID: tenant, ServiceItemID: id, SubjectID: id, SubjectType: "SERVICE_ITEM", Status: "ISSUED", ValidityStatus: "ACTIVE", PreparedAt: &at, IssuedAt: &issued, CreatedAt: at, UpdatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	odd, err := repo.ReadReportDashboard(context.Background(), tenant)
	if err != nil || odd.Metrics.CycleSamples != 3 || odd.Metrics.CycleP50Days == nil || *odd.Metrics.CycleP50Days != 4 {
		t.Fatalf("odd=%+v err=%v", odd, err)
	}
	private, err := repo.ReadReportDashboard(context.Background(), tenant+"-other")
	if err != nil || private.Metrics.ActiveReports != 0 {
		t.Fatalf("cross-tenant=%+v err=%v", private, err)
	}
}
