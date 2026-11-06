package mysql

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type rectifyingFixture struct {
	db                        *gorm.DB
	repo                      *Repository
	tenant, projectID, itemID string
	now                       time.Time
}

type rectifyingSnapshot struct {
	Item     serviceItemRecord
	Revision reportRevisionRecord
	Project  projectRecord
	Events   int64
}

// newRectifyingFixture 在明确配置的隔离 MySQL 中创建现场已完成的主服务项。
// 参数：t 为测试上下文。
// 返回值：可执行真实仓储事务的测试夹具；缺少测试 DSN 时跳过。
func newRectifyingFixture(t *testing.T) rectifyingFixture {
	t.Helper()
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("PM_TEST_DSN is required")
	}
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	id := ulid.Make().String()
	f := rectifyingFixture{
		db: db, repo: NewRepository(db), tenant: id,
		projectID: "PJ-" + id, itemID: "SI-" + id,
		now: time.Now().UTC().Truncate(time.Millisecond),
	}
	t.Cleanup(func() {
		for _, table := range []string{
			"prj_audit_outbox", "pm_delivery_effects_outbox", "pm_notification_outbox",
			"pm_delivery_event", "pm_report_revision", "pm_service_item", "pm_project",
		} {
			if err := db.Table(table).Where("tenant_id=?", f.tenant).Delete(nil).Error; err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	project := projectRecord{
		ID: f.projectID, TenantID: f.tenant, Name: "报告整改回归", Customer: "客户",
		Contract: "C-" + id, ContractVersion: "v1", SupplementStatus: "NONE",
		Services: 1, Status: "现场实施完成", Version: 1, CreatedAt: f.now, UpdatedAt: f.now,
	}
	item := serviceItemRecord{
		ID: f.itemID, TenantID: f.tenant, ProjectID: f.projectID, SourceServiceID: "S1",
		TestMode: "STANDARD", Status: "现场实施完成", ReportStatus: "NONE",
		ConflictStatus: "PASSED", TechReviewStatus: "NONE", Version: 1,
		CreatedAt: f.now, UpdatedAt: f.now,
	}
	revision := reportRevisionRecord{
		TenantID: f.tenant, ServiceItemID: f.itemID, SubjectType: "SERVICE_ITEM",
		SubjectID: f.itemID, Status: "NONE", ValidityStatus: "ACTIVE",
		CreatedAt: f.now, UpdatedAt: f.now,
	}
	for _, record := range []any{&project, &item, &revision} {
		if err := db.Create(record).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// snapshot 读取事务拒绝前后的持久化状态与事件数量。
// 参数：t 为测试上下文。
// 返回值：当前主服务项、修订、项目与事件快照；读取失败终止测试。
func (f rectifyingFixture) snapshot(t *testing.T) rectifyingSnapshot {
	t.Helper()
	var s rectifyingSnapshot
	if err := f.db.Where("tenant_id=? AND id=?", f.tenant, f.itemID).Take(&s.Item).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("tenant_id=? AND subject_id=? AND revision=?", f.tenant, f.itemID, s.Item.ReportRevision).Take(&s.Revision).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("tenant_id=? AND id=?", f.tenant, f.projectID).Take(&s.Project).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&deliveryEventRecord{}).Where("tenant_id=?", f.tenant).Count(&s.Events).Error; err != nil {
		t.Fatal(err)
	}
	return s
}

// advance 通过真实仓储提交带版本号的报告推进事件。
// 参数：t 为测试上下文，actor 为操作人，phase 为目标报告阶段。
// 返回值：真实事务校验或存储错误；成功返回 nil。
func (f rectifyingFixture) advance(t *testing.T, actor, phase string) error {
	t.Helper()
	s := f.snapshot(t)
	return f.repo.ApplyDeliveryEvent(context.Background(), domain.DeliveryEvent{
		ID: ulid.Make().String(), TenantID: f.tenant, ProjectID: f.projectID,
		ServiceItemID: f.itemID, Type: application.EventReportStatusUpdated,
		ActorUserID: actor, CreatedAt: time.Now().UTC(),
		Payload: map[string]any{"phase": phase, "expected_version": s.Item.Version},
	})
}

// rejectAdvance 验证非法推进失败且持久化对象与版本、事件均未变化。
// 参数：t 为测试上下文，actor 为操作人，phase 为目标阶段，want 为期望错误。
// 返回值：无；错误或原子性不符时测试失败。
func (f rectifyingFixture) rejectAdvance(t *testing.T, actor, phase string, want error) {
	t.Helper()
	before := f.snapshot(t)
	if err := f.advance(t, actor, phase); !errors.Is(err, want) {
		t.Fatalf("拒绝%s错误=%v，期望%v", phase, err, want)
	}
	if after := f.snapshot(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("拒绝%s后持久化快照变化", phase)
	}
}

// snapshotProjectStage 通过真实快照流读取该项目行的报告阶段，验证 PM→DA 流式契约
// 与详情派生同源（投影始终带 report_stage，header/end 版本仍为 1）。
// 参数：t 为测试上下文，want 为期望报告阶段。
// 返回值：无；流式读取失败、行缺失或阶段不一致时测试失败。
func (f rectifyingFixture) snapshotProjectStage(t *testing.T, want string) {
	t.Helper()
	sink := &dbSnapshotSink{}
	summary, err := f.repo.StreamProjectSnapshot(context.Background(), f.tenant, application.DefaultProjectSnapshotLimits(), sink)
	if err != nil {
		t.Fatalf("读取项目快照流: %v", err)
	}
	if sink.header.SchemaVersion != 1 || summary.SchemaVersion != 1 {
		t.Fatalf("快照版本 header=%d end=%d，期望 1/1", sink.header.SchemaVersion, summary.SchemaVersion)
	}
	if len(sink.rows) != 1 || sink.rows[0].ID != f.projectID {
		t.Fatalf("快照项目行=%+v，期望仅 %s", sink.rows, f.projectID)
	}
	if sink.rows[0].ReportStage != want {
		t.Fatalf("快照 report_stage=%q，期望 %q", sink.rows[0].ReportStage, want)
	}
}

// TestReportRectifyingChainOnMySQL 验证整改必经、职责分离及文件门禁的完整主报告链。
// 参数：t 为测试上下文。
// 返回值：无；状态跳级、证据误写或原有审核签发门禁失效时测试失败。
func TestReportRectifyingChainOnMySQL(t *testing.T) {
	f := newRectifyingFixture(t)
	f.rejectAdvance(t, "preparer", "COMPILING", application.ErrValidation)
	before := f.snapshot(t)
	if err := f.advance(t, "rectifier", "RECTIFYING"); err != nil {
		t.Fatalf("开始整改: %v", err)
	}
	s := f.snapshot(t)
	wantRevision := before.Revision
	wantRevision.Status, wantRevision.UpdatedAt = "RECTIFYING", s.Revision.UpdatedAt
	if !reflect.DeepEqual(wantRevision, s.Revision) {
		t.Fatal("整改修改了状态与更新时间之外的修订证据")
	}
	if s.Item.ReportStatus != "RECTIFYING" || s.Revision.Status != "RECTIFYING" || s.Project.Status != domain.ProjectStatusReporting {
		t.Fatalf("整改状态未同步: item=%s revision=%s project=%s", s.Item.ReportStatus, s.Revision.Status, s.Project.Status)
	}
	// 项目级报告阶段是派生维度：报告推进后详情必须立即反映 RECTIFYING。
	listed, err := f.repo.GetProject(context.Background(), platform.ScopeFilter{TenantID: f.tenant, AllowAll: true}, f.projectID)
	if err != nil {
		t.Fatalf("读取整改中的项目: %v", err)
	}
	if listed.ReportStage != domain.ReportPhaseRectifying {
		t.Fatalf("整改中项目 report_stage=%q，期望 RECTIFYING", listed.ReportStage)
	}
	f.snapshotProjectStage(t, domain.ReportPhaseRectifying)
	if s.Item.ReportPreparedBy != "" || s.Revision.PreparedBy != "" || s.Revision.PreparedAt != nil {
		t.Fatal("整改提前记录了编制人或编制时间")
	}
	f.rejectAdvance(t, "reviewer", "REVIEWED", application.ErrValidation)
	artifact := domain.ReportArtifactInput{FileID: "FILE-RECTIFY", FileName: "报告.pdf", MIME: "application/pdf", Size: 128, SHA256: strings.Repeat("a", 64)}
	if err := f.repo.RegisterReportArtifact(context.Background(), f.tenant, f.itemID, 0, artifact, "rectifier", f.now); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("整改上传错误=%v，期望前置条件拒绝", err)
	}
	if err := f.advance(t, "preparer", "COMPILING"); err != nil {
		t.Fatal(err)
	}
	s = f.snapshot(t)
	if s.Item.ReportPreparedBy != "preparer" || s.Revision.PreparedBy != "preparer" || s.Revision.PreparedAt == nil {
		t.Fatal("编制阶段未登记真实编制人和时间")
	}
	f.rejectAdvance(t, "reviewer", "REVIEWED", application.ErrPrecondition)
	if err := f.repo.RegisterReportArtifact(context.Background(), f.tenant, f.itemID, 0, artifact, "preparer", f.now); err != nil {
		t.Fatal(err)
	}
	f.rejectAdvance(t, "preparer", "REVIEWED", application.ErrPrecondition)
	if err := f.advance(t, "reviewer", "REVIEWED"); err != nil {
		t.Fatal(err)
	}
	f.rejectAdvance(t, "preparer", "ISSUED", application.ErrPrecondition)
	f.rejectAdvance(t, "reviewer", "ISSUED", application.ErrPrecondition)
	if err := f.advance(t, "issuer", "ISSUED"); err != nil {
		t.Fatal(err)
	}
	if err := f.advance(t, "archiver", "ARCHIVED"); err != nil {
		t.Fatal(err)
	}
	s = f.snapshot(t)
	if s.Item.ReportStatus != "ARCHIVED" || s.Revision.Status != "ARCHIVED" || s.Project.Status != domain.ProjectStatusCompleted {
		t.Fatal("报告归档未同步项目与修订状态")
	}
	listed, err = f.repo.GetProject(context.Background(), platform.ScopeFilter{TenantID: f.tenant, AllowAll: true}, f.projectID)
	if err != nil {
		t.Fatalf("读取已归档项目: %v", err)
	}
	if listed.ReportStage != domain.ReportPhaseArchived {
		t.Fatalf("已归档项目 report_stage=%q，期望 ARCHIVED", listed.ReportStage)
	}
	f.snapshotProjectStage(t, domain.ReportPhaseArchived)
	f.rejectAdvance(t, "rectifier", "RECTIFYING", application.ErrValidation)
}
