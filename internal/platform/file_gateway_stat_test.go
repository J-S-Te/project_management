package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// evidenceMetadataResponse 是文件网关只读证据元数据端点的成功信封。
func evidenceMetadataResponse(overrides map[string]any) map[string]any {
	data := map[string]any{
		"file_id": "FILE-1", "tenant_id": "TENANT-1", "application_id": "APP-PROJECT",
		"status": "READY", "media_type": "image/png", "original_name": "field.png",
		"sha256": strings.Repeat("a", 64), "size_bytes": 128, "binding_active": true,
		"created_at": "2026-10-30T09:00:00Z",
	}
	for key, value := range overrides {
		if value == nil {
			delete(data, key)
			continue
		}
		data[key] = value
	}
	return map[string]any{"code": "OK", "message": "ok", "data": data}
}

// newEvidenceStatServer 起一个假文件网关：token 端点按 scope 发令牌，元数据端点回放给定响应。
func newEvidenceStatServer(t *testing.T, status int, payload any, assert func(*http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			_ = r.ParseForm()
			scope := r.Form.Get("scope")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-" + scope, "token_type": "Bearer", "scope": scope, "expires_in": 300})
		case "/api/v1/files/FILE-1/evidence-metadata":
			if assert != nil {
				assert(r)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if payload != nil {
				_ = json.NewEncoder(w).Encode(payload)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
}

// TestEvidenceGatewayStatEvidenceReturnsGatewayMetadata 断言只读核验走
// platform:file:download 机器令牌，携带对象级绑定的查询参数，并原样返回网关解析到的元数据。
func TestEvidenceGatewayStatEvidenceReturnsGatewayMetadata(t *testing.T) {
	server := newEvidenceStatServer(t, http.StatusOK, evidenceMetadataResponse(nil), func(r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-platform:file:download" {
			t.Fatalf("authorization = %q", got)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s", r.Method)
		}
		// 绑定必须精确到业务对象：只发 file_id 会退化成"应用级绑定"，无法阻止跨项复用。
		query := r.URL.Query()
		if query.Get("resource_type") != "PROJECT_SERVICE_ITEM" || query.Get("resource_id") != "SI-EVIDENCE-1" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
	})
	defer server.Close()
	gateway := NewEvidenceFileGateway(server.URL, server.URL, "APP-PROJECT", "client", "secret")
	metadata, err := gateway.StatEvidence(context.Background(), "FILE-1", "PROJECT_SERVICE_ITEM", "SI-EVIDENCE-1")
	if err != nil {
		t.Fatalf("StatEvidence: %v", err)
	}
	if metadata.FileID != "FILE-1" || metadata.TenantID != "TENANT-1" || metadata.ApplicationID != "APP-PROJECT" ||
		metadata.Status != "READY" || metadata.MediaType != "image/png" || metadata.SHA256 != strings.Repeat("a", 64) ||
		metadata.SizeBytes != 128 || !metadata.BindingActive || metadata.CreatedAt.IsZero() {
		t.Fatalf("metadata = %+v", metadata)
	}
}

// TestEvidenceGatewayStatEvidenceFailsClosed 断言网关拒绝、故障或响应不完整时，
// 客户端分别返回"文件无效"与"网关不可用"两类稳定错误，绝不返回零值成功。
func TestEvidenceGatewayStatEvidenceFailsClosed(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		payload    any
		wantTarget error
	}{
		{name: "file not found", status: http.StatusNotFound, payload: map[string]any{"code": "NOT_FOUND", "message": "文件证据元数据不存在"}, wantTarget: ErrEvidenceFileUnavailable},
		{name: "no active binding", status: http.StatusConflict, payload: map[string]any{"code": "CONFLICT"}, wantTarget: ErrEvidenceFileUnavailable},
		{name: "forbidden", status: http.StatusForbidden, payload: map[string]any{"code": "FORBIDDEN"}, wantTarget: ErrEvidenceGatewayUnavailable},
		{name: "gateway failure", status: http.StatusInternalServerError, payload: map[string]any{"code": "INTERNAL"}, wantTarget: ErrEvidenceGatewayUnavailable},
		{name: "other application", status: http.StatusOK, payload: evidenceMetadataResponse(map[string]any{"application_id": "APP-CONTRACT"}), wantTarget: ErrEvidenceFileUnavailable},
		{name: "incomplete metadata", status: http.StatusOK, payload: evidenceMetadataResponse(map[string]any{"sha256": nil}), wantTarget: ErrEvidenceGatewayUnavailable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := newEvidenceStatServer(t, testCase.status, testCase.payload, nil)
			defer server.Close()
			gateway := NewEvidenceFileGateway(server.URL, server.URL, "APP-PROJECT", "client", "secret")
			metadata, err := gateway.StatEvidence(context.Background(), "FILE-1", "PROJECT_SERVICE_ITEM", "SI-EVIDENCE-1")
			if !errors.Is(err, testCase.wantTarget) {
				t.Fatalf("error = %v, want %v", err, testCase.wantTarget)
			}
			if metadata.FileID != "" || metadata.SHA256 != "" || metadata.SizeBytes != 0 {
				t.Fatalf("failed verification returned metadata: %+v", metadata)
			}
		})
	}
}

// TestEvidenceGatewayStatEvidenceRequiresFileAndResource 断言空文件 ID 或空业务对象标识
// 在发出请求前被拒绝：缺少对象标识就无法做对象级绑定校验。
func TestEvidenceGatewayStatEvidenceRequiresFileAndResource(t *testing.T) {
	gateway := NewEvidenceFileGateway("http://platform.example", "http://gateway.example", "APP-PROJECT", "client", "secret")
	for _, testCase := range []struct {
		name                             string
		fileID, resourceType, resourceID string
	}{
		{name: "missing file id", fileID: "  ", resourceType: "PROJECT_SERVICE_ITEM", resourceID: "SI-EVIDENCE-1"},
		{name: "missing resource type", fileID: "FILE-1", resourceID: "SI-EVIDENCE-1"},
		{name: "missing resource id", fileID: "FILE-1", resourceType: "PROJECT_SERVICE_ITEM", resourceID: "   "},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := gateway.StatEvidence(context.Background(), testCase.fileID, testCase.resourceType, testCase.resourceID); !errors.Is(err, ErrEvidenceFileUnavailable) {
				t.Fatalf("error = %v, want ErrEvidenceFileUnavailable", err)
			}
		})
	}
}
