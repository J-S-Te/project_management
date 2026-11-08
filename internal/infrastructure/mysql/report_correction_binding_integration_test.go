package mysql

import (
	"errors"
	"os"
	"reflect"
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
)

// newCorrectionBindingFixture 在隔离迁移库建立已签发报告供更正绑定回归。
// 参数：t为测试上下文，连接仅来自明确的PM_TEST_DSN。
// 返回值：独立合成租户下的已签发修订0及真实仓储。
func newCorrectionBindingFixture(t *testing.T) rectifyingFixture {
	t.Helper()
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("PM_TEST_DSN is required")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.Addr, "127.0.0.1:") ||
		!strings.HasPrefix(cfg.DBName, "pm_test_") {
		t.Fatal("report correction migration requires an explicit loopback pm_test_* schema")
	}
	if err := migration.Run(t.Context(), dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	f := newRectifyingFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).
		Update("report_status", "ISSUED").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&reportRevisionRecord{}).Where("tenant_id=?", f.tenant).
		Updates(map[string]any{"status": "ISSUED", "prepared_by": "preparer",
			"reviewed_by": "reviewer", "issued_by": "issuer"}).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

// correctionBindingEvent 构造冻结报告修订与服务项版本的真实更正请求。
// 参数：f为合成报告，kind为事件类型，revision/version为业务基线。
// 返回值：具有新事件身份和明确申请人的命令。
func correctionBindingEvent(f rectifyingFixture, kind string, revision, version uint64) domain.DeliveryEvent {
	return domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: f.tenant,
		ProjectID: f.projectID, ServiceItemID: f.itemID, Type: kind,
		ActorUserID: "requester", CreatedAt: time.Now().UTC(),
		Payload: map[string]any{"old_revision": revision, "expected_version": version,
			"reason": "隔离报告更正", "requester_user_id": "requester"}}
}

// TestReportCorrectionRequestsAreUniquePerRevision 验证两个会话不能创建同修订双待决申请。
// 参数：t为测试上下文，只操作新建隔离库里的合成报告。
// 返回值：无；并发请求必须一成功一冲突且只持久一份待决申请。
func TestReportCorrectionRequestsAreUniquePerRevision(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			results <- f.repo.ApplyDeliveryEvent(t.Context(),
				correctionBindingEvent(f, application.EventReportCorrectionRequested, 0, 1))
		})
	}
	close(start)
	workers.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, application.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || f.snapshot(t).Events != 1 {
		t.Fatalf("重复更正申请: success=%d conflict=%d events=%d", success, conflict, f.snapshot(t).Events)
	}
}

// TestReportCorrectionOldRequestCannotInvalidateNewRevision 验证历史双待决旧申请不能作废新报告。
// 参数：t为测试上下文，夹具显式模拟旧版本遗留的第二份待决申请。
// 返回值：无；第二份旧申请必须拒绝，当前报告与事件快照保持不变。
func TestReportCorrectionOldRequestCannotInvalidateNewRevision(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	a := correctionBindingEvent(f, application.EventReportCorrectionRequested, 0, 1)
	if err := f.repo.ApplyDeliveryEvent(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	b := correctionBindingEvent(f, application.EventReportCorrectionRequested, 0, 1)
	// 直接插入只用于模拟升级前已存在的双申请，不把它当作新API写入路径。
	if err := createEvent(f.db, b); err != nil {
		t.Fatal(err)
	}
	decision := correctionBindingEvent(f, application.EventReportCorrectionApproved, 0, 1)
	decision.ActorUserID, decision.Payload["request_id"] = "independent-approver", a.ID
	if err := f.repo.ApplyDeliveryEvent(t.Context(), decision); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"RECTIFYING", "COMPILING", "REVIEWED", "ISSUED"} {
		if phase == "REVIEWED" {
			if err := f.repo.RegisterReportArtifact(t.Context(), f.tenant, f.itemID, 1,
				domain.ReportArtifactInput{FileID: "synthetic-r1", FileName: "R1.pdf",
					MIME: "application/pdf", Size: 100, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				"preparer", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.advance(t, phase, phase); err != nil {
			t.Fatal(err)
		}
	}
	var autoClosed []deliveryEventRecord
	if err := f.db.Where("tenant_id=? AND event_type=?", f.tenant, application.EventReportCorrectionRejected).Find(&autoClosed).Error; err != nil {
		t.Fatal(err)
	}
	if len(autoClosed) != 1 || autoClosed[0].ActorUserID != "SYSTEM" {
		t.Errorf("legacy sibling was not explicitly closed by system: %+v", autoClosed)
	}
	// 模拟更旧系统遗留、尚无失效决议的旧请求，单独验证审批读取原修订。
	legacy := correctionBindingEvent(f, application.EventReportCorrectionRequested, 0, 1)
	if err := createEvent(f.db, legacy); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	stale := correctionBindingEvent(f, application.EventReportCorrectionApproved, 1, before.Item.Version)
	stale.ActorUserID, stale.Payload["request_id"] = "another-approver", legacy.ID
	// 即使提交者把payload里的old_revision改成当前版，也必须以原申请为准。
	if err := f.repo.ApplyDeliveryEvent(t.Context(), stale); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("旧申请作废新修订: %v", err)
	}
	if after := f.snapshot(t); !reflect.DeepEqual(before, after) {
		t.Fatal("拒绝旧申请后仍改变当前报告或事件")
	}
	// 遗留申请仍允许独立审批人明确驳回收口，避免前端一直显示待处理并阻塞新申请。
	stale.Type = application.EventReportCorrectionRejected
	if err := f.repo.ApplyDeliveryEvent(t.Context(), stale); err != nil {
		t.Fatalf("无法关闭遗留旧申请: %v", err)
	}
	if after := f.snapshot(t); !reflect.DeepEqual(before.Item, after.Item) ||
		!reflect.DeepEqual(before.Revision, after.Revision) || after.Events != before.Events+1 {
		t.Fatal("关闭旧申请不应改变新报告，只能追加关闭证据")
	}
}
