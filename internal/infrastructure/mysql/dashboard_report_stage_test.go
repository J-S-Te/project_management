package mysql

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// reportStageDashboardFixture 是仪表盘报告阶段计数的输入：同一批服务项同时用于
// 夹具期望值与 DeriveProjectReportStage，避免测试复制第三份阶段口径。
type reportStageDashboardFixture struct {
	projectID string
	items     []domain.ProjectStatusItem
	want      string
}

// TestDashboardReportStageCountsMatchDerivedStage 验证内部看板的 report_stage_counts：
// 只统计非空派生阶段，且与 DeriveProjectReportStage 逐项一致（混合取最滞后、
// 终止项剔除、未知码与未进入报告段为空、空报告状态视为 NONE）。
// 参数：t 为测试上下文。
// 返回值：无；计数与派生函数或字面期望不一致时测试失败。
func TestDashboardReportStageCountsMatchDerivedStage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err = db.AutoMigrate(&projectRecord{}, &serviceItemRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	fixtures := []reportStageDashboardFixture{
		{"PJ-MIXED", []domain.ProjectStatusItem{{Status: domain.ProjectStatusFieldCompleted, ReportStatus: domain.ReportPhaseNone}, {Status: domain.ProjectStatusFieldCompleted, ReportStatus: domain.ReportPhaseRectifying}}, domain.ReportPhaseNone},
		{"PJ-RECTIFYING", []domain.ProjectStatusItem{{Status: domain.ProjectStatusFieldCompleted, ReportStatus: domain.ReportPhaseRectifying}}, domain.ReportPhaseRectifying},
		{"PJ-ARCHIVED", []domain.ProjectStatusItem{{Status: domain.ProjectStatusFieldCompleted, ReportStatus: domain.ReportPhaseArchived}}, domain.ReportPhaseArchived},
		{"PJ-TERMINATED-ITEM", []domain.ProjectStatusItem{{Status: domain.ProjectStatusTerminated, ReportStatus: domain.ReportPhaseArchived}}, ""},
		{"PJ-UNKNOWN-CODE", []domain.ProjectStatusItem{{Status: domain.ProjectStatusFieldCompleted, ReportStatus: "DRAFTING"}}, ""},
		{"PJ-NO-ITEM", nil, ""},
		{"PJ-IN-PROGRESS", []domain.ProjectStatusItem{{Status: domain.ProjectStatusInProgress, ReportStatus: domain.ReportPhaseRectifying}}, ""},
		{"PJ-EMPTY-REPORT", []domain.ProjectStatusItem{{Status: domain.ProjectStatusFieldCompleted, ReportStatus: ""}}, domain.ReportPhaseNone},
	}
	for index, fixture := range fixtures {
		if err := db.Create(&projectRecord{ID: fixture.projectID, TenantID: "dashboard-tenant", Name: fixture.projectID, Customer: "客户", Contract: "C-" + fixture.projectID, ContractVersion: "v1", SupplementStatus: "NONE", Services: len(fixture.items), Status: domain.ProjectStatusFieldCompleted, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("seed project %s: %v", fixture.projectID, err)
		}
		for itemIndex, item := range fixture.items {
			record := serviceItemRecord{ID: fmt.Sprintf("SI-%02d-%02d", index, itemIndex), TenantID: "dashboard-tenant", ProjectID: fixture.projectID, SourceServiceID: "S1", TestMode: "STANDARD", ConflictStatus: "PASSED", TechReviewStatus: "NONE", ReportStatus: item.ReportStatus, Status: item.Status, Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := db.Create(&record).Error; err != nil {
				t.Fatalf("seed item %s: %v", record.ID, err)
			}
		}
	}

	repo := NewRepository(db)
	dashboard, err := repo.Dashboard(context.Background(), platform.ScopeFilter{TenantID: "dashboard-tenant", AllowAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.ReportStageCounts == nil {
		t.Fatal("report_stage_counts 必须初始化为非 nil 空 map")
	}
	want := map[string]int{}
	for _, fixture := range fixtures {
		// 夹具期望值与权威派生函数必须一致，否则字面期望本身在漂移。
		if got := domain.DeriveProjectReportStage(fixture.items); got != fixture.want {
			t.Fatalf("%s DeriveProjectReportStage()=%q，期望%q", fixture.projectID, got, fixture.want)
		}
		if fixture.want != "" {
			want[fixture.want]++
		}
	}
	if !reflect.DeepEqual(dashboard.ReportStageCounts, want) {
		t.Fatalf("report_stage_counts=%v，期望%v（空阶段不进桶）", dashboard.ReportStageCounts, want)
	}
	// 字面期望：混合 NONE+RECTIFYING 记 NONE；终止/未知码/无服务项/未到报告段不出现。
	if !reflect.DeepEqual(dashboard.ReportStageCounts, map[string]int{domain.ReportPhaseNone: 2, domain.ReportPhaseRectifying: 1, domain.ReportPhaseArchived: 1}) {
		t.Fatalf("report_stage_counts=%v", dashboard.ReportStageCounts)
	}
	bucketed := 0
	for _, count := range dashboard.ReportStageCounts {
		bucketed += count
	}
	if bucketed > dashboard.ProjectCount || dashboard.ProjectCount != len(fixtures) {
		t.Fatalf("bucketed=%d project_count=%d，期望桶和不大于项目数 %d", bucketed, dashboard.ProjectCount, len(fixtures))
	}

	// 没有项目时也是空 map 而不是 nil：内部看板 JSON 不输出 null。
	empty, err := repo.Dashboard(context.Background(), platform.ScopeFilter{TenantID: "empty-tenant", AllowAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if empty.ReportStageCounts == nil || len(empty.ReportStageCounts) != 0 {
		t.Fatalf("空范围 report_stage_counts=%v，期望非 nil 空 map", empty.ReportStageCounts)
	}
}
