package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
)

// manualReportFixture 建立无报告文件、无真实审核的隔离主报告事务场景。
// 参数：t 为测试上下文，phase 为起始报告阶段。
// 返回值：独立内存仓储、数据库和指派经理为 manager 的服务项。
func manualReportFixture(t *testing.T, phase string) (*Repository, *gorm.DB, serviceItemRecord) {
	t.Helper()
	repo, db := metricTestDB(t)
	if err := db.AutoMigrate(&deliveryEventRecord{}, &automationRecord{}, &deliveryEffectsRecord{}); err != nil {
		t.Fatal(err)
	}
	_, item := seedMetricProject(t, db, "SERVICE_ITEM")
	item.ReportStatus = phase
	if err := db.Save(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&reportRevisionRecord{}).Where("tenant_id=?", item.TenantID).
		Updates(map[string]any{"status": phase, "prepared_by": "", "file_id": "", "file_sha256": ""}).Error; err != nil {
		t.Fatal(err)
	}
	return repo, db, item
}

// TestManualReportStatusRejectsPendingReview 验证旧待审请求不能覆盖手动归档阶段。
// 参数：t 为带既有真实待审轮次的隔离事务上下文。
// 返回值：无；刷新版本后的审核决定改变阶段、版本或真实轮次时失败。
func TestManualReportStatusRejectsPendingReview(t *testing.T) {
	repo, db, item := manualReportFixture(t, "COMPILING")
	db.Model(&reportRevisionRecord{}).Where("tenant_id=?", item.TenantID).
		Updates(map[string]any{"prepared_by": "author", "file_id": "file", "file_sha256": "sha"})
	filter := platform.ScopeFilter{TenantID: item.TenantID, AllowAll: true}
	round, err := repo.MetricReportReview(context.Background(), filter, item.ID, "manager",
		domain.ReportReviewInput{SubjectType: "SERVICE_ITEM", ExpectedVersion: item.Version,
			IdempotencyKey: "pending-before-manual"})
	if err != nil {
		t.Fatal(err)
	}
	db.First(&item)
	for _, phase := range []string{"REVIEWED", "ISSUED", "ARCHIVED"} {
		if err = repo.ApplyDeliveryEvent(context.Background(), manualReportEvent(item, phase, "manager")); err != nil {
			t.Fatal(err)
		}
		db.First(&item)
	}
	version := item.Version
	_, err = repo.MetricReportReview(context.Background(), filter, item.ID, "quality",
		domain.ReportReviewInput{SubjectType: "SERVICE_ITEM", ExpectedVersion: version,
			Decision: "PASSED", RoundID: round.ID, IdempotencyKey: "late-real-review"})
	if !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("手动归档后的旧审核错误=%v，期望拒绝混用流程", err)
	}
	var stored metricReviewRecord
	db.First(&stored)
	db.First(&item)
	if item.ReportStatus != "ARCHIVED" || item.Version != version || stored.Decision != "PENDING" {
		t.Fatal("被拒绝的旧审核仍改写了手动阶段或真实轮次")
	}
}

// TestManualReportStatusRejectsStrictContinuation 验证进入手动流转后旧状态接口不能接管。
// 参数：t 为当前版本已经手动开始整改的隔离测试上下文。
// 返回值：无；旧命令能更新该修订时失败。
func TestManualReportStatusRejectsStrictContinuation(t *testing.T) {
	repo, db, item := manualReportFixture(t, "NONE")
	if err := repo.ApplyDeliveryEvent(context.Background(), manualReportEvent(item, "RECTIFYING", "manager")); err != nil {
		t.Fatal(err)
	}
	db.First(&item)
	event := manualReportEvent(item, "COMPILING", "other")
	delete(event.Payload, "transition_mode")
	if err := repo.ApplyDeliveryEvent(context.Background(), event); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("旧状态命令错误=%v，期望拒绝混用流程", err)
	}
}

// manualReportEvent 构造只包含阶段、当前版本及明确手动模式的业务事件。
// 参数：item 为当前服务项，phase 为下一阶段，actor 为真实操作主体。
// 返回值：带独立事件编号的手动流转事件。
func manualReportEvent(item serviceItemRecord, phase, actor string) domain.DeliveryEvent {
	return domain.DeliveryEvent{ID: "manual-" + phase, TenantID: item.TenantID,
		ServiceItemID: item.ID, ActorUserID: actor, Type: application.EventReportStatusUpdated,
		CreatedAt: time.Now().UTC(), Payload: map[string]any{
			"phase": phase, "expected_version": item.Version, "transition_mode": "PROJECT_MANAGER_MANUAL",
		}}
}

