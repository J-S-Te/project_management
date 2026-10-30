package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 此处使用 SQLite 验证事务回滚及持久化业务不变量；
// InnoDB 行锁调度由另行显式启用的 MySQL 测试覆盖。
// equipmentAtomicityDB 创建本地事务夹具并纳入导入关系查询所需表。
// 参数：t 为测试上下文。
// 返回值：SQLite隔离连接。
func equipmentAtomicityDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&capabilityRecord{}, &equipmentReservationRecord{}, &implPlanRecord{}, &serviceItemRecord{}, &projectRecord{}, &contractImportBindingRecord{}, &contractIdentityGuardRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX uq_pm_capability_resource ON pm_capability (tenant_id, resource_type, resource_id)").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func atomicityEquipment(t *testing.T, db *gorm.DB, tenant, id string) capabilityRecord {
	t.Helper()
	row := capabilityRecord{ID: ulid.Make().String(), TenantID: tenant, ResourceType: "EQUIPMENT", ResourceID: id, ResourceName: id, Status: "ACTIVE", CapabilityCodes: []byte(`[]`), UsageScope: domain.EquipmentUsageAny}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func atomicityPlan(t *testing.T, db *gorm.DB, tenant, status string, equipment []domain.PlanResource) serviceItemRecord {
	t.Helper()
	start, _ := time.Parse("2006-01-02", "2026-10-20")
	end := start.AddDate(0, 0, 2)
	item := serviceItemRecord{ID: "item-" + tenant, TenantID: tenant, ProjectID: "project-" + tenant, Status: status, PlannedStart: &start, PlannedEnd: &end, Version: 1}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(equipment)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&implPlanRecord{ID: ulid.Make().String(), TenantID: tenant, ServiceItemID: item.ID, PlannedStart: &start, PlannedEnd: &end, Equipment: raw}).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func TestDeleteEquipmentChecksHistoricalActiveSnapshotInTransaction(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "EQ")
	atomicityPlan(t, db, "t1", domain.ProjectStatusPreparing, []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ"}})
	// 没有每日记录：这是迁移 000031 前保存的有效计划。
	if err := NewRepository(db).DeleteEquipment(context.Background(), "t1", "EQ"); !errors.Is(err, application.ErrResourceConflict) {
		t.Fatalf("active historical plan delete error=%v, want conflict", err)
	}
	var count int64
	if err := db.Model(&capabilityRecord{}).Where("tenant_id=?", "t1").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("master count=%d err=%v", count, err)
	}
}

func TestPreparationRejectsStaleEquipmentAndRollsBack(t *testing.T) {
	for _, state := range []string{"deleted", "disabled", "expired", "future-validity", "company-only", "other-tenant"} {
		t.Run(state, func(t *testing.T) {
			db := equipmentAtomicityDB(t)
			atomicityEquipment(t, db, "t1", "OLD")
			device := atomicityEquipment(t, db, "t1", "NEW")
			var changeErr error
			switch state {
			case "deleted":
				changeErr = db.Delete(&device).Error
			case "disabled":
				changeErr = db.Model(&device).Update("status", "DISABLED").Error
			case "expired":
				changeErr = db.Model(&device).Update("valid_until", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)).Error
			case "future-validity":
				changeErr = db.Model(&device).Update("valid_from", time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)).Error
			case "company-only":
				changeErr = db.Model(&device).Update("usage_scope", domain.EquipmentUsageCompanyOnly).Error
			case "other-tenant":
				changeErr = db.Model(&device).Update("tenant_id", "t2").Error
			}
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			old := []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "OLD"}}
			item := atomicityPlan(t, db, "t1", domain.ProjectStatusPreparing, old)
			row := equipmentReservationRecord{TenantID: "t1", ResourceID: "OLD", UseDate: *item.PlannedStart, ServiceItemID: item.ID, WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			event := domain.DeliveryEvent{Type: application.EventPreparationStarted, CreatedAt: time.Now().UTC(), Payload: map[string]any{"equipment": []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "NEW"}}}}
			err := db.Transaction(func(tx *gorm.DB) error { return applyItemEvent(tx, &item, event) })
			if !errors.Is(err, application.ErrConflict) {
				t.Fatalf("stale %s preparation error=%v, want conflict", state, err)
			}
			var reservations []equipmentReservationRecord
			if err := db.Find(&reservations).Error; err != nil || len(reservations) != 1 || reservations[0].ResourceID != "OLD" {
				t.Fatalf("old reservation lost: %+v %v", reservations, err)
			}
			var plan implPlanRecord
			if err := db.Where("tenant_id=?", "t1").Take(&plan).Error; err != nil {
				t.Fatal(err)
			}
			var resources []domain.PlanResource
			if err := json.Unmarshal(plan.Equipment, &resources); err != nil || len(resources) != 1 || resources[0].ResourceID != "OLD" {
				t.Fatalf("old snapshot overwritten: %s %v", plan.Equipment, err)
			}
		})
	}
}

