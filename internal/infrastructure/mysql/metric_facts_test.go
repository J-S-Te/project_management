package mysql

import (
	"context"
	"errors"
	"fmt"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
	"time"
)

// metricTestDB 创建仅供本用例使用的内存数据库。
// 参数：t 为测试生命周期。
// 返回值：独立仓储和数据库；初始化失败时终止测试。
func metricTestDB(t *testing.T) (*Repository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err = db.AutoMigrate(&reportRevisionRecord{}, &serviceItemRecord{}, &projectRecord{}, &penetrationWorkPackageRecord{}, &metricReviewRecord{}, &metricResourceMutexRecord{}, &metricCapacityRecord{}, &metricUsageRecord{}, &metricOperationRecord{}, &auditOutboxRecord{}, &capabilityRecord{}); err != nil {
		t.Fatal(err)
	}
	return NewRepository(db), db
}

// TestMetricNewReportCannotBypassRound 捕获新报告直接推进审核阶段绕过轮次的问题。
// 参数：t 为测试上下文。
// 返回值：无；绕过审核轮次时失败。
func TestMetricNewReportCannotBypassRound(t *testing.T) {
	_, db := metricTestDB(t)
	now := time.Now().UTC()
	item := serviceItemRecord{TenantID: "tenant", ID: "SI-1", ReportStatus: "COMPILING"}
	if err := ensureReportRevision(db, &item, now); err != nil {
		t.Fatal(err)
	}
	var revision reportRevisionRecord
	db.First(&revision)
	revision.PreparedBy = "author"
	revision.FileID = "file"
	revision.FileSHA256 = "sha"
	db.Save(&revision)
	err := advanceReportRevision(db, &revision, "REVIEWED", domain.DeliveryEvent{ActorUserID: "quality", CreatedAt: now})
	if err == nil {
		t.Fatal("new report bypassed real review round")
	}
}

// seedMetricProject 建立小型真实状态机测试所需的项目和编制版本。
// 参数：t/db 为测试数据库，subject 为主报告或专项。
// 返回值：可信租户范围和当前父项。
func seedMetricProject(t *testing.T, db *gorm.DB, subject string) (platform.ScopeFilter, serviceItemRecord) {
	t.Helper()
	now := time.Now().UTC()
	project := projectRecord{ID: "PJ-1", TenantID: "tenant", OwnerOrgID: "org-1"}
	item := serviceItemRecord{ID: "SI-1", TenantID: "tenant", ProjectID: "PJ-1", ProjectManagerID: "manager", Status: "现场实施完成", ReportStatus: "COMPILING", Version: 1}
	for _, value := range []any{&project, &item} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	revision := reportRevisionRecord{TenantID: "tenant", ServiceItemID: item.ID, SubjectType: subject, SubjectID: item.ID, Status: "COMPILING", ValidityStatus: "ACTIVE", PreparedBy: "author", FileID: "f", FileSHA256: "sha", ReviewRequired: true, CreatedAt: now, UpdatedAt: now}
	if subject == "PENETRATION_WORK_PACKAGE" {
		pkg := penetrationWorkPackageRecord{ID: "WP-1", TenantID: "tenant", ProjectID: project.ID, ParentServiceItemID: item.ID, ExecutionStatus: "COMPLETED", ReportStatus: "DRAFTING", Version: 1}
		if err := db.Create(&pkg).Error; err != nil {
			t.Fatal(err)
		}
		revision.SubjectID = pkg.ID
		revision.Status = "DRAFTING"
	}
	if err := db.Create(&revision).Error; err != nil {
		t.Fatal(err)
	}
	return platform.ScopeFilter{TenantID: "tenant", AllowAll: true}, item
}

