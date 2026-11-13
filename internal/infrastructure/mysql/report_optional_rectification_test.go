package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// TestReportOptionalRectificationTransitions 验证默认整改与直接编制及其它非法跳步。
// 参数：t 为使用真实内存SQL、独立事务的测试上下文。
// 返回值：无；阶段、版本、编制人、项目汇总或事件事实不一致时失败。
func TestReportOptionalRectificationTransitions(t *testing.T) {
	cases := []struct {
		name, current, target string
		allowed, skipped      bool
	}{
		{"默认先整改", "NONE", "RECTIFYING", true, false},
		{"待整改可直接编制", "NONE", "COMPILING", true, true},
		{"整改后正常编制", "RECTIFYING", "COMPILING", true, false},
		{"不可跳到审核", "NONE", "REVIEWED", false, false},
		{"不可跳到签发", "NONE", "ISSUED", false, false},
		{"不可跳到归档", "NONE", "ARCHIVED", false, false},
		{"整改不可跳审核", "RECTIFYING", "REVIEWED", false, false},
		{"编制不可绕过真实审核", "COMPILING", "REVIEWED", false, false},
		{"编制不可退整改", "COMPILING", "RECTIFYING", false, false},
		{"不可重复编制", "COMPILING", "COMPILING", false, false},
		{"审核后不可重用跳过", "REVIEWED", "COMPILING", false, false},
		{"签发后不可重用跳过", "ISSUED", "COMPILING", false, false},
		{"归档后不可重用跳过", "ARCHIVED", "COMPILING", false, false},
		{"未知阶段拒绝", "NONE", "UNKNOWN", false, false},
		{"未知来源不可编制", "UNKNOWN", "COMPILING", false, false},
		{"空来源不可整改", "", "RECTIFYING", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, db := metricTestDB(t)
			if err := db.AutoMigrate(&deliveryEventRecord{}, &automationRecord{}, &deliveryEffectsRecord{}); err != nil {
				t.Fatal(err)
			}
			_, item := seedMetricProject(t, db, "SERVICE_ITEM")
			item.ReportStatus = tc.current
			if err := db.Model(&item).Update("report_status", tc.current).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&reportRevisionRecord{}).Where("tenant_id=?", item.TenantID).
				Updates(map[string]any{"status": tc.current, "prepared_by": "", "prepared_at": nil}).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
			event := domain.DeliveryEvent{ID: "event-report-phase", TenantID: item.TenantID, ServiceItemID: item.ID,
				ActorUserID: "manager", Type: application.EventReportStatusUpdated, CreatedAt: now,
				Payload: map[string]any{"phase": tc.target, "expected_version": item.Version}}
			err := repo.ApplyDeliveryEvent(context.Background(), event)
			if (err == nil) != tc.allowed {
				t.Fatalf("%s→%s error=%v，期望允许=%v", tc.current, tc.target, err, tc.allowed)
			}
			var saved serviceItemRecord
			if err := db.Where("id=?", item.ID).Take(&saved).Error; err != nil {
				t.Fatal(err)
			}
			if !tc.allowed {
				if saved.ReportStatus != tc.current || saved.Version != item.Version {
					t.Fatal("拒绝后仍修改了阶段或版本")
				}
				return
			}
			if saved.Status != "现场实施完成" || saved.ReportStatus != tc.target || saved.Version != item.Version+1 {
				t.Fatalf("阶段/版本不一致: %+v", saved)
			}
			var revision reportRevisionRecord
			if err := db.First(&revision).Error; err != nil {
				t.Fatal(err)
			}
			if revision.Status != tc.target {
				t.Fatal("报告修订与服务项不同步")
			}
			if tc.target == "COMPILING" && (revision.PreparedBy != "manager" || revision.PreparedAt == nil || !revision.PreparedAt.Equal(now)) {
				t.Fatal("进入编制未记录真实编制人及时间")
			}
			var stored deliveryEventRecord
			if err := db.First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Previous string `json:"previous_phase"`
				Skipped  bool   `json:"rectification_skipped"`
				Revision uint64 `json:"report_revision"`
			}
			if json.Unmarshal(stored.Payload, &payload) != nil || payload.Previous != tc.current || payload.Skipped != tc.skipped || payload.Revision != item.ReportRevision {
				t.Fatalf("阶段选择没有真实留痕: %s", stored.Payload)
			}
			var project projectRecord
			if err := db.Where("id=?", item.ProjectID).Take(&project).Error; err != nil {
				t.Fatal(err)
			}
			if project.Status != "报告编制" {
				t.Fatalf("项目派生阶段=%s", project.Status)
			}
		})
	}
}

// TestReportSkipRectificationKeepsVersionAndAuditAtomic 验证旧版本与审计失败不会半推进。
// 参数：t 为本用例独占的内存SQL上下文。
// 返回值：无；过期写入或审计失败留下新阶段时失败。
func TestReportSkipRectificationKeepsVersionAndAuditAtomic(t *testing.T) {
	for _, mode := range []string{"stale-version", "audit-failure"} {
		t.Run(mode, func(t *testing.T) {
			repo, db := metricTestDB(t)
			if err := db.AutoMigrate(&deliveryEventRecord{}, &automationRecord{}, &deliveryEffectsRecord{}); err != nil {
				t.Fatal(err)
			}
			_, item := seedMetricProject(t, db, "SERVICE_ITEM")
			if err := db.Model(&item).Update("report_status", "NONE").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&reportRevisionRecord{}).Where("tenant_id=?", item.TenantID).
				Update("status", "NONE").Error; err != nil {
				t.Fatal(err)
			}
			expected := item.Version
			auditAttempted := false
			auditFailure := errors.New("synthetic audit failure")
			if mode == "stale-version" {
				expected++
			} else {
				if err := db.Callback().Create().Before("gorm:create").Register("fail-report-audit", func(tx *gorm.DB) {
					if tx.Statement.Table == "prj_audit_outbox" {
						auditAttempted = true
						tx.AddError(auditFailure)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			event := domain.DeliveryEvent{ID: "event-atomic", TenantID: item.TenantID, ServiceItemID: item.ID,
				ActorUserID: "manager", Type: application.EventReportStatusUpdated, CreatedAt: time.Now().UTC(),
				Payload: map[string]any{"phase": "COMPILING", "expected_version": expected}}
			err := repo.ApplyDeliveryEvent(context.Background(), event)
			if mode == "stale-version" && !errors.Is(err, application.ErrConflict) {
				t.Fatalf("旧版本错误=%v，期望版本冲突", err)
			}
			if mode == "audit-failure" && (!auditAttempted || !errors.Is(err, auditFailure)) {
				t.Fatalf("未执行真实审计失败路径: attempted=%t, err=%v", auditAttempted, err)
			}
			var saved serviceItemRecord
			if err := db.Where("id=?", item.ID).Take(&saved).Error; err != nil {
				t.Fatal(err)
			}
			if saved.ReportStatus != "NONE" || saved.Version != item.Version {
				t.Fatal("失败事务仍推进服务项")
			}
			var revision reportRevisionRecord
			if err := db.First(&revision).Error; err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Model(&deliveryEventRecord{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if revision.Status != "NONE" || count != 0 {
				t.Fatal("失败事务仍残留新修订状态或交付事件")
			}
		})
	}
}
