package mysql

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
)

// rollbackCycleEvent 构造通过真实仓储事务执行的回退申请或决定。
// 参数：f为隔离服务项，kind为事件类型，requestID为原申请，version为当前版本。
// 返回值：携带真实业务身份及独立事件ID的命令。
func rollbackCycleEvent(f rectifyingFixture, kind, requestID string, version uint64) domain.DeliveryEvent {
	actor := "requester"
	if kind != application.EventRollbackRequested {
		actor = "independent"
	}
	return domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: f.tenant,
		ProjectID: f.projectID, ServiceItemID: f.itemID, Type: kind, ActorUserID: actor,
		CreatedAt: time.Now().UTC(), Payload: map[string]any{"kind": "REPORT_TO_FIELD",
			"request_id": requestID, "reason": "隔离回退验证", "comment": "独立复核",
			"requester_user_id": "requester", "expected_version": version}}
}

// TestRollbackCycleConcurrentRequests 验证行锁内只接受同服务项单个待决回退。
// 参数：t为测试上下文，使用明确隔离的PM测试库。
// 返回值：无；两个会话必须一成功一冲突且仅留一个申请。
func TestRollbackCycleConcurrentRequests(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Update("report_status", "RECTIFYING").Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			results <- f.repo.ApplyDeliveryEvent(t.Context(), rollbackCycleEvent(f, application.EventRollbackRequested, "", 1))
		})
	}
	close(start)
	wg.Wait()
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
		t.Fatalf("success=%d conflict=%d events=%d", success, conflict, f.snapshot(t).Events)
	}
}

