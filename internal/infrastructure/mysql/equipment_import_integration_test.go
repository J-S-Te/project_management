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

func TestEquipmentImportCreationNeverOverwritesConcurrentNumber(t *testing.T) {
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
	const tenant = "EQUIPMENT-IMPORT-TEST"
	t.Cleanup(func() { db.Where("tenant_id=?", tenant).Delete(&capabilityRecord{}) })
	repo := NewRepository(db)
	ctx := context.Background()
	var wg sync.WaitGroup
	type outcome struct {
		name string
		err  error
	}
	outcomes := make(chan outcome, 2)
	for _, name := range []string{"扫描设备甲", "扫描设备乙"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := repo.CreateEquipmentForImport(ctx, domain.Capability{TenantID: tenant, ResourceType: "EQUIPMENT", ResourceID: "EQ-IMPORT-RACE", ResourceName: name, Codes: []string{"C1"}, Status: "ACTIVE", UsageScope: domain.EquipmentUsageCompanyOnly}, "admin")
			outcomes <- outcome{name, err}
		}(name)
	}
	wg.Wait()
	close(outcomes)
	success := 0
	winner := ""
	for result := range outcomes {
		if result.err == nil {
			success++
			winner = result.name
		}
	}
	if success != 1 {
		t.Fatalf("expected one successful insert, got %d", success)
	}
	items, err := repo.ListCapabilities(ctx, tenant, "EQUIPMENT")
	if err != nil || len(items) != 1 || items[0].ResourceName != winner || items[0].UsageScope != domain.EquipmentUsageCompanyOnly {
		t.Fatalf("equipment overwritten: %+v %v", items, err)
	}
	if _, err = repo.CreateEquipmentForImport(ctx, domain.Capability{TenantID: tenant, ResourceType: "EQUIPMENT", ResourceID: "EQ-IMPORT-RACE", ResourceName: "覆盖设备", Codes: []string{"C2"}}, "admin"); err == nil {
		t.Fatal("duplicate insert accepted")
	}
	items, err = repo.ListCapabilities(ctx, tenant, "EQUIPMENT")
	if err != nil || items[0].ResourceName != winner {
		t.Fatalf("duplicate changed winner: %+v %v", items, err)
	}
}
