package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
)

// TestRectifyingRollbackAllowed 验证报告返工仅开放整改、编制和审核阶段。
// 参数：t 为测试上下文。
// 返回值：无；非法阶段放行或合法阶段拒绝时测试失败。
func TestRectifyingRollbackAllowed(t *testing.T) {
	for _, phase := range []string{"NONE", "RECTIFYING", "COMPILING", "REVIEWED", "ISSUED", "ARCHIVED"} {
		want := phase == "RECTIFYING" || phase == "COMPILING" || phase == "REVIEWED"
		item := domain.ServiceItem{Status: "现场实施完成", ReportStatus: phase}
		if got := rollbackKindAllowed("REPORT_TO_FIELD", item); got != want {
			t.Errorf("报告阶段%s返工=%t，期望%t", phase, got, want)
		}
		item.Status = "实施中"
		if rollbackKindAllowed("REPORT_TO_FIELD", item) {
			t.Errorf("现场未完成却允许%s报告返工", phase)
		}
	}
}

// TestRectifyingRequiresPreparePermission 验证整改推进使用编制权限且权限缺失时关闭。
// 参数：t 为测试上下文。
// 返回值：无；权限错误或错误拒绝合法编制人时测试失败。
func TestRectifyingRequiresPreparePermission(t *testing.T) {
	for _, permission := range []string{
		"project.report.prepare", "project.report.review", "project.report.issue",
		"project.report.archive", "project.field.complete",
	} {
		t.Run(permission, func(t *testing.T) {
			repo := &reportCorrectionRepository{item: domain.ServiceItem{
				ID: "SI-RECTIFY", ProjectID: "PJ-RECTIFY", Status: "现场实施完成",
				ReportStatus: "NONE", Version: 2,
			}}
			service := Service{Repo: repo}
			err := service.UpdateReportStatus(context.Background(),
				reportCorrectionPrincipal("actor", permission), repo.item.ID,
				domain.ReportStatusInput{Phase: " rectifying ", ExpectedVersion: 2})
			if permission == "project.report.prepare" {
				if err != nil {
					t.Fatalf("编制权限无法开始整改: %v", err)
				}
			} else if !errors.Is(err, ErrForbidden) {
				t.Fatalf("权限%s错误=%v，期望拒绝", permission, err)
			}
		})
	}
}