// TestMetricReviewReturnResubmitAndFirstFailure 捕获退回丢失、自审、旧轮次覆盖和更正重获首次样本。
// 参数：t 为测试上下文。
// 返回值：无；主报告或专项任何约束失效时失败。
func TestMetricReviewReturnResubmitAndFirstFailure(t *testing.T) {
	for _, subject := range []string{"SERVICE_ITEM", "PENETRATION_WORK_PACKAGE"} {
		t.Run(subject, func(t *testing.T) {
			repo, db := metricTestDB(t)
			filter, item := seedMetricProject(t, db, subject)
			ctx := context.Background()
			in := domain.ReportReviewInput{SubjectType: subject, ExpectedVersion: 1, IdempotencyKey: "submit-1"}
			first, err := repo.MetricReportReview(ctx, filter, item.ID, "manager", in)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := repo.MetricReportReview(ctx, filter, item.ID, "manager", in)
			if err != nil || replay.ID != first.ID {
				t.Fatalf("replay=%+v %v", replay, err)
			}
			in.ExpectedVersion = 2
			in.RoundID = first.ID
			in.Decision = "RETURNED"
			in.ReasonCode = "TECHNICAL_ERROR"
			in.Note = "实测结论需要更正"
			in.IdempotencyKey = "decision-1"
			if _, err = repo.MetricReportReview(ctx, filter, item.ID, "author", in); !errors.Is(err, application.ErrForbidden) {
				t.Fatalf("self review %v", err)
			}
			if _, err = repo.MetricReportReview(ctx, filter, item.ID, "quality", in); err != nil {
				t.Fatal(err)
			}
			in.ExpectedVersion = 3
			in.Decision = ""
			in.IdempotencyKey = "submit-2"
			second, err := repo.MetricReportReview(ctx, filter, item.ID, "manager", in)
			if err != nil {
				t.Fatal(err)
			}
			if second.RoundNo != 2 {
				t.Fatal("resubmission did not retain round")
			}
			in.ExpectedVersion = 4
			in.Decision = "PASSED"
			in.IdempotencyKey = "decision-2"
			if _, err = repo.MetricReportReview(ctx, filter, item.ID, "quality", in); !errors.Is(err, application.ErrConflict) {
				t.Fatalf("stale round %v", err)
			}
			in.RoundID = second.ID
			if _, err = repo.MetricReportReview(ctx, filter, item.ID, "quality", in); err != nil {
				t.Fatal(err)
			}
			var revision reportRevisionRecord
			db.First(&revision)
			if err = metricReportIssueGate(db, &revision); err != nil {
				t.Fatal(err)
			}
			out := domain.MetricFactsSnapshot{}
			if err = metricReportRows(db, "tenant", &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Rows) != 2 || out.Rows[0].Kind != "report_return" || out.Rows[1].Status != "RETURNED" {
				t.Fatalf("first failure lost %+v", out.Rows)
			}
			filter.TenantID = "other"
			if _, err = repo.MetricReportReview(ctx, filter, item.ID, "quality", in); err == nil {
				t.Fatal("cross tenant accepted")
			}
		})
	}
}

