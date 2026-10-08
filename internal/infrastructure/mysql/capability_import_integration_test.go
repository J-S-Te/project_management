package mysql

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestCapabilityImportCannotRebindConcurrentNumber(t *testing.T) {
	dsn := os.Getenv("PM_CAPABILITY_IMPORT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires dedicated PM_CAPABILITY_IMPORT_TEST_DSN")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "pm_qualification_test") {
		t.Fatal("requires a dedicated pm_qualification_test database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&capabilityRecord{}); err != nil {
		t.Fatal(err)
	}
	ensureCapabilityImportProductionUniqueIndex(t, db)
	const tenant = "QUALIFICATION-IMPORT-TEST"
	t.Cleanup(func() { db.Where("tenant_id=?", tenant).Delete(&capabilityRecord{}) })
	repo := NewRepository(db)
	ctx := context.Background()
	first := domain.Capability{TenantID: tenant, ResourceType: "PERSON", ResourceID: "P-IMPORT-EXISTING", ResourceName: "甲", UserID: "user-a", Codes: []string{"QUAL-1"}, Status: "ACTIVE"}
	saved, err := repo.UpsertCapability(ctx, first, "admin")
	if err != nil {
		t.Fatal(err)
	}
	first.Codes = []string{"QUAL-2"}
	updated, err := repo.UpsertCapability(ctx, first, "admin")
	if err != nil || updated.ID != saved.ID {
		t.Fatalf("existing identity update failed: %+v, %v", updated, err)
	}
	first.UserID = "user-b"
	if _, err = repo.UpsertCapability(ctx, first, "admin"); err == nil {
		t.Fatal("rebound another person's qualification")
	}
	var wg sync.WaitGroup
	type outcome struct {
		user string
		err  error
	}
	outcomes := make(chan outcome, 2)
	for _, user := range []string{"user-c", "user-d"} {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			_, writeErr := repo.UpsertCapability(ctx, domain.Capability{TenantID: tenant, ResourceType: "PERSON", ResourceID: "P-IMPORT-RACE", ResourceName: user, UserID: user, Codes: []string{"QUAL-1"}, Status: "ACTIVE"}, "admin")
			outcomes <- outcome{user, writeErr}
		}(user)
	}
	wg.Wait()
	close(outcomes)
	winner := ""
	for result := range outcomes {
		if result.err == nil {
			if winner != "" {
				t.Fatal("both competing identities were allowed to write the same number")
			}
			winner = result.user
		}
	}
	if winner == "" {
		t.Fatal("neither concurrent insert succeeded")
	}
	var rows []capabilityRecord
	if err = db.Where("tenant_id=?", tenant).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected exactly two numbered records, got %d", len(rows))
	}
	for _, row := range rows {
		if row.ResourceID == "P-IMPORT-EXISTING" && row.UserID != "user-a" {
			t.Fatalf("owner overwritten: %+v", row)
		}
		if row.ResourceID == "P-IMPORT-RACE" && row.UserID != winner {
			t.Fatalf("unexpected race owner: %+v", row)
		}
	}
}

// AutoMigrate only sees ORM tags, while the production uniqueness constraint
// comes from migrations/000002_delivery_execution.sql. Reproduce and assert it
// explicitly so concurrency tests exercise the actual deployed schema.
func ensureCapabilityImportProductionUniqueIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	const index = "uq_pm_capability_resource"
	if !db.Migrator().HasIndex(&capabilityRecord{}, index) {
		if err := db.Exec("CREATE UNIQUE INDEX uq_pm_capability_resource ON pm_capability (tenant_id, resource_type, resource_id)").Error; err != nil {
			t.Fatal(err)
		}
	}
	type indexColumn struct {
		ColumnName string
		NonUnique  int
		SeqInIndex int
	}
	var columns []indexColumn
	if err := db.Raw("SELECT COLUMN_NAME, NON_UNIQUE, SEQ_IN_INDEX FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pm_capability' AND INDEX_NAME = ? ORDER BY SEQ_IN_INDEX", index).Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if len(columns) != 3 {
		t.Fatalf("production capability unique index missing or malformed: %+v", columns)
	}
	for i, name := range []string{"tenant_id", "resource_type", "resource_id"} {
		if columns[i].ColumnName != name || columns[i].NonUnique != 0 || columns[i].SeqInIndex != i+1 {
			t.Fatalf("production capability unique index differs: %+v", columns)
		}
	}
}
