package auditoutbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/platform"
)

type fakeStore struct {
	entries   []Entry
	delivered []string
	failed    []string
	retryAt   []time.Time
}

func (f *fakeStore) Claim(context.Context, string, int, time.Time) ([]Entry, error) {
	entries := f.entries
	f.entries = nil
	return entries, nil
}
func (f *fakeStore) MarkDelivered(_ context.Context, id string, _ time.Time) error {
	f.delivered = append(f.delivered, id)
	return nil
}
func (f *fakeStore) MarkFailed(_ context.Context, id, _ string, retryAt, _ time.Time) error {
	f.failed = append(f.failed, id)
	f.retryAt = append(f.retryAt, retryAt)
	return nil
}

type fakeReporter struct {
	err    error
	events []platform.AuditEvent
}

func (f *fakeReporter) Report(_ context.Context, event platform.AuditEvent) error {
	f.events = append(f.events, event)
	return f.err
}

// 平台不可用时事件必须保留重试，并带上来源信息；业务早已提交，审计不能丢。
func TestDeliverRetriesWithProvenance(t *testing.T) {
	store := &fakeStore{entries: []Entry{{ID: "row-1", EventID: "event-1", Action: "PROJECT_MANAGEMENT:DELIVERED", ClientIP: "10.8.0.5", IPSource: "TRUSTED_PROXY", Result: "SUCCESS"}}}
	reporter := &fakeReporter{err: errors.New("platform down")}
	dispatcher := &Dispatcher{Store: store, Reporter: reporter, WorkerID: "w1", BatchSize: 10}

	dispatcher.deliverOnce(context.Background())

	if len(store.failed) != 1 || len(store.delivered) != 0 {
		t.Fatalf("failed=%v delivered=%v", store.failed, store.delivered)
	}
	if len(reporter.events) != 1 || reporter.events[0].ClientIP != "10.8.0.5" || reporter.events[0].IPSource != "TRUSTED_PROXY" {
		t.Fatalf("events=%#v", reporter.events)
	}
}

func TestDeliverMarksSuccess(t *testing.T) {
	store := &fakeStore{entries: []Entry{{ID: "row-2", EventID: "event-2", Action: "A", Result: "SUCCESS"}}}
	dispatcher := &Dispatcher{Store: store, Reporter: &fakeReporter{}, WorkerID: "w1"}

	dispatcher.deliverOnce(context.Background())

	if len(store.delivered) != 1 || len(store.failed) != 0 {
		t.Fatalf("delivered=%v failed=%v", store.delivered, store.failed)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	dispatcher := &Dispatcher{MaxBackoff: 16 * time.Second}
	previous := time.Duration(0)
	for attempt := uint(1); attempt <= 8; attempt++ {
		value := dispatcher.backoff(attempt)
		if value <= 0 || value > dispatcher.MaxBackoff {
			t.Fatalf("backoff(%d)=%s", attempt, value)
		}
		if previous != 0 && value < previous && value != dispatcher.MaxBackoff {
			t.Fatalf("backoff 必须单调不减：%s -> %s", previous, value)
		}
		previous = value
	}
}
