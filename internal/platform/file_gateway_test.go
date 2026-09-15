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
	var uploaded, bound bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			_ = r.ParseForm()
			scope := r.Form.Get("scope")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-" + scope, "token_type": "Bearer", "scope": scope, "expires_in": 300})
		case "/api/v1/files":
			if r.Header.Get("Authorization") != "Bearer token-platform:file:upload" {
				t.Fatalf("upload token=%q", r.Header.Get("Authorization"))
			}
			if err := r.ParseMultipartForm(21 << 20); err != nil {
				t.Fatal(err)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			uploaded = true
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"file_id": "FILE-1"}})
		case "/api/v1/files/FILE-1/bindings":
			if r.Header.Get("Authorization") != "Bearer token-platform:file:bind" {
				t.Fatalf("bind token=%q", r.Header.Get("Authorization"))
			}
			bound = true
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
	if !uploaded || !bound || artifact.FileID != "FILE-1" || artifact.FileName != "evidence.pdf" || len(artifact.SHA256) != 64 || artifact.Size != 8 {
		t.Fatalf("artifact=%+v uploaded=%v bound=%v", artifact, uploaded, bound)
	}
}
