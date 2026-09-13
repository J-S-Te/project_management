package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 站内信投递必须使用服务凭据，并按平台契约发送字段；幂等命中（200）同样视为成功。
//
// 注意：本用例此前把 endpoint 桩在 /internal/v1/notifications/events、并断言
// notification_scope == "application"，等于把实现里的两处错误固化成期望值——
// 平台没有 /internal/v1 路由（必然 404），也不接受 "application" 这个 scope（必然 400）。
// 现在按平台真实契约断言，路径与 scope 任一写错都会立刻失败。
func TestNotificationPublisherPostsIngestEvent(t *testing.T) {
	var received notificationIngestPayload
	var authHeader, contentType string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "scope": "notification.ingest", "expires_in": 300})
		case "/api/v1/notifications/events":
			authHeader = request.Header.Get("Authorization")
			contentType = request.Header.Get("Content-Type")
			if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			writer.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	publisher := NewNotificationPublisher(server.URL, "", "pm-client", "pm-secret", "")
	if publisher == nil {
		t.Fatal("publisher must be constructed with full credentials")
	}
	err := publisher.Publish(context.Background(), NotificationEvent{
		EventID: "evt-1", EventType: "AUTOMATION_TRIGGERED", Title: "标题", Content: "内容",
		Recipients: []string{"u-1", "u-1", " u-2 ", ""}, IdempotencyKey: "key-1",
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if authHeader != "Bearer token" || contentType != "application/json" {
		t.Fatalf("auth=%q content-type=%q", authHeader, contentType)
	}
	if len(received.Recipients) != 2 {
		t.Fatalf("recipients must be trimmed and deduplicated: %+v", received.Recipients)
	}
	if received.EventType != "AUTOMATION_TRIGGERED" || received.IdempotencyKey != "key-1" || received.OccurredAt.IsZero() {
		t.Fatalf("payload = %+v", received)
	}
	// 未指定 scope 时回落到平台白名单默认值 CROSS_SYSTEM（绝不能是 "application"）。
	if received.NotificationScope != NotificationScopeCrossSystem || received.Priority != "NORMAL" {
		t.Fatalf("defaults not applied: %+v", received)
	}
}

// 超长收件人（超过平台长度上限）必须被过滤：平台对任一非法收件人会判整条事件非法，
// 把整条通知一起丢弃，因此不能因为一个脏标识让全部收件人都收不到。
func TestNotificationPublisherDropsOverlongRecipients(t *testing.T) {
	var received notificationIngestPayload
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth2/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "scope": "notification.ingest", "expires_in": 300})
			return
		}
		_ = json.NewDecoder(request.Body).Decode(&received)
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	publisher := NewNotificationPublisher(server.URL, "", "pm-client", "pm-secret", "")
	valid := "01ARZ3NDEKTSV4RRFFQ69G5FAV" // 26 位 ULID，平台用户 ID 的长度上限
	overlong := valid + "0123456789"
	if err := publisher.Publish(context.Background(), NotificationEvent{
		EventID: "evt-2", EventType: "TEAM_ASSIGNED", Title: "标题", Content: "内容",
		Recipients: []string{valid, overlong},
	}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if len(received.Recipients) != 1 || received.Recipients[0] != valid {
		t.Fatalf("超长收件人必须被过滤，实际 %+v", received.Recipients)
	}
}

// 收件人为空时不发请求：平台会把它当作无人接收的事件。
func TestNotificationPublisherSkipsEmptyRecipients(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	publisher := NewNotificationPublisher(server.URL, "", "pm-client", "pm-secret", "")
	if err := publisher.Publish(context.Background(), NotificationEvent{EventID: "evt-1", Recipients: []string{"", "  "}}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if called {
		t.Fatal("empty recipient set must not call the platform")
	}
}

// 缺少凭据时返回 nil，未开通该集成的部署继续可用。
func TestNotificationPublisherRequiresCredentials(t *testing.T) {
	if publisher := NewNotificationPublisher("", "", "", "", ""); publisher != nil {
		t.Fatal("publisher must be nil without credentials")
	}
	if publisher := NewNotificationPublisher("http://platform", "", "client", "", ""); publisher != nil {
		t.Fatal("publisher must be nil without a secret")
	}
}
