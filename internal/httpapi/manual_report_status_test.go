package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
)

// TestManualReportStatusRoute 验证新接口完整执行认证授权和命令校验。
// 参数：t 为实际 Gin 路由测试上下文。
// 返回值：无；接口未接通、错误状态或非当前经理被放行时失败。
func TestManualReportStatusRoute(t *testing.T) {
	for _, tc := range []struct {
		name, permission, manager, body string
		want                            int
	}{
		{"正常推进", "project.report.prepare", "user-1", `{"phase":"REVIEWED","expected_version":3}`, 200},
		{"无编制权限", "project.report.review", "user-1", `{"phase":"REVIEWED","expected_version":3}`, 403},
		{"其他经理", "project.report.prepare", "other", `{"phase":"REVIEWED","expected_version":3}`, 403},
		{"缺少版本", "project.report.prepare", "user-1", `{"phase":"REVIEWED"}`, 422},
		{"旧版本", "project.report.prepare", "user-1", `{"phase":"REVIEWED","expected_version":2}`, 409},
		{"非法请求", "project.report.prepare", "user-1", `{`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []domain.ServiceItem{{ID: "SI-1", TenantID: "tenant-1", ProjectID: "PJ-1",
				ProjectManagerID: tc.manager, Status: "现场实施完成", ReportStatus: "COMPILING", Version: 3}}
			handler := routerWithItems(t, map[string]bool{tc.permission: true}, nil, items)
			response := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/report-status/manual", tc.body)
			if response.Code != tc.want {
				t.Fatalf("status=%d, want=%d, body=%s", response.Code, tc.want, response.Body.String())
			}
			if tc.want == 200 && !strings.Contains(response.Body.String(), "PROJECT_MANAGER_MANUAL") {
				t.Fatal("手动流转响应缺少明确模式")
			}
		})
	}
}
