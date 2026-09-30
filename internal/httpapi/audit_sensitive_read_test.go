package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/platform"
)

// AUD-2026-006：敏感读（导出/下载/令牌签发）在强制审计模式下必须先缓冲后提交。
// 本文件用中间件级路由直接驱动 auditWrites，聚焦缓冲与拒绝语义本身。

const sensitiveExportPath = "/api/v1/capabilities/export"

// newSensitiveReadTestRouter 构造只挂审计中间件的最小路由：handler 模拟导出接口
// 写出业务数据（小响应走 c.Data 的 Write 路径，大响应走 c.String 的 WriteString 路径）。
func newSensitiveReadTestRouter(audit platform.AuditReporter, logs *bytes.Buffer, payload string, useData bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	h := &Handler{audit: audit, logger: logger}
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET(sensitiveExportPath, h.auditWrites(), func(c *gin.Context) {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		if useData {
			c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(payload))
			return
		}
		c.String(http.StatusOK, "%s", payload)
	})
	return router
}

func performSensitiveRead(t *testing.T, router *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, sensitiveExportPath, nil)
	request.Header.Set("X-Request-ID", "01JSENSITIVE0000000000000000")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// 强制审计 + 敏感读 + Report 失败：必须 503，且客户端拿不到任何业务数据。
func TestPMSensitiveReadRejectedWhenReportFailsUnderRequiredAudit(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	var logs bytes.Buffer
	reporter := &stubAuditReporterPM{err: errors.New("platform audit ingest unavailable")}
	router := newSensitiveReadTestRouter(reporter, &logs, "EXPORT-ROW,secret-capability", true)

	response := performSensitiveRead(t, router)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "PM_AUDIT_WRITE_REJECTED") {
		t.Fatalf("body = %s, want PM_AUDIT_WRITE_REJECTED", body)
	}
	if strings.Contains(body, "EXPORT-ROW") {
		t.Fatalf("business data leaked after audit failure: %s", body)
	}
	if reporter.calls != 1 {
		t.Fatalf("Report calls = %d, want 1", reporter.calls)
	}
	logged := logs.String()
	if !strings.Contains(logged, "report platform audit failed") || !strings.Contains(logged, "GET "+sensitiveExportPath) {
		t.Fatalf("missing report failure log with route: %s", logged)
	}
}

// 强制审计 + 敏感读 + Report 成功：缓冲的业务响应必须原样提交（含响应头）。
func TestPMSensitiveReadFlushedWhenReportSucceedsUnderRequiredAudit(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	var logs bytes.Buffer
	reporter := &stubAuditReporterPM{}
	router := newSensitiveReadTestRouter(reporter, &logs, "EXPORT-ROW,ok-capability", false)

	response := performSensitiveRead(t, router)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "EXPORT-ROW") {
		t.Fatalf("business body not flushed: %s", response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); !strings.Contains(got, "text/csv") {
		t.Fatalf("buffered Content-Type lost after flush: %q", got)
	}
	if reporter.calls != 1 {
		t.Fatalf("Report calls = %d, want 1", reporter.calls)
	}
	if strings.Contains(logs.String(), "report platform audit failed") {
		t.Fatalf("unexpected failure log: %s", logs.String())
	}
}

// 强制审计 + 大体积流式导出超过缓冲上限：数据必须照常返回（内存安全优先），
// 同时留下降级 Warn 与审计失败 error 日志（含路由、请求 id、上限与实际字节数），
// 且不再对已提交的响应追加 503。
func TestPMAuditBufferOverflowFlushesResponseAndWarns(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	var logs bytes.Buffer
	reporter := &stubAuditReporterPM{err: errors.New("platform audit ingest unavailable")}
	payload := strings.Repeat("EXPORT-ROW-0123456789,", maxAuditBufferBytes/11) + "\nTAIL-MARKER"
	router := newSensitiveReadTestRouter(reporter, &logs, payload, false)

	response := performSensitiveRead(t, router)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (degraded response must be delivered); body head = %s", response.Code, http.StatusOK, response.Body.String()[:64])
	}
	body := response.Body.String()
	if len(body) != len(payload) || !strings.Contains(body, "TAIL-MARKER") {
		t.Fatalf("degraded response truncated: got %d bytes, want %d", len(body), len(payload))
	}
	if strings.Contains(body, "PM_AUDIT_WRITE_REJECTED") {
		t.Fatalf("503 appended onto an already committed response: %s", body[len(body)-128:])
	}
	if reporter.calls != 1 {
		t.Fatalf("Report calls = %d, want 1", reporter.calls)
	}
	logged := logs.String()
	if !strings.Contains(logged, "audit buffered response exceeded limit") {
		t.Fatalf("missing degrade warn log: %s", logged)
	}
	for _, field := range []string{"\"limit_bytes\":65536", "response_bytes", "GET " + sensitiveExportPath, "01JSENSITIVE0000000000000000"} {
		if !strings.Contains(logged, field) {
			t.Fatalf("degrade warn log missing %s: %s", field, logged)
		}
	}
	if !strings.Contains(logged, "report platform audit failed") {
		t.Fatalf("audit failure must still be logged after degrade: %s", logged)
	}
}

// 非强制模式：敏感读行为不变（不缓冲、不拒绝），审计失败仅记日志。
func TestPMSensitiveReadUnchangedWhenAuditNotRequired(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "false")
	var logs bytes.Buffer
	reporter := &stubAuditReporterPM{err: errors.New("platform audit ingest unavailable")}
	router := newSensitiveReadTestRouter(reporter, &logs, "EXPORT-ROW,unchanged", true)

	response := performSensitiveRead(t, router)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (business outcome unchanged); body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "EXPORT-ROW") {
		t.Fatalf("business body missing: %s", response.Body.String())
	}
	if !strings.Contains(logs.String(), "report platform audit failed") {
		t.Fatalf("missing report failure log: %s", logs.String())
	}
}
