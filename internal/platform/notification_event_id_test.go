package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNotificationPublisherSendsApprovalEventWithValidSourceID 验证审批通知满足平台事件编号契约。
// 参数：t 为当前测试上下文。
// 返回值：无；编号不合法、重试身份变化或收件人变化时测试失败。
func TestNotificationPublisherSendsApprovalEventWithValidSourceID(t *testing.T) {
	const (
		approvalID = "01ARZ3NDEKTSV4RRFFQ69G5FAV-approval"
		recipient  = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	)
	inbox := make(chan notificationIngestPayload, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token", "token_type": "Bearer",
				"scope": "notification.ingest", "expires_in": 300,
			})
		case notificationIngestPath:
			var payload notificationIngestPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if payload.EventID != "pm-event-"+approvalID && payload.EventID != "approval-batch-example" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			inbox <- payload
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	publisher := NewNotificationPublisher(server.URL, "", "pm-client", "pm-secret", "")
	for _, scenario := range []struct {
		name, eventID, eventType, key, wantID, wantKey string
	}{
		{"回退驳回", approvalID, "ROLLBACK_REJECTED", approvalID, "pm-event-" + approvalID, approvalID},
		{"驳回重试", approvalID, "ROLLBACK_REJECTED", approvalID, "pm-event-" + approvalID, approvalID},
		{"更正驳回缺省去重键", approvalID, "REPORT_CORRECTION_REJECTED", "", "pm-event-" + approvalID, approvalID},
		{"已有合法编号", "approval-batch-example", "REPORT_CORRECTION_REJECTED", "", "approval-batch-example", "approval-batch-example"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			err := publisher.Publish(t.Context(), NotificationEvent{
				EventID: scenario.eventID, EventType: scenario.eventType,
				IdempotencyKey: scenario.key, Title: "申请已驳回", Content: "请查看审批意见。",
				ReferenceType: "project_approval", ReferenceID: "01ARZ3NDEKTSV4RRFFQ69G5FAT",
				Recipients: []string{recipient},
			})
			if err != nil {
				t.Fatal(err)
			}
			var data notificationIngestPayload
			select {
			case data = <-inbox:
			default:
				t.Fatal("未收到审批通知请求")
			}
			if data.EventID != scenario.wantID || data.IdempotencyKey != scenario.wantKey ||
				data.EventType != scenario.eventType || len(data.Recipients) != 1 ||
				data.Recipients[0] != recipient {
				t.Fatalf("审批通知载荷不符合预期: %+v", data)
			}
		})
	}
}
