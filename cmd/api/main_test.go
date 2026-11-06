package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/shutdown"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, nil))
}

// AUD-2026-029：收到停机信号后必须排空在途请求再退出，
// 而不是像原来那样由 ListenAndServe 直接断连。
func TestServeWithGracefulShutdownDrainsInFlightRequests(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}

	handlerEntered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Addr: addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(handlerEntered)
		<-release
		w.WriteHeader(http.StatusOK)
	})}

	shutdown := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- serveWithGracefulShutdown(server, shutdown, quietLogger()) }()

	waitForServer(t, addr)

	responseCh := make(chan *http.Response, 1)
	go func() {
		response, err := http.Get("http://" + addr + "/")
		if err != nil {
			t.Logf("request error: %v", err)
			responseCh <- nil
			return
		}
		responseCh <- response
	}()

	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the handler")
	}

	// 请求仍在途时发出停机信号：Shutdown 必须等待排空而不是断连。
	close(shutdown)
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("serveWithGracefulShutdown returned before in-flight request finished: %v", err)
	default:
	}

	close(release)
	var response *http.Response
	select {
	case response = <-responseCh:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request was cut by shutdown instead of drained")
	}
	if response == nil {
		t.Fatal("in-flight request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serveWithGracefulShutdown = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveWithGracefulShutdown did not return after shutdown")
	}
}

// ListenAndServe 自身的错误（端口被占用）必须原样上抛，不能被吞掉。
func TestServeWithGracefulShutdownPropagatesServeError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	addr := listener.Addr().String()

	never := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveWithGracefulShutdown(&http.Server{Addr: addr, Handler: http.NewServeMux()}, never, quietLogger())
	}()
	select {
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error = %v, want a real listener failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve error was not propagated")
	}
}

// waitForServer 轮询直到服务开始接受连接（ListenAndServe 在独立 goroutine 中启动）。
func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server on %s never accepted connections", addr)
}

// B2：退出时先取消后台上下文，再按预算 join 后台 goroutine；
// 预算内没退出必须点名并返回错误（main 据此非零退出）。
func TestDrainRuntimeJoinsBackgroundGoroutines(t *testing.T) {
	group := shutdown.NewGroup()
	joined := make(chan struct{})
	cancel := func() { close(joined) }
	group.Go("audit-outbox-dispatcher", func() { <-joined })
	if err := drainRuntime(context.Background(), cancel, group, time.Second, quietLogger()); err != nil {
		t.Fatalf("drainRuntime = %v, want nil", err)
	}
}

func TestDrainRuntimeNamesPendingGoroutinesOnBudgetExceeded(t *testing.T) {
	group := shutdown.NewGroup()
	release := make(chan struct{})
	defer close(release)
	group.Go("stuck-notifier", func() { <-release })
	err := drainRuntime(context.Background(), func() {}, group, 50*time.Millisecond, quietLogger())
	if err == nil || !strings.Contains(err.Error(), "stuck-notifier") {
		t.Fatalf("drainRuntime = %v, want deadline error naming stuck-notifier", err)
	}
}
