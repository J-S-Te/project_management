package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/migrations"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 两个服务项在重叠日期准备同一设备时，不能
// 同时预留成功。服务预检改善体验；该数据库级
// 测试证明每日唯一键是最终并发边界。
func TestConcurrentEquipmentPreparationOnlyOneSucceedsOnMySQL(t *testing.T) {
	db := equipmentConcurrencyMySQL(t)
	ctx := context.Background()
	tenant := fmt.Sprintf("qa-eqp-%019d", time.Now().UnixNano())
	projectID := "PMP" + fmt.Sprint(time.Now().UnixNano())[:12]
	if err := db.Exec(`INSERT INTO pm_project (id, tenant_id, owner_org_id, name, customer, customer_id, contract, contract_version, supplement_status, services, owner_identity_id, manager_identity_id, status, created_at, updated_at, version) VALUES (?,?,?,'并发设备','客户','c','HT','1','NONE',2,'o','m','实施中',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),1)`, projectID, tenant, "org-1").Error; err != nil {
		t.Fatal(err)
	}
	makeItem := func(suffix string) string {
		id := "PMI" + suffix + fmt.Sprint(time.Now().UnixNano())[:10]
		if err := db.Exec(`INSERT INTO pm_service_item (id,tenant_id,project_id,source_service_id,requirement,test_mode,status,report_status,conflict_status,planned_start,planned_end,created_at,updated_at) VALUES (?,?,?,?,?,'STANDARD','待实施','NONE','UNCHECKED',?,?,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))`, id, tenant, projectID, "source-"+suffix, "服务", "2026-10-20 00:00:00", "2026-10-22 00:00:00").Error; err != nil {
			t.Fatal(err)
		}
		createEquipmentConcurrencyPlan(t, db, tenant, id)
		return id
	}
	first, second := makeItem("a"), makeItem("b")
	atomicityEquipment(t, db, tenant, "EQ-RACE")
	atomicityEquipment(t, db, tenant, "EQ-RACE-B")
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM pm_equipment_reservation WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_delivery_event WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_impl_plan WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_capability WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_service_item WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_project WHERE tenant_id=?`, tenant).Error
	})
	makeEvent := func(id, itemID string) domain.DeliveryEvent {
		equipment := []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ-RACE", WindowStart: "2026-10-20", WindowEnd: "2026-10-22"}, {ResourceType: "EQUIPMENT", ResourceID: "EQ-RACE-B", WindowStart: "2026-10-20", WindowEnd: "2026-10-22"}}
		if itemID == second {
			equipment[0], equipment[1] = equipment[1], equipment[0]
		}
		return domain.DeliveryEvent{ID: id, TenantID: tenant, ServiceItemID: itemID, Type: application.EventPreparationStarted, ActorUserID: "actor", CreatedAt: time.Now().UTC(), Payload: map[string]any{
			"travel":    map[string]any{"mode": "NO_TRAVEL", "no_travel_reason": "本地"},
			"equipment": equipment,
		}}
	}
	repo := NewRepository(db)
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for index, itemID := range []string{first, second} {
		go func(index int, itemID string) {
			start.Wait()
			results <- repo.ApplyDeliveryEvent(ctx, makeEvent(fmt.Sprintf("EV-%d-%d", index, time.Now().UnixNano()), itemID))
		}(index, itemID)
	}
	start.Done()
	var success, conflict int
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, application.ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d, want exactly one success and one conflict", success, conflict)
	}
}

func equipmentConcurrencyMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PM_TEST_DSN"))
	if dsn == "" {
		t.Skip("PM_TEST_DSN is required (dedicated isolated test schema)")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || !strings.Contains(strings.ToLower(cfg.DBName), "test") {
		t.Fatal("PM_TEST_DSN must name a dedicated test schema")
	}
	if err := migration.Run(context.Background(), dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db
}

func createEquipmentConcurrencyPlan(t *testing.T, db *gorm.DB, tenant, itemID string) {
	t.Helper()
	start, _ := time.Parse("2006-01-02", "2026-10-20")
	end := start.AddDate(0, 0, 2)
	if err := db.Create(&implPlanRecord{ID: ulid.Make().String(), TenantID: tenant, ServiceItemID: itemID, PlannedStart: &start, PlannedEnd: &end, Equipment: []byte(`[]`)}).Error; err != nil {
		t.Fatal(err)
	}
}

type equipmentRaceContextKey struct{}

// 胜出事务取得设备主锁后、首次预留变更前暂停。
// 持锁时启动失败方，使两种
// 串行顺序均被确定性验证，不依赖偶然时序。
func TestEquipmentDeleteVersusPreparationOnMySQL(t *testing.T) {
	db := equipmentConcurrencyMySQL(t)
	for _, first := range []string{"prepare", "delete"} {
		t.Run(first+"-wins", func(t *testing.T) {
			tenant := ulid.Make().String()
			projectID, itemID := ulid.Make().String(), ulid.Make().String()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			t.Cleanup(func() {
				for _, table := range []string{"pm_notification_outbox", "pm_delivery_effects_outbox", "pm_delivery_event", "pm_equipment_reservation", "pm_impl_plan", "pm_service_item", "pm_project", "pm_capability"} {
					if err := db.Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant).Error; err != nil {
						t.Errorf("cleanup %s: %v", table, err)
					}
				}
			})
			if err := db.Exec(`INSERT INTO pm_project (id,tenant_id,owner_org_id,name,customer,customer_id,contract,contract_version,supplement_status,services,owner_identity_id,manager_identity_id,status,created_at,updated_at,version) VALUES (?,?,?,'原子设备','客户','c',?,'1','NONE',1,'o','m','待实施',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),1)`, projectID, tenant, "org", projectID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`INSERT INTO pm_service_item (id,tenant_id,project_id,source_service_id,requirement,test_mode,status,report_status,conflict_status,planned_start,planned_end,created_at,updated_at,version) VALUES (?,?,?,'source','服务','STANDARD','待实施','NONE','UNCHECKED','2026-10-20 00:00:00','2026-10-22 00:00:00',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),1)`, itemID, tenant, projectID).Error; err != nil {
				t.Fatal(err)
			}
			createEquipmentConcurrencyPlan(t, db, tenant, itemID)
			// 真实隔离夹具，不能替换生产主数据。
			device := capabilityRecord{ID: ulid.Make().String(), TenantID: tenant, ResourceType: "EQUIPMENT", ResourceID: "EQ-ATOMIC", ResourceName: "设备", Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny, CapabilityCodes: []byte(`[]`), UpdatedAt: time.Now().UTC()}
			if err := db.Create(&device).Error; err != nil {
				t.Fatal(err)
			}
			event := domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: tenant, ServiceItemID: itemID, Type: application.EventPreparationStarted, ActorUserID: "actor", CreatedAt: time.Now().UTC(), Payload: map[string]any{"equipment": []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: device.ResourceID}}}}
			locked, resume, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var gateOnce, queryOnce, resumeOnce sync.Once
			defer resumeOnce.Do(func() { close(resume) })
			name := "qa_equipment_barrier_" + first
			if err := db.Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "pm_equipment_reservation" && tx.Statement.Context.Value(equipmentRaceContextKey{}) == first {
					gateOnce.Do(func() {
						close(locked)
						select {
						case <-resume:
						case <-ctx.Done():
							tx.AddError(ctx.Err())
						}
					})
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Delete().Remove(name)
			if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "pm_capability" && tx.Statement.Context.Value(equipmentRaceContextKey{}) != first {
					queryOnce.Do(func() { close(attempted) })
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Query().Remove(name)
			repo := NewRepository(db)
			prepareResult, deleteResult := make(chan error, 1), make(chan error, 1)
			prepare := func() {
				prepareResult <- repo.ApplyDeliveryEvent(context.WithValue(ctx, equipmentRaceContextKey{}, "prepare"), event)
			}
			remove := func() {
				deleteResult <- repo.DeleteEquipment(context.WithValue(ctx, equipmentRaceContextKey{}, "delete"), tenant, device.ResourceID)
			}
			if first == "prepare" {
				go prepare()
			} else {
				go remove()
			}
			select {
			case <-locked:
			case <-ctx.Done():
				t.Fatal("winner failed to reach reservation barrier")
			}
			if first == "prepare" {
				go remove()
			} else {
				go prepare()
			}
			select {
			case <-attempted:
			case <-ctx.Done():
				t.Fatal("loser failed to attempt equipment lock")
			}
			loser := prepareResult
			if first == "prepare" {
				loser = deleteResult
			}
			select {
			case err := <-loser:
				t.Fatalf("loser escaped equipment lock: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			resumeOnce.Do(func() { close(resume) })
			var prepareErr, deleteErr error
			select {
			case prepareErr = <-prepareResult:
			case <-ctx.Done():
				t.Fatal("preparation did not complete")
			}
			select {
			case deleteErr = <-deleteResult:
			case <-ctx.Done():
				t.Fatal("deletion did not complete")
			}
			wantMaster, wantReservations, wantEvents := int64(1), int64(3), int64(1)
			if first == "prepare" {
				if prepareErr != nil || !errors.Is(deleteErr, application.ErrResourceConflict) {
					t.Fatalf("prepare=%v delete=%v", prepareErr, deleteErr)
				}
			} else {
				wantMaster, wantReservations, wantEvents = 0, 0, 0
				if deleteErr != nil || !errors.Is(prepareErr, application.ErrConflict) {
					t.Fatalf("delete=%v prepare=%v", deleteErr, prepareErr)
				}
			}
			for _, check := range []struct {
				model any
				want  int64
			}{{&capabilityRecord{}, wantMaster}, {&equipmentReservationRecord{}, wantReservations}, {&deliveryEventRecord{}, wantEvents}} {
				var count int64
				if err := db.Model(check.model).Where("tenant_id=?", tenant).Count(&count).Error; err != nil || count != check.want {
					t.Fatalf("persisted %T count=%d want=%d err=%v", check.model, count, check.want, err)
				}
			}
		})
	}
}

func TestEquipmentReturnCanonicalAliasesOnMySQL(t *testing.T) {
	db := equipmentConcurrencyMySQL(t)
	for _, alias := range []string{"éq-001", "eq-001"} {
		t.Run(alias, func(t *testing.T) {
			tenant := ulid.Make().String()
			projectID, itemID := ulid.Make().String(), ulid.Make().String()
			t.Cleanup(func() {
				for _, table := range []string{"pm_notification_outbox", "pm_delivery_effects_outbox", "pm_delivery_event", "pm_equipment_reservation", "pm_impl_plan", "pm_service_item", "pm_project", "pm_capability"} {
					if err := db.Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant).Error; err != nil {
						t.Errorf("cleanup %s: %v", table, err)
					}
				}
			})
			if err := db.Exec(`INSERT INTO pm_project (id,tenant_id,owner_org_id,name,customer,customer_id,contract,contract_version,supplement_status,services,owner_identity_id,manager_identity_id,status,created_at,updated_at,version) VALUES (?,?,?,'归还别名','客户','c',?,'1','NONE',1,'o','m','实施准备中',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),1)`, projectID, tenant, "org", projectID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`INSERT INTO pm_service_item (id,tenant_id,project_id,source_service_id,requirement,test_mode,status,report_status,conflict_status,planned_start,planned_end,created_at,updated_at,version) VALUES (?,?,?,'source','服务','STANDARD','实施准备中','NONE','UNCHECKED','2026-10-20 00:00:00','2026-10-22 00:00:00',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),1)`, itemID, tenant, projectID).Error; err != nil {
				t.Fatal(err)
			}
			createEquipmentConcurrencyPlan(t, db, tenant, itemID)
			atomicityEquipment(t, db, tenant, "ÉQ-001")
			atomicityEquipment(t, db, tenant, "EQ-OTHER")
			resources := []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "ÉQ-001"}}
			raw, err := json.Marshal(resources)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&implPlanRecord{}).Where("tenant_id=? AND service_item_id=?", tenant, itemID).Update("equipment", raw).Error; err != nil {
				t.Fatal(err)
			}
			day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
			for _, resourceID := range []string{"ÉQ-001", "EQ-OTHER"} {
				if err := db.Create(&equipmentReservationRecord{TenantID: tenant, ResourceID: resourceID, UseDate: day, ServiceItemID: itemID, WindowStart: day, WindowEnd: day}).Error; err != nil {
					t.Fatal(err)
				}
			}
			repo := NewRepository(db)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for _, resourceID := range []string{alias, "eq-other"} {
				event := domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: tenant, ServiceItemID: itemID, Type: application.EventEquipmentReturned, ActorUserID: "actor", CreatedAt: time.Now().UTC(), Payload: map[string]any{"resource_id": resourceID}}
				if err := repo.ApplyDeliveryEvent(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			var plan implPlanRecord
			if err := db.Where("tenant_id=? AND service_item_id=?", tenant, itemID).Take(&plan).Error; err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(plan.Equipment, &resources); err != nil || len(resources) != 1 || resources[0].ResourceID != "ÉQ-001" || resources[0].ReturnedAt == "" {
				t.Fatalf("canonical snapshot after alias return=%s err=%v", plan.Equipment, err)
			}
			var remaining []equipmentReservationRecord
			if err := db.Where("tenant_id=?", tenant).Find(&remaining).Error; err != nil || len(remaining) != 1 || remaining[0].ResourceID != "EQ-OTHER" {
				t.Fatalf("returned and unknown reservations diverged=%+v err=%v", remaining, err)
			}
		})
	}
}
