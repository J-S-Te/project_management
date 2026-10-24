package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestPreparationStaleVersionRejectedInsideTransactionOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PM_TEST_DSN"))
	if dsn == "" {
		t.Skip("PM_TEST_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tenant := fmt.Sprintf("qa-ver-%019d", time.Now().UnixNano())
	projectID := "PMV" + fmt.Sprint(time.Now().UnixNano())[:12]
	if err := db.Exec(`INSERT INTO pm_project (id, tenant_id, owner_org_id, name, customer, customer_id, contract, contract_version, supplement_status, services, owner_identity_id, manager_identity_id, status, created_at, updated_at, version) VALUES (?,?,?,'版本竞态','客户','c','HT','1','NONE',1,'o','m','实施中',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),1)`, projectID, tenant, "org-1").Error; err != nil {
		t.Fatal(err)
	}
	itemID := "PMW" + fmt.Sprint(time.Now().UnixNano())[:12]
	if err := db.Exec(`INSERT INTO pm_service_item (id,tenant_id,project_id,source_service_id,requirement,test_mode,status,report_status,conflict_status,planned_start,planned_end,created_at,updated_at,version) VALUES (?,?,?,'source','服务','STANDARD','待实施','NONE','UNCHECKED','2026-10-20 00:00:00','2026-10-22 00:00:00',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),1)`, itemID, tenant, projectID).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM pm_equipment_reservation WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_delivery_event WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_service_item WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_project WHERE tenant_id=?`, tenant).Error
	})
	// 模拟应用预检后、交付事务取得服务项行锁前
	// 发生的并发编辑。
	if err := db.Exec(`UPDATE pm_service_item SET version=2 WHERE tenant_id=? AND id=?`, tenant, itemID).Error; err != nil {
		t.Fatal(err)
	}
	event := domain.DeliveryEvent{ID: "EV-VER-" + fmt.Sprint(time.Now().UnixNano()), TenantID: tenant, ServiceItemID: itemID, Type: application.EventPreparationStarted, ActorUserID: "actor", CreatedAt: time.Now().UTC(), Payload: map[string]any{
		"expected_version": 1,
		"travel":           map[string]any{"mode": "NO_TRAVEL", "no_travel_reason": "本地"},
		"equipment":        []map[string]any{{"resource_type": "EQUIPMENT", "resource_id": "EQ-VER", "resource_name": "设备", "window_start": "2026-10-20", "window_end": "2026-10-22"}},
	}}
	if err := NewRepository(db).ApplyDeliveryEvent(ctx, event); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("stale version error=%v, want ErrConflict", err)
	}
	var reservations int64
	if err := db.Raw(`SELECT COUNT(*) FROM pm_equipment_reservation WHERE tenant_id=?`, tenant).Scan(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if reservations != 0 {
		t.Fatalf("stale write created %d reservations, want zero", reservations)
	}
}
