package mysql

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

// newPenetrationParentFixture 创建未开展专项及可独立推进的父报告。
// 参数：t为测试上下文，仅使用本轮隔离MySQL。
// 返回值：主服务项夹具与尚未开展的专项。
func newPenetrationParentFixture(t *testing.T) (rectifyingFixture, domain.PenetrationWorkPackage) {
	t.Helper()
	f := newCorrectionBindingFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Updates(map[string]any{
		"category": "等级保护测评", "project_manager_id": "pm", "status": "实施中"}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"pm_penetration_work_package_event", "pm_penetration_work_package"} {
			if err := f.db.Table(table).Where("tenant_id=?", f.tenant).Delete(nil).Error; err != nil {
				t.Error(err)
			}
		}
	})
	pkg, err := f.repo.EnsurePenetrationWorkPackage(t.Context(), f.tenant, serviceFromRecord(f.snapshot(t).Item), "pm", "ensure-parent", f.now)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err = f.repo.SavePenetrationDecision(t.Context(), f.tenant, f.itemID, domain.PenetrationDecisionInput{
		DecisionStatus: "NOT_REQUIRED", CustomerContact: "客户", CommunicationSummary: "无需专项",
		CommunicatedAt: f.now.Format(time.RFC3339), ExpectedVersion: pkg.Version, IdempotencyKey: "not-required"}, "pm", f.now)
	if err != nil {
		t.Fatal(err)
	}
	return f, pkg
}

// TestPenetrationParentClosedDecision 验证现场完成、归档及终止后不能重新增加必需专项。
// 参数：t为测试上下文，状态表分别代表已交付与已终止边界。
// 返回值：无；新决定必须拒绝，既有成功请求的安全重放保持成功。
func TestPenetrationParentClosedDecision(t *testing.T) {
	for _, state := range []struct{ status, report string }{{"现场实施完成", "RECTIFYING"}, {"现场实施完成", "ARCHIVED"}, {"已终止", "NONE"}} {
		t.Run(state.status+state.report, func(t *testing.T) {
			f, pkg := newPenetrationParentFixture(t)
			if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Updates(map[string]any{"status": state.status, "report_status": state.report}).Error; err != nil {
				t.Fatal(err)
			}
			_, err := f.repo.SavePenetrationDecision(t.Context(), f.tenant, f.itemID, domain.PenetrationDecisionInput{
				DecisionStatus: "REQUIRED", CustomerContact: "客户", CommunicationSummary: "补做专项", ChangeReason: "客户新增要求",
				CommunicatedAt: f.now.Format(time.RFC3339), ExpectedVersion: pkg.Version, IdempotencyKey: "new-required"}, "pm", f.now)
			if !errors.Is(err, application.ErrPrecondition) {
				t.Fatalf("父状态%s/%s应阻断: %v", state.status, state.report, err)
			}
			after, err := f.repo.GetPenetrationWorkPackage(t.Context(), f.tenant, f.itemID)
			if err != nil || after.Version != pkg.Version || after.DecisionStatus != "NOT_REQUIRED" {
				t.Fatalf("被拒操作改变专项: %+v %v", after, err)
			}
			_, err = f.repo.SavePenetrationDecision(t.Context(), f.tenant, f.itemID, domain.PenetrationDecisionInput{
				DecisionStatus: "NOT_REQUIRED", CustomerContact: "客户", CommunicationSummary: "无需专项",
				CommunicatedAt: f.now.Format(time.RFC3339), ExpectedVersion: 1, IdempotencyKey: "not-required"}, "pm", f.now)
			if err != nil {
				t.Fatalf("已受理命令应可在终态安全重放: %v", err)
			}
		})
	}
}

// TestPenetrationParentArchiveRace 验证父报告归档与重新开展专项并发时门禁不会被破坏。
// 参数：t为测试上下文，同时启动两条真实数据库事务。
// 返回值：无；父报告归档成功，专项保持不开展且无死锁。
func TestPenetrationParentArchiveRace(t *testing.T) {
	f, pkg := newPenetrationParentFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Updates(map[string]any{"status": "现场实施完成", "report_status": "ISSUED"}).Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var archiveErr, decisionErr error
	wg.Go(func() { <-start; archiveErr = f.advance(t, "archiver", "ARCHIVED") })
	wg.Go(func() {
		<-start
		_, decisionErr = f.repo.SavePenetrationDecision(t.Context(), f.tenant, f.itemID,
			domain.PenetrationDecisionInput{DecisionStatus: "REQUIRED", CommunicatedAt: f.now.Format(time.RFC3339),
				ExpectedVersion: pkg.Version, IdempotencyKey: "race-required"}, "pm", time.Now().UTC())
	})
	close(start)
	wg.Wait()
	if archiveErr != nil || !errors.Is(decisionErr, application.ErrPrecondition) {
		t.Fatalf("archive=%v decision=%v", archiveErr, decisionErr)
	}
	after, err := f.repo.GetPenetrationWorkPackage(t.Context(), f.tenant, f.itemID)
	if err != nil || after.DecisionStatus != "NOT_REQUIRED" || f.snapshot(t).Item.ReportStatus != "ARCHIVED" {
		t.Fatalf("归档门禁被破坏: %+v %v", after, err)
	}
}