// TestMetricResourceCapacityOverlapIndependentCorrection 捕获无容量、跨项目重叠、自审和归属被事后改写。
// 参数：t 为测试上下文。
// 返回值：无；资源时段账本任一守卫失效时失败。
func TestMetricResourceCapacityOverlapIndependentCorrection(t *testing.T) {
	repo, db := metricTestDB(t)
	filter, item := seedMetricProject(t, db, "SERVICE_ITEM")
	ctx := context.Background()
	resource := capabilityRecord{CapabilityCodes: []byte("[]"), ID: "CAP-P1", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "person-1", UserID: "person-subject", Status: "ACTIVE", IdentityStatus: "ACTIVE"}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	usage := domain.ResourceUsageInput{ResourceType: "PERSON", ResourceID: resource.ResourceID, ServiceItemID: item.ID, BusinessDate: "2026-11-01", Start: at, End: at.Add(time.Hour), Note: "现场测试", Evidence: "现场记录", IdempotencyKey: "u-1"}
	if _, err := repo.CreateMetricUsage(ctx, filter, "manager", true, usage); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("missing capacity %v", err)
	}
	capIn := domain.ResourceCapacityInput{ResourceType: "PERSON", ResourceID: resource.ResourceID, BusinessDate: usage.BusinessDate, OrganizationID: "org-1", OrganizationWitnessID: "person-subject", Intervals: []domain.MetricInterval{{Start: at, End: at.Add(4 * time.Hour)}}, Note: "实际工作安排", IdempotencyKey: "c-1"}
	cap, err := repo.PublishMetricCapacity(ctx, filter, "resource-admin", capIn)
	if err != nil {
		t.Fatal(err)
	}
	row, err := repo.CreateMetricUsage(ctx, filter, "manager", true, usage)
	if err != nil {
		t.Fatal(err)
	}
	decision := domain.ResourceUsageDecision{ExpectedVersion: 1, IdempotencyKey: "u-submit"}
	row, err = repo.MutateMetricUsage(ctx, filter, "manager", row.ID, "SUBMIT", decision)
	if err != nil {
		t.Fatal(err)
	}
	decision.ExpectedVersion = 2
	decision.Decision = "CONFIRMED"
	decision.IdempotencyKey = "u-confirm"
	if _, err = repo.MutateMetricUsage(ctx, filter, "manager", row.ID, "DECISION", decision); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("self confirm %v", err)
	}
	row, err = repo.MutateMetricUsage(ctx, filter, "quality", row.ID, "DECISION", decision)
	if err != nil {
		t.Fatal(err)
	}
	capIn.ExpectedVersion = cap.Version
	capIn.OrganizationID = "org-new"
	capIn.IdempotencyKey = "c-2"
	if _, err = repo.PublishMetricCapacity(ctx, filter, "resource-admin", capIn); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("frozen org %v", err)
	}
	other := serviceItemRecord{ID: "SI-2", TenantID: "tenant", ProjectID: item.ProjectID, Status: "实施中", Version: 1}
	db.Create(&other)
	usage.ServiceItemID = other.ID
	usage.IdempotencyKey = "u-overlap"
	overlapping, err := repo.CreateMetricUsage(ctx, filter, "manager", true, usage)
	if err != nil {
		t.Fatal(err)
	}
	decision.ExpectedVersion = 1
	decision.IdempotencyKey = "u-overlap-submit"
	if _, err = repo.MutateMetricUsage(ctx, filter, "manager", overlapping.ID, "SUBMIT", decision); !errors.Is(err, application.ErrResourceConflict) {
		t.Fatalf("overlap %v", err)
	}
	usage.ServiceItemID = item.ID
	usage.Start = at.Add(time.Hour)
	usage.End = at.Add(2 * time.Hour)
	usage.CorrectsID = row.ID
	usage.IdempotencyKey = "u-correct"
	corrected, err := repo.CreateMetricUsage(ctx, filter, "manager", true, usage)
	if err != nil {
		t.Fatal(err)
	}
	decision.ExpectedVersion = 1
	decision.IdempotencyKey = "u-correct-submit"
	corrected, err = repo.MutateMetricUsage(ctx, filter, "manager", corrected.ID, "SUBMIT", decision)
	if err != nil {
		t.Fatal(err)
	}
	decision.ExpectedVersion = 2
	decision.IdempotencyKey = "u-correct-confirm"
	if _, err = repo.MutateMetricUsage(ctx, filter, "quality", corrected.ID, "DECISION", decision); err != nil {
		t.Fatal(err)
	}
	db.Table("pm_resource_usage").Where("id=?", row.ID).First(&row)
	if row.Status != "REPLACED" {
		t.Fatal("confirmed original overwritten without version replacement")
	}
	out := domain.MetricFactsSnapshot{}
	if err = metricResourceRows(db, "tenant", &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) < 1 || out.Rows[0].ActualMinutes != 60 || out.Rows[0].CapacityMinutes != 240 || out.Rows[0].OrganizationID != "org-1" {
		t.Fatalf("wrong frozen total %+v", out.Rows)
	}
}

// TestMetricDisabledResourceCanCancelConfirmedUsage 捕获资源停用后无法独立纠正历史用量的死锁。
// 参数：t 为测试上下文。
// 返回值：无；停用阻止取消和独立取消确认时失败。
func TestMetricDisabledResourceCanCancelConfirmedUsage(t *testing.T) {
	repo, db := metricTestDB(t)
	filter, item := seedMetricProject(t, db, "SERVICE_ITEM")
	now := time.Now().UTC()
	resource := capabilityRecord{ID: "P", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "P", UserID: "user", Status: "DISABLED", CapabilityCodes: []byte("[]")}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	row := metricUsageRecord{domain.ResourceUsage{ID: "U", OrganizationID: "org-1", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "P", ProjectID: item.ProjectID, ServiceItemID: item.ID, BusinessDate: "2026-11-01", Status: "CONFIRMED", Version: 3, CreatedBy: "manager", SubmittedBy: "manager", CreatedAt: now, UpdatedAt: now}}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	in := domain.ResourceUsageDecision{ExpectedVersion: 3, Note: "事实需取消", IdempotencyKey: "cancel"}
	out, err := repo.MutateMetricUsage(context.Background(), filter, "manager", row.ID, "CANCEL", in)
	if err != nil || out.Status != "CANCEL_PENDING" {
		t.Fatalf("disabled resource must allow cancel request: %v %+v", err, out)
	}
	in.ExpectedVersion = 4
	in.IdempotencyKey = "cancel-confirm"
	in.Decision = "CONFIRMED"
	out, err = repo.MutateMetricUsage(context.Background(), filter, "quality", row.ID, "DECISION", in)
	if err != nil || out.Status != "CANCELLED" {
		t.Fatalf("disabled resource must allow independent cancellation: %v %+v", err, out)
	}
}