// TestManualReportStatusCompleteFlow 验证无文件手动逐步归档且不制造审核事实。
// 参数：t 为真实 SQLite 事务测试上下文。
// 返回值：无；修订/版本/项目派生、留痕或审核事实错误时失败。
func TestManualReportStatusCompleteFlow(t *testing.T) {
	repo, db, item := manualReportFixture(t, "NONE")
	for _, phase := range []string{"RECTIFYING", "COMPILING", "REVIEWED", "ISSUED", "ARCHIVED"} {
		event := manualReportEvent(item, phase, "manager")
		if err := repo.ApplyDeliveryEvent(context.Background(), event); err != nil {
			t.Fatalf("推进至 %s 失败: %v", phase, err)
		}
		oldVersion := item.Version
		if err := db.Where("id=?", item.ID).Take(&item).Error; err != nil {
			t.Fatal(err)
		}
		if item.ReportStatus != phase || item.Version != oldVersion+1 ||
			item.ReportPreparedBy != "" || item.ReportReviewedBy != "" || item.ReportIssuedBy != "" {
			t.Fatalf("阶段或版本错误，或伪造了人员证据: %+v", item)
		}
		var revision reportRevisionRecord
		if err := db.First(&revision).Error; err != nil {
			t.Fatal(err)
		}
		if revision.Status != phase || !revision.ReviewRequired || revision.ReviewedBy != "" ||
			revision.IssuedBy != "" || revision.ArchivedBy != "" || revision.ReviewedAt != nil {
			t.Fatalf("手动流转污染了真实审核证据: %+v", revision)
		}
		var stored deliveryEventRecord
		if err := db.Where("id=?", event.ID).Take(&stored).Error; err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if json.Unmarshal(stored.Payload, &payload) != nil ||
			payload["transition_mode"] != "PROJECT_MANAGER_MANUAL" || payload["previous_phase"] == nil {
			t.Fatal("手动模式及原阶段未留痕")
		}
	}
	var rounds int64
	db.Model(&metricReviewRecord{}).Count(&rounds)
	var project projectRecord
	db.First(&project)
	if rounds != 0 || project.Status != "已完成" {
		t.Fatalf("审核轮次=%d，项目阶段=%s", rounds, project.Status)
	}
}

// TestManualReportStatusLockedGuards 验证事务内重查经理、版本和阶段并完整回滚。
// 参数：t 为使用真实 SQLite 的隔离测试上下文。
// 返回值：无；非法命令改动阶段、版本或留下事件时失败。
func TestManualReportStatusLockedGuards(t *testing.T) {
	for _, scenario := range []string{
		"经理已换", "缺少版本", "旧版本", "跳步", "重复阶段", "已归档", "越租户", "失效修订",
	} {
		t.Run(scenario, func(t *testing.T) {
			repo, db, item := manualReportFixture(t, "COMPILING")
			event := manualReportEvent(item, "REVIEWED", "manager")
			switch scenario {
			case "经理已换":
				db.Model(&item).Update("project_manager_id", "other")
			case "缺少版本":
				delete(event.Payload, "expected_version")
			case "旧版本":
				db.Model(&item).Update("version", item.Version+1)
			case "跳步":
				event.Payload["phase"] = "ISSUED"
			case "重复阶段":
				event.Payload["phase"] = "COMPILING"
			case "已归档":
				db.Model(&item).Update("report_status", "ARCHIVED")
			case "越租户":
				event.TenantID = "other-tenant"
			case "失效修订":
				db.Model(&reportRevisionRecord{}).Where("tenant_id=?", item.TenantID).
					Update("validity_status", "VOID")
			}
			var before serviceItemRecord
			db.First(&before)
			if err := repo.ApplyDeliveryEvent(context.Background(), event); err == nil {
				t.Fatal("非法手动流转未被拒绝")
			}
			var after serviceItemRecord
			db.First(&after)
			var count int64
			db.Model(&deliveryEventRecord{}).Count(&count)
			if after.Version != before.Version || after.ReportStatus != before.ReportStatus || count != 0 {
				t.Fatal("拒绝后仍留下业务修改")
			}
		})
	}
}
