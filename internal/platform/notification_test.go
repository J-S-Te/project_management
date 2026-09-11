package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 站内信投递必须使用服务凭据，并按平台契约发送字段；幂等命中（200）同样视为成功。
func TestNotificationPublisherPostsIngestEvent(t *testing.T) {
	var received notificationIngestPayload
	var authHeader, contentType string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/token":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "scope": "notification.ingest", "expires_in": 300})
		case "/internal/v1/notifications/events":
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
	// 未指定 scope/priority 时回落到平台可接受的默认值。
	if received.NotificationScope != "application" || received.Priority != "NORMAL" {
		t.Fatalf("defaults not applied: %+v", received)
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