// TestMetricPendingReviewFreezesArtifact 捕获送审后同版本文件被静默替换的问题。
// 参数：t 为测试上下文。
// 返回值：无；待审版本仍可替换文件时失败。
func TestMetricPendingReviewFreezesArtifact(t *testing.T) {
	repo, db := metricTestDB(t)
	filter, item := seedMetricProject(t, db, "SERVICE_ITEM")
	_, err := repo.MetricReportReview(context.Background(), filter, item.ID, "manager", domain.ReportReviewInput{SubjectType: "SERVICE_ITEM", ExpectedVersion: 1, IdempotencyKey: "send"})
	if err != nil {
		t.Fatal(err)
	}
	err = repo.RegisterReportArtifact(context.Background(), "tenant", item.ID, 0, domain.ReportArtifactInput{FileID: "different", SHA256: "different-sha"}, "author", time.Now().UTC())
	if err == nil {
		t.Fatal("pending report artifact was replaced after submission")
	}
}

// TestMetricEquipmentWithFactsCannotBeDeleted 捕获设备档案删除破坏容量和历史更正锁身份。
// 参数：t 为测试上下文。
// 返回值：无；存在明确容量或实际用量的设备被删除时失败。
func TestMetricEquipmentWithFactsCannotBeDeleted(t *testing.T) {
	db := equipmentAtomicityDB(t)
	if err := db.AutoMigrate(&metricResourceMutexRecord{}, &metricCapacityRecord{}, &metricUsageRecord{}); err != nil {
		t.Fatal(err)
	}
	equipment := atomicityEquipment(t, db, "tenant", "EQ")
	cap := metricCapacityRecord{domain.ResourceCapacity{ID: "capacity", TenantID: "tenant", ResourceType: "EQUIPMENT", ResourceID: equipment.ResourceID, BusinessDate: "2026-11-01", OrganizationID: "org", Status: "PUBLISHED"}}
	if err := db.Create(&cap).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewRepository(db).DeleteEquipment(context.Background(), "tenant", equipment.ResourceID); !errors.Is(err, application.ErrResourceConflict) {
		t.Fatalf("equipment with metric history must remain: %v", err)
	}
}

// TestMetricResourceListDoesNotTreatEmptyIdentityAsScope 捕获PROJECT范围因设备人员身份空值而泄漏租户记录。
// 参数：t 为测试上下文。
// 返回值：无；项目范围无资源授权仍看见他人设备容量或用量时失败。
func TestMetricResourceListDoesNotTreatEmptyIdentityAsScope(t *testing.T) {
	repo, db := metricTestDB(t)
	cap := metricCapacityRecord{domain.ResourceCapacity{ID: "c", TenantID: "tenant", ResourceType: "EQUIPMENT", ResourceID: "eq", OrganizationID: "secret-org", Status: "PUBLISHED"}}
	usage := metricUsageRecord{domain.ResourceUsage{ID: "u", TenantID: "tenant", ResourceType: "EQUIPMENT", ResourceID: "eq", OrganizationID: "secret-org", CreatedBy: "other"}}
	if err := db.Create(&cap).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&usage).Error; err != nil {
		t.Fatal(err)
	}
	filter := platform.ScopeFilter{TenantID: "tenant", UserID: "viewer", ProjectIDs: []string{"PJ-authorized"}}
	caps, err := repo.ListMetricCapacities(context.Background(), filter, 1)
	if err != nil {
		t.Fatal(err)
	}
	uses, err := repo.ListMetricUsages(context.Background(), filter, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 0 || len(uses) != 0 {
		t.Fatalf("empty identity leaked caps=%d usages=%d", len(caps), len(uses))
	}
}

