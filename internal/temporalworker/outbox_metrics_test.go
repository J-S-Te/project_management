package temporalworker

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type stubOutboxBacklog struct {
	backlog OutboxBacklog
	err     error
	calls   atomic.Int64
}

func (stub *stubOutboxBacklog) CountBacklog(context.Context) (OutboxBacklog, error) {
	stub.calls.Add(1)
	return stub.backlog, stub.err
}

func renderMetrics(registry *MetricsRegistry) string {
	response := httptest.NewRecorder()
	registry.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	return response.Body.String()
}

// C2 有值态：积压被读到就暴露 pending/dead 两个 gauge。
func TestOutboxBacklogCollectorPublishesBacklogGauges(t *testing.T) {
	registry := NewMetricsRegistry()
	source := &stubOutboxBacklog{backlog: OutboxBacklog{Pending: 7, Dead: 2}}
	collector := &OutboxBacklogCollector{Source: source, Registry: registry}
	collector.collect(context.Background())
	body := renderMetrics(registry)
	if !strings.Contains(body, "project_audit_outbox_pending 7") || !strings.Contains(body, "project_audit_outbox_dead 2") {
		t.Fatalf("metrics body = %s", body)
	}
	if strings.Contains(body, "project_audit_outbox_query_errors_total") {
		t.Fatalf("query errors must stay absent on success: %s", body)
	}
}

// C2 零值态：空积压必须显式输出 0，而不是把 gauge 省掉。
func TestOutboxBacklogCollectorReportsZeroBacklogAsZero(t *testing.T) {
	registry := NewMetricsRegistry()
	collector := &OutboxBacklogCollector{Source: &stubOutboxBacklog{}, Registry: registry}
	collector.collect(context.Background())
	body := renderMetrics(registry)
	if !strings.Contains(body, "project_audit_outbox_pending 0") || !strings.Contains(body, "project_audit_outbox_dead 0") {
		t.Fatalf("metrics body = %s", body)
	}
}

// C2 失败态：查询失败不得沿用陈旧值；gauge 缺失 + 错误计数递增才表示"未知"。
func TestOutboxBacklogCollectorDropsGaugesOnQueryFailure(t *testing.T) {
	registry := NewMetricsRegistry()
	source := &stubOutboxBacklog{backlog: OutboxBacklog{Pending: 5, Dead: 1}}
	collector := &OutboxBacklogCollector{Source: source, Registry: registry}
	collector.collect(context.Background())
	if body := renderMetrics(registry); !strings.Contains(body, "project_audit_outbox_pending 5") {
		t.Fatalf("metrics body = %s", body)
	}
	source.err = errors.New("database unavailable")
	collector.collect(context.Background())
	body := renderMetrics(registry)
	if strings.Contains(body, "project_audit_outbox_pending") || strings.Contains(body, "project_audit_outbox_dead") {
		t.Fatalf("stale backlog gauges survived a failed query: %s", body)
	}
	if !strings.Contains(body, "project_audit_outbox_query_errors_total 1") {
		t.Fatalf("metrics body = %s", body)
	}
	collector.collect(context.Background())
	if body := renderMetrics(registry); !strings.Contains(body, "project_audit_outbox_query_errors_total 2") {
		t.Fatalf("metrics body = %s", body)
	}
}

// C2：采集循环由 ctx 取消退出，首次采集不必等一个周期。
func TestOutboxBacklogCollectorRunStopsOnContextCancel(t *testing.T) {
	registry := NewMetricsRegistry()
	source := &stubOutboxBacklog{backlog: OutboxBacklog{Pending: 3}}
	collector := &OutboxBacklogCollector{Source: source, Registry: registry, Interval: time.Hour, Timeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		collector.Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for source.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if source.calls.Load() == 0 {
		t.Fatal("collector did not collect immediately on start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collector Run did not stop after context cancel")
	}
}
