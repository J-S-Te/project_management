package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	notificationIngestScope       = "notification.ingest"
	notificationIngestPath        = "/internal/v1/notifications/events"
	maximumNotificationRespBytes  = 64 << 10
)

// NotificationEvent 是投递给基础平台统一站内信 outbox 的一条事件。
// 租户与来源由服务凭据决定，不接受调用方指定，避免子系统伪造来源。
type NotificationEvent struct {
	EventID        string
	EventType      string
	Scope          string
	Priority       string
	Title          string
	Content        string
	ReferenceType  string
	ReferenceID    string
	Recipients     []string
	OccurredAt     time.Time
	IdempotencyKey string
}

// NotificationPublisher 投递平台站内信；未开通该集成时实现为 nil。
type NotificationPublisher interface {
	Publish(context.Context, NotificationEvent) error
}

type notificationPublisher struct {
	service  *serviceClient
	endpoint string
	scope    string
}

// NewNotificationPublisher 在缺少凭据时返回 nil，使尚未开通该集成的部署继续可用。
// 与负责人目录一致：endpoint 为空时回退到平台基址上的内部站内信路径。
func NewNotificationPublisher(baseURL, endpoint, clientID, clientSecret, scope string) NotificationPublisher {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" && baseURL != "" {
		endpoint = baseURL + notificationIngestPath
	}
	if baseURL == "" || endpoint == "" || strings.TrimSpace(clientID) == "" || clientSecret == "" {
		return nil
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = notificationIngestScope
	}
	return &notificationPublisher{service: newServiceClient(baseURL, clientID, clientSecret), endpoint: endpoint, scope: scope}
}

type notificationIngestPayload struct {
	EventID           string     `json:"event_id"`
	EventType         string     `json:"event_type"`
	NotificationScope string     `json:"notification_scope"`
	Priority          string     `json:"priority"`
	Title             string     `json:"title"`
	Content           string     `json:"content"`
	ReferenceType     string     `json:"reference_type"`
	ReferenceID       string     `json:"reference_id"`
	IdempotencyKey    string     `json:"idempotency_key"`
	Recipients        []string   `json:"recipient_user_ids"`
	OccurredAt        time.Time  `json:"occurred_at"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
}

// Publish 投递一条站内信事件。平台按 idempotency_key 幂等，重复投递返回 200 而非 201，
// 两者都视为成功。收件人为空时直接跳过：平台会把它当作无人接收的事件。
func (c *notificationPublisher) Publish(ctx context.Context, event NotificationEvent) error {
	recipients := make([]string, 0, len(event.Recipients))
	seen := make(map[string]struct{}, len(event.Recipients))
	for _, id := range event.Recipients {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		recipients = append(recipients, id)
	}
	if len(recipients) == 0 {
		return nil
	}
	occurredAt := event.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	idempotencyKey := strings.TrimSpace(event.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = event.EventID
	}
	payload := notificationIngestPayload{
		EventID: event.EventID, EventType: strings.TrimSpace(event.EventType),
		NotificationScope: firstNonBlankNotificationValue(event.Scope, "application"),
		Priority:          strings.ToUpper(firstNonBlankNotificationValue(event.Priority, "NORMAL")),
		Title:             strings.TrimSpace(event.Title), Content: strings.TrimSpace(event.Content),
		ReferenceType: strings.TrimSpace(event.ReferenceType), ReferenceID: strings.TrimSpace(event.ReferenceID),
		IdempotencyKey: idempotencyKey, Recipients: recipients, OccurredAt: occurredAt.UTC(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode notification event: %w", err)
	}
	token, err := c.service.token(ctx, c.scope)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.service.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumNotificationRespBytes))
	// 201 = 新事件，200 = 幂等命中，两者都表示平台已接收。
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return fmt.Errorf("platform notification ingest returned %d", response.StatusCode)
	}
	return nil
}

func firstNonBlankNotificationValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