// TestMetricSamePersonArchivesShareCapacityAndConflict 捕获同一人员不同档案重复日容量与重叠用量。
// 参数：t 为测试上下文。
// 返回值：无；多档案绕过冻结日组织或时段互斥时失败。
func TestMetricSamePersonArchivesShareCapacityAndConflict(t *testing.T) {
	repo, db := metricTestDB(t)
	filter, item := seedMetricProject(t, db, "SERVICE_ITEM")
	ctx := context.Background()
	for _, id := range []string{"archive-a", "archive-b"} {
		if err := db.Create(&capabilityRecord{ID: id, TenantID: "tenant", ResourceType: "PERSON", ResourceID: id, UserID: "same-person", Status: "ACTIVE", IdentityStatus: "ACTIVE", CapabilityCodes: []byte("[]")}).Error; err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	capInput := domain.ResourceCapacityInput{ResourceType: "PERSON", ResourceID: "archive-a", BusinessDate: "2026-11-01", OrganizationID: "org-a", Intervals: []domain.MetricInterval{{Start: at, End: at.Add(4 * time.Hour)}}, Note: "明确容量", IdempotencyKey: "cap-a"}
	if _, err := repo.PublishMetricCapacity(ctx, filter, "capacity-admin", capInput); err != nil {
		t.Fatal(err)
	}
	capInput.ResourceID = "archive-b"
	capInput.OrganizationID = "org-b"
	capInput.IdempotencyKey = "cap-b"
	if _, err := repo.PublishMetricCapacity(ctx, filter, "capacity-admin", capInput); err == nil {
		t.Fatal("same person acquired two capacity organizations via different archives")
	}
	usage := domain.ResourceUsageInput{ResourceType: "PERSON", ResourceID: "archive-a", ServiceItemID: item.ID, BusinessDate: "2026-11-01", Start: at, End: at.Add(time.Hour), Note: "实际用量", Evidence: "现场", IdempotencyKey: "use-a"}
	first, err := repo.CreateMetricUsage(ctx, filter, "manager", true, usage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.MutateMetricUsage(ctx, filter, "manager", first.ID, "SUBMIT", domain.ResourceUsageDecision{ExpectedVersion: 1, IdempotencyKey: "submit-a"}); err != nil {
		t.Fatal(err)
	}
	usage.ResourceID = "archive-b"
	usage.IdempotencyKey = "use-b"
	second, err := repo.CreateMetricUsage(ctx, filter, "manager", true, usage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.MutateMetricUsage(ctx, filter, "manager", second.ID, "SUBMIT", domain.ResourceUsageDecision{ExpectedVersion: 1, IdempotencyKey: "submit-b"}); !errors.Is(err, application.ErrResourceConflict) {
		t.Fatalf("same-person overlap not rejected: %v", err)
	}
}

// TestMetricCurrentReviewIndependentOfOtherSubjectPagination 验证另一主体超过50轮不隐藏当前待审轮次。
// 参数：t 为测试上下文。
// 返回值：无；当前查询受分页或另一报告轮数影响时失败。
func TestMetricCurrentReviewIndependentOfOtherSubjectPagination(t *testing.T) {
	repo, db := metricTestDB(t)
	_, item := seedMetricProject(t, db, "SERVICE_ITEM")
	now := time.Now().UTC()
	pkg := penetrationWorkPackageRecord{ID: "WP", TenantID: "tenant", ParentServiceItemID: item.ID, ProjectID: item.ProjectID, ReportRevision: 0, EngineerIDs: []byte("[]")}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatal(err)
	}
	pending := metricReviewRecord{domain.ReportReviewRound{ID: "main-pending", TenantID: "tenant", ServiceItemID: item.ID, SubjectType: "SERVICE_ITEM", SubjectID: item.ID, Revision: 0, RoundNo: 1, Decision: "PENDING", SubmittedAt: now.Add(-24 * time.Hour)}}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	for index := uint64(1); index <= 51; index++ {
		row := metricReviewRecord{domain.ReportReviewRound{ID: fmt.Sprintf("special-%02d", index), TenantID: "tenant", ServiceItemID: item.ID, SubjectType: "PENETRATION_WORK_PACKAGE", SubjectID: pkg.ID, Revision: 0, RoundNo: index, Decision: "RETURNED", SubmittedAt: now.Add(time.Duration(index) * time.Minute)}}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	history, err := repo.ListMetricReportReviews(context.Background(), "tenant", item.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 50 || history[0].SubjectType != "PENETRATION_WORK_PACKAGE" {
		t.Fatalf("bad boundary fixture %+v", history)
	}
	current, err := repo.CurrentMetricReportReviews(context.Background(), "tenant", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 2 || current[0].ID != "main-pending" || current[0].Decision != "PENDING" || current[1].RoundNo != 51 {
		t.Fatalf("current lost %+v", current)
	}
}
