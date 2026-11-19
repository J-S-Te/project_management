package httpapi_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

const workflowStreamPath = "/api/v1/workflow/stream"

type workflowTestRepository struct {
	repo
	revision atomic.Int64
	scopes   chan platform.ScopeFilter
}

// WorkflowVersion 为真实 HTTP 流提供可推进的隔离业务版本。
// 参数：ctx 为读取上下文，scope 为服务端权限范围，projectID 为当前项目。
// 返回值：确定性版本及读取结果。
func (r *workflowTestRepository) WorkflowVersion(ctx context.Context, scope platform.ScopeFilter, projectID string) (string, error) {
	if projectID != "P1" {
		return "", fmt.Errorf("unexpected project %q", projectID)
	}
	select {
	case r.scopes <- scope:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(r.revision.Load())))), nil
}

// TestWorkflowStreamFlushesCommittedChanges 校验无需重发读请求即可观察版本更新。
// 参数：t 为 Go 测试上下文。
// 返回值：无。
func TestWorkflowStreamFlushesCommittedChanges(t *testing.T) {
	r := &workflowTestRepository{scopes: make(chan platform.ScopeFilter, 8)}
	p := platform.Principal{
		TenantID: "tenant-1", UserID: "leader-1", IdentityID: "leader-1",
		Permissions: map[string]bool{"project.read": true},
		DataScopes:  []platform.DataScope{{ScopeType: "PROJECT", ScopeID: "P1"}},
	}
	shutdown := make(chan struct{})
	server := httptest.NewServer(httpapi.NewRouter(&application.Service{Repo: r},
		identity{p: p}, nil, slog.Default(), httpapi.RouterOptions{WorkflowShutdown: shutdown}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+workflowStreamPath+"?project_id=P1", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected an SSE response, got HTTP %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(res.Body)
	initial := readWorkflowFrame(t, reader, "ready")
	scope := <-r.scopes
	if scope.TenantID != p.TenantID || len(scope.ProjectIDs) != 1 || scope.ProjectIDs[0] != "P1" {
		t.Fatalf("unexpected repository scope: %+v", scope)
	}
	r.revision.Add(1)
	if changed := readWorkflowFrame(t, reader, "workflow-changed"); changed == initial {
		t.Fatal("same version emitted for a committed change")
	}
	close(shutdown)
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("expected shutdown to close the connection, got %v", err)
	}
}

// readWorkflowFrame 验证线上帧在 Flush 后可读取且不携带业务正文。
// 参数：t 为测试句柄，reader 为 SSE 响应，want 为期望事件名。
// 返回值：事件的数据行。
func readWorkflowFrame(t *testing.T, reader *bufio.Reader, want string) string {
	t.Helper()
	event, err := reader.ReadString('\n')
	if err != nil || event != "event: "+want+"\n" {
		t.Fatalf("expected %q, got %q: %v", want, event, err)
	}
	value, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(value, "data: ") || len(strings.TrimSpace(value)) != len("data: ")+64 {
		t.Fatalf("expected opaque revision, got %q: %v", value, err)
	}
	separator, err := reader.ReadString('\n')
	if err != nil || separator != "\n" {
		t.Fatalf("expected SSE frame separator, got %q: %v", separator, err)
	}
	return value
}

// TestWorkflowStreamRejectsMissingAuthorization 验证握手的会话和权限门禁。
// 参数：t 为 Go 测试上下文。
// 返回值：无。
func TestWorkflowStreamRejectsMissingAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		identity   identity
		statusCode int
	}{
		{name: "unauthenticated", identity: identity{err: platform.ErrUnauthenticated}, statusCode: http.StatusUnauthorized},
		{name: "no permission", statusCode: http.StatusForbidden},
		{name: "no data scope", identity: identity{p: platform.Principal{
			TenantID: "tenant-1", Permissions: map[string]bool{"project.read": true},
		}}, statusCode: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &workflowTestRepository{}
			handler := httpapi.NewRouter(&application.Service{Repo: r}, tc.identity, nil, slog.Default())
			got := perform(handler, http.MethodGet, workflowStreamPath, "")
			if got.Code != tc.statusCode {
				t.Fatalf("status %d, want %d", got.Code, tc.statusCode)
			}
		})
	}
}

type revocableWorkflowIdentity struct {
	principal platform.Principal
	revoked   atomic.Bool
}

// Authenticate 模拟真实流握手后的会话撤销。
// 参数：ctx 和请求由 HTTP 认证边界传入。
// 返回值：原主体或登录失效错误。
func (i *revocableWorkflowIdentity) Authenticate(_ context.Context, _ *http.Request) (platform.Principal, error) {
	if i.revoked.Load() {
		return platform.Principal{}, platform.ErrUnauthenticated
	}
	return i.principal, nil
}

// TestWorkflowStreamFailsClosedAfterRevocation 确认已经打开的授权流撤销后结束。
// 参数：t 为隔离 HTTP 测试上下文。
// 返回值：无。
func TestWorkflowStreamFailsClosedAfterRevocation(t *testing.T) {
	id := &revocableWorkflowIdentity{principal: platform.Principal{
		TenantID: "tenant-1", UserID: "leader-1", IdentityID: "leader-1",
		Permissions: map[string]bool{"project.read": true},
		DataScopes:  []platform.DataScope{{ScopeType: "PROJECT", ScopeID: "P1"}},
	}}
	r := &workflowTestRepository{scopes: make(chan platform.ScopeFilter, 4)}
	server := httptest.NewServer(httpapi.NewRouter(&application.Service{Repo: r}, id, nil, slog.Default()))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+workflowStreamPath+"?project_id=P1", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	readWorkflowFrame(t, reader, "ready")
	id.revoked.Store(true)
	r.revision.Add(1)
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("expected revoked stream to close without a change event, got %v", err)
	}
}