// TestRollbackCycleOldRequestCannotReworkNewReport 验证旧申请不能作用于后续报告轮次。
// 参数：t为测试上下文，直接更新仅模拟已完成下一轮业务的持久化夹具。
// 返回值：无；旧申请批准失败且状态、版本与事件均不改变。
func TestRollbackCycleOldRequestCannotReworkNewReport(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Update("report_status", "RECTIFYING").Error; err != nil {
		t.Fatal(err)
	}
	request := rollbackCycleEvent(f, application.EventRollbackRequested, "", 1)
	if err := f.repo.ApplyDeliveryEvent(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Updates(map[string]any{"report_revision": 1, "version": 5}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&reportRevisionRecord{TenantID: f.tenant, ServiceItemID: f.itemID, SubjectType: "SERVICE_ITEM", SubjectID: f.itemID, Revision: 1, Status: "RECTIFYING", ValidityStatus: "ACTIVE", CreatedAt: f.now, UpdatedAt: f.now}).Error; err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	err := f.repo.ApplyDeliveryEvent(t.Context(), rollbackCycleEvent(f, application.EventRollbackApproved, request.ID, 5))
	if !errors.Is(err, application.ErrConflict) {
		t.Fatalf("旧轮次批准应冲突: %v", err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("旧申请改变新报告")
	}
	if err := f.repo.ApplyDeliveryEvent(t.Context(), rollbackCycleEvent(f, application.EventRollbackRejected, request.ID, 5)); err != nil {
		t.Fatalf("独立驳回关闭旧申请: %v", err)
	}
	if err := f.repo.ApplyDeliveryEvent(t.Context(), rollbackCycleEvent(f, application.EventRollbackRejected, request.ID, 5)); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("重复决定应冲突: %v", err)
	}
}

// TestRollbackCycleDecisionClosesOnce 验证驳回后不能再批准或重复驳回同一申请。
// 参数：t为测试上下文，使用可返工的真实主报告。
// 返回值：无；后续决定均冲突且报告及事件不被重复改变。
func TestRollbackCycleDecisionClosesOnce(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Update("report_status", "RECTIFYING").Error; err != nil {
		t.Fatal(err)
	}
	request := rollbackCycleEvent(f, application.EventRollbackRequested, "", 1)
	if err := f.repo.ApplyDeliveryEvent(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.ApplyDeliveryEvent(t.Context(), rollbackCycleEvent(f, application.EventRollbackRejected, request.ID, 1)); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	for _, kind := range []string{application.EventRollbackApproved, application.EventRollbackRejected} {
		if err := f.repo.ApplyDeliveryEvent(t.Context(), rollbackCycleEvent(f, kind, request.ID, 1)); !errors.Is(err, application.ErrConflict) {
			t.Fatalf("已关闭申请再次决定: %v", err)
		}
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("重复决定改变终态")
	}
}

// TestRollbackCycleFieldBoundary 验证同报告修订的不同现场轮次也不能复用旧申请。
// 参数：t为测试上下文，合成后续实施轮次的状态边界与版本。
// 返回值：无；旧现场回退必须冲突，不受审批载荷伪造轮次影响。
func TestRollbackCycleFieldBoundary(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Updates(map[string]any{"status": "实施中", "report_status": "NONE"}).Error; err != nil {
		t.Fatal(err)
	}
	request := rollbackCycleEvent(f, application.EventRollbackRequested, "", 1)
	request.Payload["kind"] = "FIELD_TO_PREPARATION"
	if err := f.repo.ApplyDeliveryEvent(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Updates(map[string]any{"status_changed_at": f.now.Add(time.Hour), "version": 4}).Error; err != nil {
		t.Fatal(err)
	}
	decision := rollbackCycleEvent(f, application.EventRollbackApproved, request.ID, 4)
	decision.Payload["kind"] = "FIELD_TO_PREPARATION"
	decision.Payload["rollback_cycle_started_at"] = f.now.Add(time.Hour).Format(time.RFC3339Nano)
	before := f.snapshot(t)
	if err := f.repo.ApplyDeliveryEvent(t.Context(), decision); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("旧现场申请应冲突: %v", err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("旧现场申请改变新轮次")
	}
}

// TestRollbackCycleApprovalClosesLegacySibling 验证批准回退时遗留双申请自动失效且审计主体真实。
// 参数：t为测试上下文，第二申请直接落库仅模拟升级前的遗留数据。
// 返回值：无；原申请正常返工，兄弟申请被系统关闭且不能再次决定。
func TestRollbackCycleApprovalClosesLegacySibling(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Update("report_status", "RECTIFYING").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&reportRevisionRecord{}).Where("tenant_id=?", f.tenant).Update("status", "RECTIFYING").Error; err != nil {
		t.Fatal(err)
	}
	a := rollbackCycleEvent(f, application.EventRollbackRequested, "", 1)
	if err := f.repo.ApplyDeliveryEvent(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	b := rollbackCycleEvent(f, application.EventRollbackRequested, "", 1)
	if err := createEvent(f.db, b); err != nil {
		t.Fatal(err)
	}
	approved := rollbackCycleEvent(f, application.EventRollbackApproved, a.ID, 1)
	if err := f.repo.ApplyDeliveryEvent(t.Context(), approved); err != nil {
		t.Fatal(err)
	}
	var closed deliveryEventRecord
	if err := f.db.Where("tenant_id=? AND event_type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.request_id'))=?", f.tenant, application.EventRollbackRejected, b.ID).Take(&closed).Error; err != nil {
		t.Fatal(err)
	}
	if closed.ActorUserID != "SYSTEM" {
		t.Fatal("自动失效伪装人工")
	}
	var audits []auditOutboxRecord
	if err := f.db.Where("tenant_id=? AND action=?", f.tenant, "PROJECT_MANAGEMENT:"+application.EventRollbackRejected).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].ActorType != "SYSTEM" {
		t.Fatalf("系统审计=%+v", audits)
	}
	snapshot := f.snapshot(t)
	if snapshot.Item.Status != "实施中" || snapshot.Item.ReportRevision != 1 {
		t.Fatal("原申请未正常返工")
	}
	if err := f.repo.ApplyDeliveryEvent(t.Context(), rollbackCycleEvent(f, application.EventRollbackRejected, b.ID, snapshot.Item.Version)); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("失效兄弟仍可决定: %v", err)
	}
}
