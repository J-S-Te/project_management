package mysql

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const penetrationIntegrationTenant = "PM-PENETRATION-TEST-TENANT"

func TestEmbeddedPenetrationLifecycleAgainstRealDatabase(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run the embedded penetration lifecycle integration check")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	cleanup := func() {
		db.WithContext(ctx).Exec("DELETE FROM pm_notification_outbox WHERE tenant_id=?", penetrationIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_penetration_work_package_event WHERE tenant_id=?", penetrationIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_report_revision WHERE tenant_id=?", penetrationIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_penetration_work_package WHERE tenant_id=?", penetrationIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_delivery_event WHERE tenant_id=?", penetrationIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_service_item WHERE tenant_id=?", penetrationIntegrationTenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_project WHERE tenant_id=?", penetrationIntegrationTenant)
	}
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().UTC().Truncate(time.Millisecond)
	project := projectRecord{
		ID: "PJ-PEN-1", TenantID: penetrationIntegrationTenant, Name: "等保内嵌渗透测试", Customer: "测试客户",
		Contract: "HT-PEN-1", ContractVersion: "v1", SupplementStatus: "NONE", Services: 1, Status: "实施中",
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	item := serviceItemRecord{
		ID: "SI-PEN-1", TenantID: penetrationIntegrationTenant, ProjectID: project.ID, SourceServiceID: "S-1",
		Category: "等级保护测评", Requirement: "等保三级", TestMode: "STANDARD", ProjectManagerID: "pm-1",
		Status: "实施中", ReportStatus: "NONE", ConflictStatus: "PASSED", TechReviewStatus: "NONE",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err = db.WithContext(ctx).Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err = db.WithContext(ctx).Create(&item).Error; err != nil {
		t.Fatalf("seed service item: %v", err)
	}
	repository := NewRepository(db)
	domainItem := serviceFromRecord(item)
	workPackage, err := repository.EnsurePenetrationWorkPackage(ctx, penetrationIntegrationTenant, domainItem, "pm-1", "ensure-1", now)
	if err != nil {
		t.Fatalf("ensure package: %v", err)
	}

	apply := func(actor, eventType string, payload map[string]any) error {
		return repository.ApplyDeliveryEvent(ctx, domain.DeliveryEvent{
			ID: ulid.Make().String(), TenantID: penetrationIntegrationTenant, ProjectID: project.ID,
			ServiceItemID: item.ID, Type: eventType, ActorUserID: actor, Payload: payload, CreatedAt: time.Now().UTC(),
		})
	}
	if err = apply("pm-1", application.EventFieldCompleted, map[string]any{}); err == nil {
		t.Fatal("pending embedded decision must block parent field completion")
	}

	communicatedAt := now.Format(time.RFC3339)
	workPackage, err = repository.SavePenetrationDecision(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationDecisionInput{
		DecisionStatus: "REQUIRED", CustomerContact: "客户联系人", CommunicatedAt: communicatedAt,
		CommunicationSummary: "确认开展", ExpectedVersion: workPackage.Version, IdempotencyKey: "decision-1",
	}, "pm-1", now)
	if err != nil {
		t.Fatalf("record decision: %v", err)
	}
	workPackage, err = repository.SavePenetrationPlan(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationPlanInput{
		PlannedStart: now.Add(-time.Hour).Format(time.RFC3339), PlannedEnd: now.Add(time.Hour).Format(time.RFC3339), EngineerIDs: []string{"engineer-1"},
		AuthDocNo: "AUTH-1", AuthStart: now.Add(-2 * time.Hour).Format(time.RFC3339), AuthEnd: now.Add(2 * time.Hour).Format(time.RFC3339),
		AuthScope: "10.0.0.0/8", TestScope: "10.0.0.0/8", TestWindow: "00:00-06:00", EmergencyContact: "张三 13800000000",
		RollbackPlan: "停止测试并恢复快照", ExpectedVersion: workPackage.Version, IdempotencyKey: "plan-1",
	}, "pm-1", now)
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	workPackage, err = repository.AdvancePenetrationExecution(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationExecutionInput{
		Action: "START", ExpectedVersion: workPackage.Version, IdempotencyKey: "start-1",
	}, "engineer-1", now)
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	workPackage, err = repository.AdvancePenetrationExecution(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationExecutionInput{
		Action: "COMPLETE", ExpectedVersion: workPackage.Version, IdempotencyKey: "complete-1",
	}, "engineer-1", now)
	if err != nil {
		t.Fatalf("complete execution: %v", err)
	}
	if err = apply("pm-1", application.EventFieldCompleted, map[string]any{}); err != nil {
		t.Fatalf("completed package should allow parent field completion: %v", err)
	}

	workPackage, err = repository.AdvancePenetrationReport(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationReportStatusInput{
		Phase: "DRAFTING", ExpectedVersion: workPackage.Version, IdempotencyKey: "report-drafting-1",
	}, "pm-1", now)
	if err != nil {
		t.Fatalf("start report: %v", err)
	}
	workPackage, err = repository.RegisterPenetrationReportArtifact(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationReportArtifactInput{
		ReportArtifactInput: domain.ReportArtifactInput{FileID: "FILE-PEN-R0", FileName: "penetration-R0.pdf", MIME: "application/pdf", Size: 128, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		ExpectedVersion:     workPackage.Version, IdempotencyKey: "report-artifact-1",
	}, "pm-1", now)
	if err != nil {
		t.Fatalf("register report artifact: %v", err)
	}
	for _, step := range []struct{ phase, actor, key string }{
		{phase: "SUBMITTED", actor: "pm-1", key: "report-submit-1"},
		{phase: "APPROVED", actor: "quality-1", key: "report-approve-1"},
	} {
		workPackage, err = repository.AdvancePenetrationReport(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationReportStatusInput{
			Phase: step.phase, ExpectedVersion: workPackage.Version, IdempotencyKey: step.key,
		}, step.actor, now)
		if err != nil {
			t.Fatalf("advance package report to %s: %v", step.phase, err)
		}
	}
	if err = apply("pm-1", application.EventReportStatusUpdated, map[string]any{"phase": "COMPILING"}); err != nil {
		t.Fatalf("start parent report: %v", err)
	}
	if err = repository.RegisterReportArtifact(ctx, penetrationIntegrationTenant, item.ID, 0, domain.ReportArtifactInput{
		FileID: "FILE-PARENT-R0", FileName: "parent-R0.pdf", MIME: "application/pdf", Size: 128,
		SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}, "pm-1", now); err != nil {
		t.Fatalf("register parent report artifact: %v", err)
	}
	if err = apply("quality-1", application.EventReportStatusUpdated, map[string]any{"phase": "REVIEWED"}); err != nil {
		t.Fatalf("parent should enter reviewed state: %v", err)
	}
	if err = apply("director-1", application.EventReportStatusUpdated, map[string]any{"phase": "ISSUED"}); err == nil {
		t.Fatal("parent report issue must wait for embedded report issue")
	}
	workPackage, err = repository.AdvancePenetrationReport(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationReportStatusInput{
		Phase: "ISSUED", ExpectedVersion: workPackage.Version, IdempotencyKey: "report-issue-1",
	}, "director-1", now)
	if err != nil {
		t.Fatalf("issue package report: %v", err)
	}
	if err = apply("director-1", application.EventReportStatusUpdated, map[string]any{"phase": "ISSUED"}); err != nil {
		t.Fatalf("issued package report should allow parent issue: %v", err)
	}
	if err = apply("director-1", application.EventReportStatusUpdated, map[string]any{"phase": "ARCHIVED"}); err == nil {
		t.Fatal("parent archive must wait for embedded report archive")
	}
	workPackage, err = repository.AdvancePenetrationReport(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationReportStatusInput{
		Phase: "ARCHIVED", ExpectedVersion: workPackage.Version, IdempotencyKey: "report-archive-1",
	}, "director-1", now)
	if err != nil {
		t.Fatalf("archive package report: %v", err)
	}
	if err = apply("director-1", application.EventReportStatusUpdated, map[string]any{"phase": "ARCHIVED"}); err != nil {
		t.Fatalf("archived package report should allow parent archive: %v", err)
	}

	var serviceCount, packageCount int64
	db.WithContext(ctx).Model(&serviceItemRecord{}).Where("tenant_id=?", penetrationIntegrationTenant).Count(&serviceCount)
	db.WithContext(ctx).Model(&penetrationWorkPackageRecord{}).Where("tenant_id=?", penetrationIntegrationTenant).Count(&packageCount)
	if serviceCount != 1 || packageCount != 1 {
		t.Fatalf("embedded package changed service count: services=%d packages=%d", serviceCount, packageCount)
	}
	_, err = repository.SavePenetrationDecision(ctx, penetrationIntegrationTenant, item.ID, domain.PenetrationDecisionInput{
		DecisionStatus: "REQUIRED", CustomerContact: "客户联系人", CommunicatedAt: communicatedAt,
		CommunicationSummary: "并发旧写入", ExpectedVersion: 1, IdempotencyKey: "stale-decision-1",
	}, "pm-1", now)
	if !errors.Is(err, application.ErrConflict) {
		t.Fatalf("stale package write error=%v, want conflict", err)
	}
}
