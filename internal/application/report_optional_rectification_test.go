package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
)

// TestDirectCompilationPreservesAuthorizationAndVersion 验证直接编制仍受编制权限和版本保护。
// 参数：t 为使用应用层仓储替身的测试上下文。
// 返回值：无；越权、旧版本或无范围主体写入事件时失败。
func TestDirectCompilationPreservesAuthorizationAndVersion(t *testing.T) {
	cases := []struct {
		name, permission string
		expectedVersion  uint64
		withoutScope     bool
		want             error
	}{
		{"编制权限允许", "project.report.prepare", 2, false, nil},
		{"审核权限不可替代", "project.report.review", 2, false, ErrForbidden},
		{"签发权限不可替代", "project.report.issue", 2, false, ErrForbidden},
		{"归档权限不可替代", "project.report.archive", 2, false, ErrForbidden},
		{"过期版本拒绝", "project.report.prepare", 1, false, ErrConflict},
		{"无数据范围拒绝", "project.report.prepare", 2, true, ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &reportCorrectionRepository{item: domain.ServiceItem{
				ID: "SI-OPTIONAL", ProjectID: "PJ-OPTIONAL", Status: "现场实施完成",
				ReportStatus: "NONE", Version: 2,
			}}
			principal := reportCorrectionPrincipal("actor", tc.permission)
			if tc.withoutScope {
				principal.DataScopes = nil
			}
			service := Service{Repo: repo}
			err := service.UpdateReportStatus(context.Background(), principal, repo.item.ID,
				domain.ReportStatusInput{Phase: " compiling ", ExpectedVersion: tc.expectedVersion})
			if !errors.Is(err, tc.want) {
				t.Fatalf("错误=%v，期望=%v", err, tc.want)
			}
			if tc.want != nil && len(repo.events) != 0 {
				t.Fatal("拒绝的命令仍生成了推进事件")
			}
			if tc.want == nil && (len(repo.events) != 1 || repo.events[0].Payload["phase"] != "COMPILING") {
				t.Fatal("合法命令未向事务层传递编制阶段")
			}
		})
	}
}