func TestEquipmentPreparationCommitAndTenantIsolation(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "EQ")
	atomicityEquipment(t, db, "t2", "EQ")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusPendingExecution, nil)
	event := domain.DeliveryEvent{Type: application.EventPreparationStarted, CreatedAt: time.Now().UTC(), Payload: map[string]any{"equipment": []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ"}}}}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyItemEvent(tx, &item, event) }); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&equipmentReservationRecord{}).Where("tenant_id=?", "t1").Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("committed reservation days=%d err=%v", count, err)
	}
	if err := NewRepository(db).DeleteEquipment(context.Background(), "t2", "EQ"); err != nil {
		t.Fatal(err)
	}
	if err := NewRepository(db).DeleteEquipment(context.Background(), "t1", "EQ"); !errors.Is(err, application.ErrResourceConflict) {
		t.Fatalf("active tenant master delete=%v", err)
	}
}

func TestEquipmentPreparationSnapshotWriteFailureRollsBackReplacement(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "OLD")
	atomicityEquipment(t, db, "t1", "NEW")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusPreparing, []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "OLD"}})
	if err := db.Create(&equipmentReservationRecord{TenantID: "t1", ResourceID: "OLD", UseDate: *item.PlannedStart, ServiceItemID: item.ID, WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_equipment_snapshot BEFORE UPDATE OF equipment ON pm_impl_plan BEGIN SELECT RAISE(ABORT, 'snapshot unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	event := domain.DeliveryEvent{Type: application.EventPreparationStarted, CreatedAt: time.Now().UTC(), Payload: map[string]any{"equipment": []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "NEW"}}}}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyItemEvent(tx, &item, event) }); err == nil {
		t.Fatal("snapshot failure was swallowed")
	}
	var rows []equipmentReservationRecord
	if err := db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].ResourceID != "OLD" {
		t.Fatalf("partially replaced index=%+v err=%v", rows, err)
	}
	var stored serviceItemRecord
	if err := db.Where("tenant_id=? AND id=?", "t1", item.ID).Take(&stored).Error; err != nil || stored.Version != 1 {
		t.Fatalf("failed replacement changed item version=%d err=%v", stored.Version, err)
	}
}

func TestDeleteEquipmentKeepsTerminatedPlanSnapshot(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "EQ")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusTerminated, []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ"}})
	if err := db.Create(&equipmentReservationRecord{TenantID: "t1", ResourceID: "EQ", UseDate: *item.PlannedStart, ServiceItemID: item.ID, WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd}).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewRepository(db).DeleteEquipment(context.Background(), "t1", "EQ"); err != nil {
		t.Fatal(err)
	}
	var plan implPlanRecord
	if err := db.Where("tenant_id=?", "t1").Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var resources []domain.PlanResource
	if err := json.Unmarshal(plan.Equipment, &resources); err != nil || len(resources) != 1 || resources[0].ResourceID != "EQ" {
		t.Fatalf("terminated history lost=%s err=%v", plan.Equipment, err)
	}
}

func TestPreparationMissingEquipmentPayloadCannotReleaseReservations(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "EQ")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusPreparing, []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ"}})
	if err := db.Create(&equipmentReservationRecord{TenantID: "t1", ResourceID: "EQ", UseDate: *item.PlannedStart, ServiceItemID: item.ID, WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd}).Error; err != nil {
		t.Fatal(err)
	}
	event := domain.DeliveryEvent{Type: application.EventPreparationStarted, CreatedAt: time.Now().UTC(), Payload: map[string]any{}}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyItemEvent(tx, &item, event) }); !errors.Is(err, application.ErrValidation) {
		t.Fatalf("missing equipment payload error=%v, want validation", err)
	}
	var count int64
	if err := db.Model(&equipmentReservationRecord{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("reservation count=%d err=%v", count, err)
	}
}

func TestEquipmentReturnReleasesReservationAndKeepsHistory(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "EQ")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusPreparing, []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ"}})
	if err := db.Create(&equipmentReservationRecord{TenantID: "t1", ResourceID: "EQ", UseDate: *item.PlannedStart, ServiceItemID: item.ID, WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd}).Error; err != nil {
		t.Fatal(err)
	}
	event := domain.DeliveryEvent{Type: application.EventEquipmentReturned, CreatedAt: time.Now().UTC(), Payload: map[string]any{"resource_id": "EQ"}}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyItemEvent(tx, &item, event) }); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&equipmentReservationRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("returned device still reserved: %d %v", count, err)
	}
	if err := NewRepository(db).DeleteEquipment(context.Background(), "t1", "EQ"); err != nil {
		t.Fatal(err)
	}
	var plan implPlanRecord
	if err := db.Where("tenant_id=?", "t1").Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var resources []domain.PlanResource
	if err := json.Unmarshal(plan.Equipment, &resources); err != nil || len(resources) != 1 || resources[0].ReturnedAt == "" {
		t.Fatalf("return history lost: %s %v", plan.Equipment, err)
	}
}

