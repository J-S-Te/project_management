package mysql

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 撤销团队或执行指派不得清除服务项的
// 必需能力基线。否则后续指派可能仅凭
// 客户端提供的编码绕过资质门禁。
func TestAssignmentRevokeKeepsQualificationBaselineOnMySQL(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("PM_TEST_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tenant := "PM-REG-QUAL-20261018"
	projectID, itemID := "PJ-REG-QUAL-1", "SI-REG-QUAL-1"
	cleanup := func() {
		_ = db.Exec(`DELETE FROM pm_equipment_reservation WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_delivery_event WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_service_item WHERE tenant_id=?`, tenant).Error
		_ = db.Exec(`DELETE FROM pm_project WHERE tenant_id=?`, tenant).Error
	}
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.Create(&projectRecord{ID: projectID, TenantID: tenant, Name: "资质基线回归", Customer: "客户", Contract: "C-1", ContractVersion: "v1", SupplementStatus: "NONE", Services: 1, Status: "实施中", CreatedAt: now, UpdatedAt: now, Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	codes, _ := json.Marshal([]string{"PENTEST-BASE", "REPORT-BASE"})
	if err := db.Create(&serviceItemRecord{ID: itemID, TenantID: tenant, ProjectID: projectID, SourceServiceID: "S-1", TestMode: "STANDARD", Status: "待分配", ReportStatus: "NONE", ConflictStatus: "UNCHECKED", TeamLeadID: "lead-1", ProjectManagerID: "pm-1", RequiredCodes: codes, Version: 3, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	if err := repo.ApplyDeliveryEvent(ctx, domain.DeliveryEvent{ID: "EV-QUAL-REVOKE", TenantID: tenant, ProjectID: projectID, ServiceItemID: itemID, Type: application.EventExecutionAssignmentRevoked, ActorUserID: "director", CreatedAt: now.Add(time.Second), Payload: map[string]any{"expected_version": uint64(3), "reason": "轮换", "revoked_user_ids": []string{"pm-1"}}}); err != nil {
		t.Fatalf("revoke execution assignment: %v", err)
	}
	var item serviceItemRecord
	if err := db.Where("tenant_id=? AND id=?", tenant, itemID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	var stored []string
	if err := json.Unmarshal(item.RequiredCodes, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0] != "PENTEST-BASE" || stored[1] != "REPORT-BASE" {
		t.Fatalf("required_codes after revoke=%v, want unchanged baseline", stored)
	}
	if item.ConflictStatus != "UNCHECKED" {
		t.Fatalf("conflict_status=%s, want UNCHECKED after revoke", item.ConflictStatus)
	}
}
