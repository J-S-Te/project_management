package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestAuditReporterMatchesPlatformCorrelationContract(t *testing.T) {
	var event map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/oauth2/token":
			clientID, secret, ok := r.BasicAuth()
			if !ok || clientID != "client" || secret != "secret" || r.FormValue("grant_type") != "client_credentials" || r.FormValue("scope") != "audit.ingest" {
				t.Fatalf("invalid token request")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"token","token_type":"Bearer","expires_in":3600,"scope":"audit.ingest"}`))}, nil
		case "/api/v1/audit/events":
			if got := r.Header.Get("X-Request-ID"); got != "01J00000000000000000000001" {
				t.Fatalf("X-Request-ID = %q", got)
			}
			if got := r.Header.Get("X-Correlation-ID"); got != "01J00000000000000000000002" {
				t.Fatalf("X-Correlation-ID = %q", got)
			}
			if got := r.Header.Get("traceparent"); len(got) != 55 {
				t.Fatalf("traceparent = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"OK"}`))}, nil
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		return nil, nil
	})

	service := newServiceClient("http://platform.test", "client", "secret")
	service.client.Transport = transport
	reporter := &auditClient{service: service, applicationCode: "project_management", environmentCode: "dev"}
	err := reporter.Report(context.Background(), AuditEvent{
		ActorID: "user-1", Action: "PROJECT_MANAGEMENT:POST", ResourceType: "PROJECT",
		RequestID: "01J00000000000000000000001", CorrelationID: "01J00000000000000000000002", Result: "SUCCESS",
	})
	if err != nil {
		t.Fatal(err)
	}
	if event["request_id"] != "01J00000000000000000000001" || event["correlation_id"] != "01J00000000000000000000002" {
		t.Fatalf("unexpected correlation payload: %#v", event)
	}
}

func TestAuditReporterRequiresCompleteSourceIdentity(t *testing.T) {
	if NewAuditReporter("", "client", "secret", "project_management", "dev") != nil {
		t.Fatal("reporter accepted missing platform base URL")
	}
	if NewAuditReporter("http://platform", "client", "secret", "", "dev") != nil {
		t.Fatal("reporter accepted missing application code")
	}
	if NewAuditReporter("http://platform", "client", "secret", "project_management", "") != nil {
		t.Fatal("reporter accepted missing environment code")
	}
}

func TestCheckAuditReporterConfigurationReportsMissingFieldsWithoutCredentials(t *testing.T) {
	status := CheckAuditReporterConfiguration("http://platform", "", "", "project_management", "dev")
	if status.Enabled {
		t.Fatal("audit reporter unexpectedly enabled")
	}
	got := strings.Join(status.MissingFields, ",")
	if got != "PLATFORM_AUDIT_CLIENT_ID,PLATFORM_AUDIT_CLIENT_SECRET" {
		t.Fatalf("missing fields = %q", got)
	}
}
