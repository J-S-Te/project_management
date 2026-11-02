package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDispatchNotificationOutboxFailsWhenDestinationMissing 覆盖 BIZ-XAPI-10：
// 通知目的地缺失时不能静默返回 (0, nil)，必须留下可重试/可告警的明确错误，
// 且不得认领任何 outbox 行（积压保留在本地）。
func TestDispatchNotificationOutboxFailsWhenDestinationMissing(t *testing.T) {
	repo := &boundaryOutboxRepo{}
	service := &Service{Repo: repo} // Notifications 为 nil：目的未配置
	sent, err := service.DispatchNotificationOutbox(context.Background(), "tenant-a", "owner-1", 1, time.Now().UTC())
	if sent != 0 {
		t.Fatalf("sent = %d, want 0", sent)
	}
	if !errors.Is(err, ErrNotificationDestinationUnavailable) {
		t.Fatalf("error = %v, want ErrNotificationDestinationUnavailable", err)
	}
	if repo.calls != 0 {
		t.Fatalf("repository claimed %d items, want 0 while destination is missing", repo.calls)
	}
}
