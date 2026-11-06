package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestJSONBodyLimitRejectsOversizedWriteBody 验证普通JSON写接口的请求体上限。
// 参数：t为HTTP层测试上下文。
// 返回值：无；超限请求未被413拒绝或正常请求被误拒时失败。
func TestJSONBodyLimitRejectsOversizedWriteBody(t *testing.T) {
	handler := router(t, map[string]bool{"project.create": true, "project.read": true}, nil)

	// 超过2MiB的合法JSON：必须在读取阶段被拒绝，而不是交给业务层解析。
	oversized := `{"name":"` + strings.Repeat("x", 3<<20) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(oversized))
	request.Header.Set("Content-Type", "application/json")
	// 同源保护（SEC-D11）要求写请求携带与 OIDC_REDIRECT_URI 一致的 Origin。
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Code != "PM_BODY_TOO_LARGE" {
		t.Fatalf("oversized body envelope=%s err=%v", response.Body.String(), err)
	}

	// 正常长度的请求体仍按业务校验通过，证明上限没有影响既有客户端。
	normal := perform(handler, http.MethodPost, "/api/v1/projects", `{"name":"新项目","customer":"伪造客户","contract":"FAKE-1","contract_version":"999","contract_id":"approved-1","service_items":[{"source_id":"SVC-1","site":"杭州机房","category":"伪造类别"}]}`)
	if normal.Code != http.StatusCreated {
		t.Fatalf("normal body status=%d body=%s", normal.Code, normal.Body.String())
	}
}
