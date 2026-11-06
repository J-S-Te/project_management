package shutdown

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// B2：登记的后台 goroutine 在退出时必须被 join，而不是随进程消失。
func TestGroupDrainWaitsForRegisteredGoroutines(t *testing.T) {
	group := NewGroup()
	first := make(chan struct{})
	second := make(chan struct{})
	group.Go("audit-outbox-dispatcher", func() {
		defer close(first)
		time.Sleep(20 * time.Millisecond)
	})
	group.Go("audit-outbox-backlog-collector", func() {
		defer close(second)
		time.Sleep(10 * time.Millisecond)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := group.Drain(ctx); err != nil {
		t.Fatalf("Drain = %v, want nil", err)
	}
	for name, done := range map[string]chan struct{}{"audit-outbox-dispatcher": first, "audit-outbox-backlog-collector": second} {
		select {
		case <-done:
		default:
			t.Fatalf("%s was not joined before Drain returned", name)
		}
	}
}

// B2：超预算时必须点名未退出的 goroutine，而不是静默继续退出。
func TestGroupDrainNamesPendingGoroutinesOnDeadline(t *testing.T) {
	group := NewGroup()
	release := make(chan struct{})
	group.Go("stuck-notifier", func() { <-release })
	group.Go("finished", func() {})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := group.Drain(ctx)
	if err == nil {
		t.Fatal("Drain = nil, want deadline error")
	}
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Drain error = %T, want *TimeoutError", err)
	}
	if len(timeout.Pending) != 1 || timeout.Pending[0] != "stuck-notifier" {
		t.Fatalf("pending = %v, want [stuck-notifier]", timeout.Pending)
	}
	if message := err.Error(); !strings.Contains(message, "stuck-notifier") || !strings.Contains(message, "50ms") {
		t.Fatalf("error message = %q, want name and budget", message)
	}
	close(release)
}

// B2：预算默认 20s，环境变量按秒覆盖；非法值回落默认。
func TestBudgetDefaultsAndOverrides(t *testing.T) {
	t.Setenv(DrainBudgetEnv, "")
	if got := Budget(); got != DefaultDrainBudget {
		t.Fatalf("default Budget = %v, want %v", got, DefaultDrainBudget)
	}
	if DefaultDrainBudget != 20*time.Second {
		t.Fatalf("DefaultDrainBudget = %v, want 20s", DefaultDrainBudget)
	}
	t.Setenv(DrainBudgetEnv, "7")
	if got := Budget(); got != 7*time.Second {
		t.Fatalf("override Budget = %v, want 7s", got)
	}
	for _, invalid := range []string{"not-a-number", "0", "-3"} {
		t.Setenv(DrainBudgetEnv, invalid)
		if got := Budget(); got != DefaultDrainBudget {
			t.Fatalf("Budget(%q) = %v, want default %v", invalid, got, DefaultDrainBudget)
		}
	}
}

// B2：无登记任务时 Drain 立即返回，不等待预算。
func TestGroupDrainWithoutTasksReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if err := NewGroup().Drain(ctx); err != nil {
		t.Fatalf("Drain = %v, want nil", err)
	}
	if names := NewGroup().Names(); len(names) != 0 {
		t.Fatalf("Names = %v, want empty", names)
	}
}
