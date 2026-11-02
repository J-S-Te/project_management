package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/httpapi"
)

func readinessRequest(t *testing.T, options httpapi.RouterOptions) *httptest.ResponseRecorder {
	t.Helper()
	handler := httpapi.NewRouter(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), options)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return response
}

// TestReadyzFailsWhenRequiredAuditDestinationMissing 覆盖 BIZ-XAPI-10 的审计 fail-closed：
// required 但未配置审计发布器 → 503，并明确暴露缺失目的地。
func TestReadyzFailsWhenRequiredAuditDestinationMissing(t *testing.T) {
	response := readinessRequest(t, httpapi.RouterOptions{
		IntegrationDestinations: &httpapi.IntegrationDestinationStatus{
			AuditRequired: true, AuditConfigured: false,
			NotificationRequired: true, NotificationConfigured: true,
		},
	})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Status      string                    `json:"status"`
			Degraded    []string                  `json:"degraded_destinations"`
			Integration map[string]map[string]any `json:"integrations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "not_ready" || strings.Join(payload.Data.Degraded, ",") != "PLATFORM_AUDIT" {
		t.Fatalf("readiness = %+v", payload.Data)
	}
	if payload.Data.Integration["audit"]["required"] != true || payload.Data.Integration["audit"]["configured"] != false {
		t.Fatalf("audit integration = %v", payload.Data.Integration["audit"])
	}
}

// TestReadyzDegradesWhenNotificationDestinationMissing 覆盖通知缺失只降级不阻断：
// 200 degraded，且集成状态与降级目的地可见。
func TestReadyzDegradesWhenNotificationDestinationMissing(t *testing.T) {
	response := readinessRequest(t, httpapi.RouterOptions{
		IntegrationDestinations: &httpapi.IntegrationDestinationStatus{
			AuditRequired: true, AuditConfigured: true,
			NotificationRequired: true, NotificationConfigured: false,
		},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 degraded; body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Status      string                    `json:"status"`
			Degraded    []string                  `json:"degraded_destinations"`
			Integration map[string]map[string]any `json:"integrations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "degraded" || strings.Join(payload.Data.Degraded, ",") != "PLATFORM_NOTIFICATION" {
		t.Fatalf("readiness = %+v", payload.Data)
	}
	if payload.Data.Integration["notification"]["configured"] != false {
		t.Fatalf("notification integration = %v", payload.Data.Integration["notification"])
	}
}

// TestReadyzIsReadyWhenIntegrationDestinationsAreConfigured 覆盖齐全时的 ready 基线。
func TestReadyzIsReadyWhenIntegrationDestinationsAreConfigured(t *testing.T) {
	response := readinessRequest(t, httpapi.RouterOptions{
		IntegrationDestinations: &httpapi.IntegrationDestinationStatus{
			AuditRequired: true, AuditConfigured: true,
			NotificationRequired: true, NotificationConfigured: true,
		},
	})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("status = %d body=%s, want ready", response.Code, response.Body.String())
	}
}
