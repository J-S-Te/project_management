package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// readyGatewayServer 模拟文件网关：会话创建按给定状态返回，并记录票据/内容接口是否被调用。
type readyGatewayServer struct {
	mu           sync.Mutex
	sessionCalls int
	ticketCalls  int
	contentCalls int
	status       string
	fileID       string
	sizeBytes    uint64
	digest       string
}

func (s *readyGatewayServer) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth2/token":
			_ = r.ParseForm()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-" + r.Form.Get("scope"), "token_type": "Bearer", "scope": r.Form.Get("scope"), "expires_in": 300})
		case r.URL.Path == "/api/v2/upload-sessions" && r.Method == http.MethodPost:
			s.mu.Lock()
			s.sessionCalls++
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"upload_id": "UPLOAD-1", "file_id": s.fileID, "status": s.status,
				"size_bytes": s.sizeBytes, "sha256": s.digest,
			}})
		case strings.HasSuffix(r.URL.Path, "/tickets"):
			s.mu.Lock()
			s.ticketCalls++
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ticket": "once", "upload_url": "/file-gateway/api/v2/upload-sessions/UPLOAD-1/content"}})
		case strings.HasSuffix(r.URL.Path, "/content"):
			s.mu.Lock()
			s.contentCalls++
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected gateway request %s %s", r.Method, r.URL.Path)
		}
	})
}

func readyGatewayFixture(status string, sizeBytes uint64, digest string) *readyGatewayServer {
	return &readyGatewayServer{status: status, fileID: "FILE-READY-1", sizeBytes: sizeBytes, digest: digest}
}

func testDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// TestUploadImportReusesReadySessionWithoutTicketOrPut 覆盖"预览失败/重复确认后再次导入"的场景：
// 网关按幂等键回放一个 READY 会话时，项目侧不得再申请票据或 PUT 内容（网关会以 409 拒绝），
// 而应直接把既有文件当作本次导入源文件。
func TestUploadImportReusesReadySessionWithoutTicketOrPut(t *testing.T) {
	content := "设备名称,能力编码\n设备,C1\n"
	server := readyGatewayFixture("READY", uint64(len(content)), testDigest(content))
	httpServer := httptest.NewServer(server.handler(t))
	defer httpServer.Close()
	gateway := NewEvidenceFileGateway(httpServer.URL, httpServer.URL, "APP-1", "client", "secret")
	requestID := "PMIMP-CAPABILITY-0123456789abcdef0123456789abcdef01234567"

	for attempt := 1; attempt <= 2; attempt++ {
		artifact, err := gateway.UploadImport(context.Background(), requestID, "CAPABILITY", "devices.csv", "text/csv", bytes.NewBufferString(content))
		if err != nil {
			t.Fatalf("attempt %d: ready session reuse failed: %v", attempt, err)
		}
		if artifact.FileID != "FILE-READY-1" || artifact.FileName != "devices.csv" || artifact.MIME != "text/csv" || artifact.Size != uint64(len(content)) || artifact.SHA256 != testDigest(content) {
			t.Fatalf("attempt %d: unexpected artifact %+v", attempt, artifact)
		}
	}
	if server.sessionCalls != 2 || server.ticketCalls != 0 || server.contentCalls != 0 {
		t.Fatalf("sessions=%d tickets=%d content=%d, want 2/0/0", server.sessionCalls, server.ticketCalls, server.contentCalls)
	}
}

// TestUploadImportFailsClosedOnNonResumableSessionStatus 断言 UPLOADING/FAILED/REJECTED/空 状态
// 都失败关闭，且不会去申请票据或 PUT 内容（避免 409 与"未知状态当成功"）。
func TestUploadImportFailsClosedOnNonResumableSessionStatus(t *testing.T) {
	content := "设备名称,能力编码\n设备,C1\n"
	for _, status := range []string{"UPLOADING", "FAILED", "REJECTED", ""} {
		server := readyGatewayFixture(status, uint64(len(content)), testDigest(content))
		httpServer := httptest.NewServer(server.handler(t))
		_, err := NewEvidenceFileGateway(httpServer.URL, httpServer.URL, "APP-1", "client", "secret").
			UploadImport(context.Background(), "PMIMP-CAPABILITY-0123456789abcdef0123456789abcdef01234567", "CAPABILITY", "devices.csv", "text/csv", bytes.NewBufferString(content))
		httpServer.Close()
		if err == nil || !strings.Contains(err.Error(), "cannot be resumed") {
			t.Fatalf("status %q must fail closed, got err=%v", status, err)
		}
		if server.ticketCalls != 0 || server.contentCalls != 0 {
			t.Fatalf("status %q: tickets=%d content=%d, want 0/0", status, server.ticketCalls, server.contentCalls)
		}
	}
}

// TestUploadImportRejectsReadySessionForDifferentFile 断言幂等键命中另一份文件时失败关闭：
// 即使会话 READY，声明的大小/摘要与本地重算值不一致也不能复用它。
func TestUploadImportRejectsReadySessionForDifferentFile(t *testing.T) {
	content := "设备名称,能力编码\n设备,C1\n"
	for _, test := range []struct {
		name   string
		size   uint64
		digest string
	}{{"digest", uint64(len(content)), testDigest("another file")}, {"size", uint64(len(content)) + 1, testDigest(content)}} {
		server := readyGatewayFixture("READY", test.size, test.digest)
		httpServer := httptest.NewServer(server.handler(t))
		_, err := NewEvidenceFileGateway(httpServer.URL, httpServer.URL, "APP-1", "client", "secret").
			UploadImport(context.Background(), "PMIMP-CAPABILITY-0123456789abcdef0123456789abcdef01234567", "CAPABILITY", "devices.csv", "text/csv", bytes.NewBufferString(content))
		httpServer.Close()
		if err == nil || !strings.Contains(err.Error(), "does not match the uploaded file") {
			t.Fatalf("%s mismatch must fail closed, got err=%v", test.name, err)
		}
		if server.ticketCalls != 0 || server.contentCalls != 0 {
			t.Fatalf("%s mismatch: tickets=%d content=%d, want 0/0", test.name, server.ticketCalls, server.contentCalls)
		}
	}
}
