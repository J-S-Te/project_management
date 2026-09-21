package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEvidenceGatewayUploadsAndBindsImmutableReceipt(t *testing.T) {
	var sessionCreated, ticketIssued, uploaded bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			_ = r.ParseForm()
			scope := r.Form.Get("scope")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-" + scope, "token_type": "Bearer", "scope": scope, "expires_in": 300})
		case "/api/v2/upload-sessions":
			if r.Header.Get("Authorization") != "Bearer token-platform:file:upload" {
				t.Fatalf("upload token=%q", r.Header.Get("Authorization"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["purpose"] != "project.field.evidence" || body["resource_id"] != "SI-1" || body["size_bytes"] != float64(8) {
				t.Fatalf("unexpected session payload %#v", body)
			}
			sessionCreated = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"upload_id": "UPLOAD-1", "file_id": "FILE-1"}})
		case "/api/v2/upload-sessions/UPLOAD-1/tickets":
			if r.Header.Get("Authorization") != "Bearer token-platform:file:upload" {
				t.Fatalf("ticket token=%q", r.Header.Get("Authorization"))
			}
			ticketIssued = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ticket": "once", "upload_url": "/file-gateway/api/v2/upload-sessions/UPLOAD-1/content"}})
		case "/api/v2/upload-sessions/UPLOAD-1/content":
			if r.Header.Get("Authorization") != "UploadTicket once" || r.ContentLength != 8 {
				t.Fatalf("upload authorization=%q length=%d", r.Header.Get("Authorization"), r.ContentLength)
			}
			content := new(bytes.Buffer)
			_, _ = content.ReadFrom(r.Body)
			if content.String() != "evidence" {
				t.Fatalf("uploaded content=%q", content.String())
			}
			uploaded = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	gateway := NewEvidenceFileGateway(server.URL, server.URL, "APP-1", "client", "secret")
	artifact, err := gateway.UploadEvidence(context.Background(), "REQ-1", "SI-1", "FIELD", "evidence.pdf", "application/pdf", bytes.NewBufferString("evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if !sessionCreated || !ticketIssued || !uploaded || artifact.FileID != "FILE-1" || artifact.FileName != "evidence.pdf" || len(artifact.SHA256) != 64 || artifact.Size != 8 {
		t.Fatalf("artifact=%+v session=%v ticket=%v uploaded=%v", artifact, sessionCreated, ticketIssued, uploaded)
	}
}