func TestDeleteEquipmentRejectsActiveReservationWithoutPlan(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "EQ")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusPreparing, nil)
	if err := db.Where("tenant_id=?", "t1").Delete(&implPlanRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&equipmentReservationRecord{TenantID: "t1", ResourceID: "EQ", UseDate: *item.PlannedStart, ServiceItemID: item.ID, WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd}).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewRepository(db).DeleteEquipment(context.Background(), "t1", "EQ"); !errors.Is(err, application.ErrResourceConflict) {
		t.Fatalf("active reservation with missing snapshot delete error=%v, want conflict", err)
	}
}

func TestPreparationWithoutPlanRollsBackReservation(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "EQ")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusPendingExecution, nil)
	if err := db.Where("tenant_id=?", "t1").Delete(&implPlanRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	event := domain.DeliveryEvent{Type: application.EventPreparationStarted, CreatedAt: time.Now().UTC(), Payload: map[string]any{"equipment": []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ"}}}}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyItemEvent(tx, &item, event) }); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("preparation without plan error=%v, want conflict", err)
	}
	var count int64
	if err := db.Model(&equipmentReservationRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("orphan reservations=%d err=%v", count, err)
	}
}

func TestEquipmentReturnCaseAliasUpdatesCanonicalSnapshot(t *testing.T) {
	db := equipmentAtomicityDB(t)
	// 此事务回归使用真实 SQLite NOCASE 列。MySQL 额外的
	// 重音等价规则由显式启用的 MySQL 用例验证。
	for _, table := range []string{"pm_capability", "pm_equipment_reservation"} {
		var schema string
		if err := db.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&schema).Error; err != nil {
			t.Fatal(err)
		}
		collated := strings.Replace(schema, "`resource_id` text", "`resource_id` text COLLATE NOCASE", 1)
		if collated == schema {
			t.Fatalf("missing resource_id text column in %s", table)
		}
		if err := db.Migrator().DropTable(table); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(collated).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("CREATE UNIQUE INDEX uq_pm_capability_resource ON pm_capability (tenant_id, resource_type, resource_id)").Error; err != nil {
		t.Fatal(err)
	}
	atomicityEquipment(t, db, "t1", "EQ-001")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusPreparing, []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001"}})
	if err := db.Create(&equipmentReservationRecord{TenantID: "t1", ResourceID: "EQ-001", UseDate: *item.PlannedStart, ServiceItemID: item.ID, WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd}).Error; err != nil {
		t.Fatal(err)
	}
	event := domain.DeliveryEvent{Type: application.EventEquipmentReturned, CreatedAt: time.Now().UTC(), Payload: map[string]any{"resource_id": "eq-001"}}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyItemEvent(tx, &item, event) }); err != nil {
		t.Fatal(err)
	}
	var plan implPlanRecord
	if err := db.Where("tenant_id=?", "t1").Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var resources []domain.PlanResource
	if err := json.Unmarshal(plan.Equipment, &resources); err != nil || len(resources) != 1 || resources[0].ResourceID != "EQ-001" || resources[0].ReturnedAt == "" {
		t.Fatalf("alias return did not mark canonical snapshot: %s err=%v", plan.Equipment, err)
	}
	var count int64
	if err := db.Model(&equipmentReservationRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("returned index count=%d err=%v", count, err)
	}
}

func TestEquipmentReturnUnknownSnapshotCannotReleaseReservation(t *testing.T) {
	db := equipmentAtomicityDB(t)
	atomicityEquipment(t, db, "t1", "EQ-001")
	atomicityEquipment(t, db, "t1", "EQ-OTHER")
	item := atomicityPlan(t, db, "t1", domain.ProjectStatusPreparing, []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001"}})
	for _, id := range []string{"EQ-001", "EQ-OTHER", "EQ-MISSING"} {
		if err := db.Create(&equipmentReservationRecord{TenantID: "t1", ResourceID: id, UseDate: *item.PlannedStart, ServiceItemID: item.ID, WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, unknown := range []string{"EQ-OTHER", "EQ-MISSING"} {
		if err := db.Where("tenant_id=? AND id=?", "t1", item.ID).Take(&item).Error; err != nil {
			t.Fatal(err)
		}
		event := domain.DeliveryEvent{Type: application.EventEquipmentReturned, CreatedAt: time.Now().UTC(), Payload: map[string]any{"resource_id": unknown}}
		if err := db.Transaction(func(tx *gorm.DB) error { return applyItemEvent(tx, &item, event) }); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&equipmentReservationRecord{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("unknown snapshot released reservation count=%d err=%v", count, err)
	}
}
