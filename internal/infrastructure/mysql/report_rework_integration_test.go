package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"os"
)

func TestReportReturnToFieldReopensRevisionAndCompletesOnMySQL(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("PM_TEST_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tenant := "PM-REG-REWORK-20261018"
	projectID, itemID := "PJ-REG-REWORK-1", "SI-REG-REWORK-1"
	cleanup := func() {
		db.WithContext(ctx).Exec("DELETE FROM pm_delivery_event WHERE tenant_id=?", tenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_report_revision WHERE tenant_id=?", tenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_service_item WHERE tenant_id=?", tenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_project WHERE tenant_id=?", tenant)
	}
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.Create(&projectRecord{ID: projectID, TenantID: tenant, Name: "报告返工回归", Customer: "客户", Contract: "C-1", ContractVersion: "v1", SupplementStatus: "NONE", Services: 1, Status: "实施中", CreatedAt: now, UpdatedAt: now, Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&serviceItemRecord{ID: itemID, TenantID: tenant, ProjectID: projectID, SourceServiceID: "S-1", TestMode: "STANDARD", Status: "现场实施完成", ReportStatus: "REVIEWED", ReportRevision: 0, ConflictStatus: "PASSED", TechReviewStatus: "NONE", Version: 5, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&reportRevisionRecord{TenantID: tenant, ServiceItemID: itemID, SubjectType: "SERVICE_ITEM", SubjectID: itemID, Revision: 0, Status: "REVIEWED", ValidityStatus: "ACTIVE", FileID: "FILE-R0", FileName: "R0.pdf", FileMIME: "application/pdf", FileSize: 10, FileSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PreparedBy: "pm", ReviewedBy: "qa", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	requestID := ulid.Make().String()
	if err := repo.ApplyDeliveryEvent(ctx, domain.DeliveryEvent{ID: requestID, TenantID: tenant, ProjectID: projectID, ServiceItemID: itemID, Type: application.EventRollbackRequested, ActorUserID: "pm", CreatedAt: now.Add(time.Second), Payload: map[string]any{"kind": "REPORT_TO_FIELD", "reason": "客户名称需修订", "expected_version": uint64(5), "requester_user_id": "pm"}}); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := repo.ApplyDeliveryEvent(ctx, domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: tenant, ProjectID: projectID, ServiceItemID: itemID, Type: application.EventRollbackApproved, ActorUserID: "director", CreatedAt: now.Add(2 * time.Second), Payload: map[string]any{"request_id": requestID, "kind": "REPORT_TO_FIELD", "reason": "客户名称需修订", "expected_version": uint64(5), "rollback_requester_id": "pm"}}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	var item serviceItemRecord
	if err := db.Where("tenant_id=? AND id=?", tenant, itemID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Status != "实施中" || item.ReportStatus != "NONE" || item.ReportRevision != 1 {
		t.Fatalf("after return status=%s report=%s revision=%d", item.Status, item.ReportStatus, item.ReportRevision)
	}
	var revisions []reportRevisionRecord
	if err := db.Where("tenant_id=? AND service_item_id=?", tenant, itemID).Order("revision").Find(&revisions).Error; err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].ValidityStatus != "SUPERSEDED" || revisions[1].ValidityStatus != "ACTIVE" || revisions[1].Revision != 1 {
		t.Fatalf("revisions=%+v", revisions)
	}
	// 在新修订上重新完成现场执行。
	if err := repo.ApplyDeliveryEvent(ctx, domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: tenant, ProjectID: projectID, ServiceItemID: itemID, Type: application.EventFieldCompleted, ActorUserID: "pm", CreatedAt: now.Add(3 * time.Second), Payload: map[string]any{"expected_version": item.Version, "incomplete_reason": "返工后重新确认"}}); err != nil {
		t.Fatalf("field complete after rework: %v", err)
	}
	if err := db.Where("tenant_id=? AND id=?", tenant, itemID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Status != "现场实施完成" || item.ReportStatus != "NONE" {
		t.Fatalf("after recomplete status=%s report=%s", item.Status, item.ReportStatus)
	}
}
