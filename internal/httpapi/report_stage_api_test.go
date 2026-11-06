package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestProjectListReportStageParameter 验证 GET /projects 的 report_stage 参数：
// 大小写不敏感、只接受 6 个报告阶段码，非法值返回 422；既有 status 参数保持宽松。
func TestProjectListReportStageParameter(t *testing.T) {
	handler := router(t, map[string]bool{"project.read": true}, nil)

	for _, value := range []string{"rectifying", "NONE", "COMPILING", "REVIEWED", "ISSUED", "ARCHIVED"} {
		response := perform(handler, http.MethodGet, "/api/v1/projects?report_stage="+value, "")
		if response.Code != http.StatusOK {
			t.Fatalf("report_stage=%q status=%d body=%s", value, response.Code, response.Body.String())
		}
	}
	invalid := perform(handler, http.MethodGet, "/api/v1/projects?report_stage=DRAFTING", "")
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "PM_VALIDATION_ERROR") {
		t.Fatalf("非法 report_stage 必须 422，实际 status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	// 与既有 status 参数刻意不对称：未知状态仍按宽松口径返回 200（空集）。
	lenient := perform(handler, http.MethodGet, "/api/v1/projects?status=不存在状态", "")
	if lenient.Code != http.StatusOK {
		t.Fatalf("未知 status 应保持宽松，实际 status=%d body=%s", lenient.Code, lenient.Body.String())
	}
	// 两个筛选可组合。
	combined := perform(handler, http.MethodGet, "/api/v1/projects?status=报告编制&report_stage=rectifying", "")
	if combined.Code != http.StatusOK {
		t.Fatalf("组合筛选 status=%d body=%s", combined.Code, combined.Body.String())
	}
}

// TestProjectMonitoringReportStageParameter 验证监控端点接受 report_stage 并返回计数契约。
func TestProjectMonitoringReportStageParameter(t *testing.T) {
	handler := router(t, map[string]bool{"project.read": true}, nil)

	response := perform(handler, http.MethodGet, "/api/v1/projects-monitoring?report_stage=rectifying", "")
	if response.Code != http.StatusOK {
		t.Fatalf("monitoring report_stage status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"report_stage_counts"`) {
		t.Fatalf("监控快照缺少 report_stage_counts: %s", response.Body.String())
	}
	invalid := perform(handler, http.MethodGet, "/api/v1/projects-monitoring?report_stage=DRAFTING", "")
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "PM_VALIDATION_ERROR") {
		t.Fatalf("监控非法 report_stage 必须 422，实际 status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}
