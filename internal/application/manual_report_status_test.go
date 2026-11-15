package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
)

// TestManualReportStatusAuthorization 验证手动阶段仅由指派经理按当前版本操作。
// 参数：t 为隔离应用测试上下文。
// 返回值：无；越权、无范围、缺版本、越租户或非法阶段被放行时失败。
func TestManualReportStatusAuthorization(t *testing.T) {
	cases := []struct {
		name, actor, permission, phase, state string
		version                               uint64
		noScope, otherTenant                  bool
		want                                  error
	}{
		{"经理手动审核", "manager", "project.report.prepare", "REVIEWED", "现场实施完成", 4, false, false, nil},
		{"其他经理拒绝", "other", "project.report.prepare", "REVIEWED", "现场实施完成", 4, false, false, ErrForbidden},
		{"无编制权限拒绝", "manager", "project.report.review", "REVIEWED", "现场实施完成", 4, false, false, ErrForbidden},
		{"无范围拒绝", "manager", "project.report.prepare", "REVIEWED", "现场实施完成", 4, true, false, ErrForbidden},
		{"无主体拒绝", "", "project.report.prepare", "REVIEWED", "现场实施完成", 4, false, false, ErrForbidden},
		{"越租户拒绝", "manager", "project.report.prepare", "REVIEWED", "现场实施完成", 4, false, true, ErrNotFound},
		{"缺版本拒绝", "manager", "project.report.prepare", "REVIEWED", "现场实施完成", 0, false, false, ErrValidation},
		{"旧版本拒绝", "manager", "project.report.prepare", "REVIEWED", "现场实施完成", 3, false, false, ErrConflict},
		{"跳步拒绝", "manager", "project.report.prepare", "ISSUED", "现场实施完成", 4, false, false, ErrPrecondition},
		{"现场未完拒绝", "manager", "project.report.prepare", "REVIEWED", "实施中", 4, false, false, ErrPrecondition},
		{"未知目标拒绝", "manager", "project.report.prepare", "UNKNOWN", "现场实施完成", 4, false, false, ErrValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &reportCorrectionRepository{item: domain.ServiceItem{
				ID: "SI-MANUAL", TenantID: "tenant-1", ProjectID: "PJ-MANUAL",
				ProjectManagerID: "manager", Status: tc.state, ReportStatus: "COMPILING", Version: 4,
			}}
			p := reportCorrectionPrincipal(tc.actor, tc.permission)
			if tc.noScope {
				p.DataScopes = nil
			}
			if tc.otherTenant {
				repo.item.TenantID = "other-tenant"
			}
			svc := Service{Repo: repo}
			err := svc.AdvanceManualReportStatus(context.Background(), p, repo.item.ID,
				domain.ReportStatusInput{Phase: tc.phase, ExpectedVersion: tc.version})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want=%v", err, tc.want)
			}
			if tc.want != nil && len(repo.events) != 0 {
				t.Fatal("拒绝的手动命令产生事件")
			}
			if tc.want == nil {
				if len(repo.events) != 1 || repo.events[0].ActorUserID != "manager" ||
					repo.events[0].Payload["transition_mode"] != "PROJECT_MANAGER_MANUAL" {
					t.Fatal("手动流转缺少真实操作者或明确模式")
				}
			}
		})
	}
}
