package typesafe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

func TestTriageDeviationReturnsOnlyTypedProbabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Fatalf("authorization header = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "jev-latest" {
			t.Fatalf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"safety_risk":{"type":"noul","noul":0.91},"compliance_risk":{"type":"noul","noul":0.72},"delivery_blocked":{"type":"noul","noul":0.83},"severity_understated":{"type":"noul","noul":0.64}}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-secret", time.Second)
	result, err := client.TriageDeviation(context.Background(), domain.DeviationInput{Description: "设备检定过期，现场工作暂停", Severity: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "ADVISORY_ONLY" || result.Model != "jev-1.13.0" || result.Probabilities["safety_risk"] != 0.91 {
		t.Fatalf("result = %+v", result)
	}
}

func TestTriageDeviationRejectsIncompleteTypedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{}}`))
	}))
	defer server.Close()
	_, err := NewClient(server.URL, "test-secret", time.Second).TriageDeviation(context.Background(), domain.DeviationInput{Description: "偏差"})
	if err == nil {
		t.Fatal("expected invalid response error")
	}
}
