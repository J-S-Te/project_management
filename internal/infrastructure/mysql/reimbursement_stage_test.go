package mysql

import (
	"context"
	"slices"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

// TestExpenseSearchAndBatchRequireDerivedReportingStage 验证搜索和批验使用服务项派生的项目阶段。
// 参数：t为隔离SQLite测试上下文。
// 返回值：无；存储状态绕过阶段门槛或进入报告后未开放时失败。
func TestExpenseSearchAndBatchRequireDerivedReportingStage(t *testing.T) {
	r := expenseRepositoryFixture(t)
	if err := r.db.AutoMigrate(&penetrationWorkPackageRecord{}); err != nil {
		t.Fatal(err)
	}
	item := serviceItemRecord{
		ID: "a", TenantID: "t", ProjectID: "p", EngineerIDs: []byte(`["u"]`),
		Status: domain.ProjectStatusFieldCompleted, Version: 1,
	}
	if err := r.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	service := application.Service{Repo: r, Personnel: expenseDirectory{Active: true}}
	ctx := context.Background()
	in := application.ExpenseBatchInput{
		ActorSubjectID: "u", ClaimID: "c", DraftVersion: 1,
		References: []application.ExpenseReferenceInput{{
			ProjectID: "p", ServiceItemID: "a", OccurredDate: "2026-11-10",
		}},
	}
	for _, stage := range []struct {
		reportStatus string
		wantStatus   string
		wantEligible bool
	}{
		{"NONE", domain.ProjectStatusFieldCompleted, false},
		{"RECTIFYING", domain.ProjectStatusReporting, true},
		{"COMPILING", domain.ProjectStatusReporting, true},
		{"ARCHIVED", domain.ProjectStatusCompleted, true},
		{"NONE", domain.ProjectStatusFieldCompleted, false},
	} {
		if err := r.db.Model(&serviceItemRecord{}).Where("id=?", "a").
			Update("report_status", stage.reportStatus).Error; err != nil {
			t.Fatal(err)
		}
		search, err := service.SearchExpenseReferences(ctx, "t", application.ExpenseSearchInput{
			ActorSubjectID: "u", PageSize: 20,
		})
		if err != nil || len(search.Items) != 1 {
			t.Fatalf("search report=%s: %+v %v", stage.reportStatus, search, err)
		}
		batch, err := service.BatchValidateExpenseReferences(ctx, "t", in)
		if err != nil || len(batch.Items) != 1 {
			t.Fatalf("batch report=%s: %+v %v", stage.reportStatus, batch, err)
		}
		for _, ref := range []domain.ExpenseProjectReference{search.Items[0], batch.Items[0]} {
			if ref.ProjectStatus != stage.wantStatus || ref.Eligible != stage.wantEligible ||
				!ref.ParticipantEligible ||
				slices.Contains(ref.ReasonCodes, "PROJECT_REPORT_STAGE_REQUIRED") == stage.wantEligible {
				t.Fatalf("report=%s reference=%+v", stage.reportStatus, ref)
			}
		}
	}
}
