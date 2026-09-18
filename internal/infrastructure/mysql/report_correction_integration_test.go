package mysql

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const reportCorrectionIntegrationTenant = "PM-REG-REPORT-20260918"

// TestReportCorrectionAgainstRealDatabase 验证更正审批在真实 MySQL 事务中的原子性：
// 两个审批并发处理同一申请时只能一个成功，旧版本作废、新版本生成且事件只落一份。
func TestReportCorrectionAgainstRealDatabase(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the report-correction integration check")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	cleanup := func() {
		db.WithContext(ctx).Exec("DELETE FROM pm_delivery_event WHERE tenant_id = ?", reportCorrectionIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_report_revision WHERE tenant_id = ?", reportCorrectionIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_service_item WHERE tenant_id = ?", reportCorrectionIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_project WHERE tenant_id = ?", reportCorrectionIntegrationTenant)
	}
	cleanup()
	t.Cleanup(cleanup)

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.WithContext(ctx).Create(&projectRecord{
		ID: "PJ-REG-REPORT-1", TenantID: reportCorrectionIntegrationTenant,
		Name: "报告更正事务回归", Customer: "回归客户", Contract: "C-REG-REPORT-1",
		ContractVersion: "v1", SupplementStatus: "NONE", Services: 1, Status: "已完成",
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.WithContext(ctx).Create(&serviceItemRecord{
		ID: "SI-REG-REPORT-1", TenantID: reportCorrectionIntegrationTenant, ProjectID: "PJ-REG-REPORT-1",
		SourceServiceID: "S-1", TestMode: "STANDARD", Status: "现场实施完成",
		ReportStatus: "ARCHIVED", ReportRevision: 0, ConflictStatus: "PASSED", TechReviewStatus: "NONE",
		Version: 5, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed service item: %v", err)
	}
	if err := db.WithContext(ctx).Create(&reportRevisionRecord{
		TenantID: reportCorrectionIntegrationTenant, ServiceItemID: "SI-REG-REPORT-1", Revision: 0,
		Status: "ARCHIVED", ValidityStatus: "ACTIVE", FileID: "FILE-R0", FileName: "R0.pdf",
		FileMIME: "application/pdf", FileSize: 128, FileSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PreparedBy: "project-manager-1", ReviewedBy: "quality-manager-1", IssuedBy: "technical-director-0",
		ArchivedBy: "technical-director-0", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed report revision: %v", err)
	}

	repository := NewRepository(db)
	request := domain.DeliveryEvent{
		ID: ulid.Make().String(), TenantID: reportCorrectionIntegrationTenant,
		ProjectID: "PJ-REG-REPORT-1", ServiceItemID: "SI-REG-REPORT-1",
		Type: application.EventReportCorrectionRequested, ActorUserID: "project-manager-1", CreatedAt: now.Add(time.Second),
		Payload: map[string]any{"reason": "修正客户名称", "old_revision": uint64(0), "requester_user_id": "project-manager-1"},
	}
	if err := repository.ApplyDeliveryEvent(ctx, request); err != nil {
		t.Fatalf("persist correction request: %v", err)
	}

	start := make(chan struct{})
	errorsByWorker := make(chan error, 2)
	var workers sync.WaitGroup
	actors := []string{"technical-director-1", "technical-director-2"}
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			event := domain.DeliveryEvent{
				ID: ulid.Make().String(), TenantID: reportCorrectionIntegrationTenant,
				ProjectID: "PJ-REG-REPORT-1", ServiceItemID: "SI-REG-REPORT-1",
				Type: application.EventReportCorrectionApproved, ActorUserID: actors[index], CreatedAt: now.Add(2 * time.Second),
				Payload: map[string]any{
					"request_id": request.ID, "reason": "修正客户名称", "old_revision": uint64(0),
					"report_correction_requester_id": "project-manager-1", "expected_version": uint64(5),
				},
			}
			errorsByWorker <- repository.ApplyDeliveryEvent(ctx, event)
		}(index)
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)

	succeeded, conflicted := 0, 0
	for applyErr := range errorsByWorker {
		switch {
		case applyErr == nil:
			succeeded++
		case errors.Is(applyErr, application.ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent approval error: %v", applyErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent approvals: success=%d conflict=%d, want 1/1", succeeded, conflicted)
	}

	var item serviceItemRecord
	if err := db.WithContext(ctx).Where("tenant_id=? AND id=?", reportCorrectionIntegrationTenant, "SI-REG-REPORT-1").Take(&item).Error; err != nil {
		t.Fatalf("load corrected item: %v", err)
	}
	if item.ReportStatus != "NONE" || item.ReportRevision != 1 || item.Version != 6 {
		t.Fatalf("corrected item status=%s revision=%d version=%d, want NONE/1/6", item.ReportStatus, item.ReportRevision, item.Version)
	}

	var revisions []reportRevisionRecord
	if err := db.WithContext(ctx).Where("tenant_id=? AND service_item_id=?", reportCorrectionIntegrationTenant, item.ID).Order("revision").Find(&revisions).Error; err != nil {
		t.Fatalf("load report revisions: %v", err)
	}
	if len(revisions) != 2 || revisions[0].ValidityStatus != "VOID" || revisions[1].ValidityStatus != "ACTIVE" || revisions[1].Revision != 1 || revisions[1].CorrectionRequestID != request.ID || revisions[1].CorrectionReason != "修正客户名称" {
		t.Fatalf("report revisions=%+v", revisions)
	}

	var approvalCount int64
	if err := db.WithContext(ctx).Model(&deliveryEventRecord{}).
		Where("tenant_id=? AND event_type=?", reportCorrectionIntegrationTenant, application.EventReportCorrectionApproved).
		Count(&approvalCount).Error; err != nil {
		t.Fatalf("count approval events: %v", err)
	}
	if approvalCount != 1 {
		t.Fatalf("approval events=%d, want 1", approvalCount)
	}
}
