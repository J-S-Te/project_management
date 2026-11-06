package mysql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/migrations"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestNativeProjectConcurrentMySQL 验证真实迁移后同委托并发幂等以及空合同不互相冲突。
// 参数：t 为测试上下文；仅接受显式PM_NATIVE_PROJECT_TEST_DSN且库名以pm_native_test开头的空隔离库。
// 返回值：无；未配置隔离DSN时跳过且不尝试连接。
func TestNativeProjectConcurrentMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PM_NATIVE_PROJECT_TEST_DSN"))
	if dsn == "" {
		t.Skip("requires dedicated empty PM_NATIVE_PROJECT_TEST_DSN; no connection attempted")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "pm_native_test") {
		t.Fatal("requires dedicated pm_native_test schema")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal("open isolated database failed")
	}
	var tables int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&tables).Error; err != nil || tables != 0 {
		t.Fatal("requires empty isolated schema")
	}
	if err := migration.Run(context.Background(), dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	if err := migration.Run(context.Background(), dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "native-concurrency-test", AllowAll: true}
	results := make(chan domain.Project, 8)
	failures := make(chan error, 8)
	var workers sync.WaitGroup
	for index := range 8 {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			now := time.Now().UTC()
			project := domain.Project{ID: fmt.Sprintf("PJ-NATIVE-RACE-%d", index), TenantID: filter.TenantID, SourceKind: "NATIVE", SourceID: "concurrent-operation", ScopeVersion: 1,
				Name: "合成测试项目", Customer: "合成单位", CustomerID: "synthetic-unit", OwnerIdentityID: "synthetic-user", Status: domain.ProjectStatusPendingDecomposition, Version: 1, CreatedAt: now, UpdatedAt: now}
			item := domain.ServiceItem{ID: fmt.Sprintf("SI-NATIVE-RACE-%d", index), ProjectID: project.ID, TenantID: filter.TenantID, SourceServiceID: "NATIVE-001", Category: "等保测评", Site: "合成现场", Status: domain.ServiceItemStatusPendingConfirm}
			result, err := repo.CreateNativeProject(context.Background(), filter, project, []domain.ServiceItem{item}, "same-fingerprint")
			if err != nil {
				failures <- err
			} else {
				results <- result
			}
		}(index)
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	winner := ""
	for result := range results {
		if winner != "" && winner != result.ID {
			t.Fatalf("duplicate native projects %s/%s", winner, result.ID)
		}
		winner = result.ID
	}
	if winner == "" {
		t.Fatal("no project created")
	}
	var count int64
	db.Model(&projectRecord{}).Where("tenant_id=?", filter.TenantID).Count(&count)
	if count != 1 {
		t.Fatalf("concurrent project count=%d", count)
	}
	next := domain.Project{ID: "PJ-NATIVE-INDEPENDENT", TenantID: filter.TenantID, SourceKind: "NATIVE", SourceID: "second-operation", ScopeVersion: 1, CustomerID: "synthetic-unit", Customer: "合成单位", OwnerIdentityID: "synthetic-user", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if _, err := repo.CreateNativeProject(context.Background(), filter, next, []domain.ServiceItem{{ID: "SI-NATIVE-INDEPENDENT", TenantID: filter.TenantID, ProjectID: next.ID, SourceServiceID: "NATIVE-001", Site: "合成现场", Category: "等保测评", Status: domain.ServiceItemStatusPendingConfirm}}, "different-fingerprint"); err != nil {
		t.Fatalf("empty contracts collided: %v", err)
	}
}
