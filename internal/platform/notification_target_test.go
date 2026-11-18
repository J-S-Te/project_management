package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNotificationPublisherPreservesApprovalTarget 验证通知摄取请求保留可直达的审批地址。
// 参数：t 为测试上下文，外部平台仅以 HTTP 边界替身接收真实请求。
// 返回值：无；平台收不到 target_url 时失败。
func TestNotificationPublisherPreservesApprovalTarget(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != notificationIngestPath {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "test-token", "expires_in": 300, "scope": "notification.ingest", "token_type": "Bearer",
			})
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	var event NotificationEvent
	_ = json.Unmarshal([]byte(`{"EventID":"event","TargetURL":"/project_management/approvals?request_id=request","Recipients":["director"]}`), &event)
	publisher := NewNotificationPublisher(server.URL, "", "test-client", "test-secret", "")
	if err := publisher.Publish(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if received["target_url"] != "/project_management/approvals?request_id=request" {
		t.Fatalf("target_url=%v", received["target_url"])
	}
}
