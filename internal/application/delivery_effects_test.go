package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type pagedEffectsDirectory struct {
	users        int
	failPage     int
	calls        []int
	inconsistent bool
}

func (d *pagedEffectsDirectory) List(_ context.Context, q platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	d.calls = append(d.calls, q.Page)
	if q.Page == d.failPage {
		return platform.OwnerDirectoryPage{}, errors.New("directory offline")
	}
	rows := []platform.OwnerDirectoryUser{}
	for i := (q.Page - 1) * q.PageSize; i < min(q.Page*q.PageSize, d.users); i++ {
		rows = append(rows, platform.OwnerDirectoryUser{UserID: fmt.Sprintf("user-%04d", i)})
	}
	total := d.users
	if d.inconsistent && q.Page > 1 {
		total++
	}
	return platform.OwnerDirectoryPage{Items: rows, Page: q.Page, PageSize: q.PageSize, Total: int64(total)}, nil
}

func TestDeliveryEffectsResolveAllPagesAndStableBatches(t *testing.T) {
	directory := &pagedEffectsDirectory{users: 601}
	service := &Service{Personnel: directory}
	effects := SnapshotDeliveryEffects(domain.DeliveryEvent{ID: "source-1", TenantID: "tenant-1", ProjectID: "PJ-1", Type: EventDeviationReported, CreatedAt: time.Now().UTC()}, []domain.Rule{{Enabled: true, Trigger: EventDeviationReported, Target: "technical_director"}}, nil)
	events, messages, err := service.buildDeliveryEffects(context.Background(), effects)
	if err != nil {
		t.Fatal(err)
	}
	if len(directory.calls) != 13 || len(events) != 1 || len(messages) != 2 || len(messages[0].Recipients) != 500 || len(messages[1].Recipients) != 101 {
		t.Fatalf("pages/events/batches=%v/%d/%+v", directory.calls, len(events), messages)
	}
	if messages[0].EventID == messages[1].EventID || messages[0].IdempotencyKey == messages[1].IdempotencyKey {
		t.Fatal("batches require distinct identities")
	}
	for _, message := range messages {
		if len(message.EventID) > 32 {
			t.Fatal("outbox event id overflows")
		}
	}
	againEvents, againMessages, err := service.buildDeliveryEffects(context.Background(), effects)
	if err != nil || !reflect.DeepEqual(events, againEvents) || !reflect.DeepEqual(messages, againMessages) {
		t.Fatal("lease retry must preserve events and idempotency")
	}
}

func TestDeliveryEffectsRejectPartialDirectoryAndRecover(t *testing.T) {
	directory := &pagedEffectsDirectory{users: 51, failPage: 2}
	repo := &hookRepository{}
	service := &Service{Repo: repo, Personnel: directory}
	now := time.Now().UTC()
	event := domain.DeliveryEvent{ID: "source", TenantID: "tenant", ProjectID: "PJ-1", Type: EventDeviationReported, CreatedAt: now}
	repo.rules = []domain.Rule{{Enabled: true, Trigger: event.Type, Target: "technical_director"}}
	if err := service.applyEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if got, err := service.DispatchDeliveryEffects(context.Background(), "tenant", "worker-1", 10, now); err != nil || got != 0 {
		t.Fatalf("count/error=%d/%v", got, err)
	}
	if len(repo.events) != 1 || len(repo.enqueued) != 0 || len(repo.effects) != 1 || repo.effects[0].RetryCount != 1 {
		t.Fatal("partial lookup must not commit effects")
	}
	// 后续编辑无影响；已接受事件保留生产时快照。
	repo.rules = nil
	directory.failPage = 0
	if got, err := service.DispatchDeliveryEffects(context.Background(), "tenant", "worker-2", 10, now.Add(2*time.Minute)); err != nil || got != 1 {
		t.Fatalf("recovery=%d/%v", got, err)
	}
	if len(repo.events) != 2 || len(repo.enqueued) != 1 || len(repo.enqueued[0].Recipients) != 51 || len(repo.effects) != 0 {
		t.Fatal("recovery lost recipients or effects")
	}
	if got, err := service.DispatchDeliveryEffects(context.Background(), "tenant", "worker-3", 10, now.Add(3*time.Minute)); err != nil || got != 0 {
		t.Fatal("completed job replayed")
	}
	directory.inconsistent = true
	if _, err := service.deliveryRoleRecipients(context.Background(), []string{"technical_director"}); !errors.Is(err, ErrPersonnelUnavailable) {
		t.Fatal("directory drift must fail closed")
	}
}

type failingEffectsCommitRepository struct {
	hookRepository
	fail bool
}

func (r *failingEffectsCommitRepository) CompleteDeliveryEffects(ctx context.Context, tenantID string, id uint64, worker string, events []domain.DeliveryEvent, messages []domain.NotificationMessage, now time.Time) error {
	if r.fail {
		return errors.New("outbox unavailable")
	}
	return r.hookRepository.CompleteDeliveryEffects(ctx, tenantID, id, worker, events, messages, now)
}
func TestDeliveryEffectsCommitFailureIsRetriedWithIdenticalIdentity(t *testing.T) {
	repo := &failingEffectsCommitRepository{fail: true}
	repo.rules = []domain.Rule{{Enabled: true, Trigger: EventDeviationReported, Target: "technical_director"}}
	service := &Service{Repo: repo, Personnel: &pagedEffectsDirectory{users: 1}}
	now := time.Now().UTC()
	if err := service.applyEvent(context.Background(), domain.DeliveryEvent{ID: "source", TenantID: "tenant", Type: EventDeviationReported, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	id := repo.effects[0].Effects.ID
	if _, err := service.DispatchDeliveryEffects(context.Background(), "tenant", "w1", 10, now); err != nil {
		t.Fatal(err)
	}
	if len(repo.effects) != 1 || repo.effects[0].Effects.ID != id || len(repo.events) != 1 || len(repo.enqueued) != 0 {
		t.Fatal("failure lost intent")
	}
	repo.fail = false
	if _, err := service.DispatchDeliveryEffects(context.Background(), "tenant", "w2", 10, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if repo.events[1].ID != id || repo.enqueued[0].EventID != id {
		t.Fatal("recovery regenerated identity")
	}
}

func TestSplitRuleRoleReminderUsesDurableEffects(t *testing.T) {
	now := time.Now().UTC()
	event := domain.DeliveryEvent{ID: "source", TenantID: "tenant", ProjectID: "PJ-1", Type: EventContractActivated, CreatedAt: now, RoleNotification: &domain.RoleNotification{Roles: []string{"business_admin"}, Message: domain.NotificationMessage{EventID: "split-reminder", IdempotencyKey: "split-once", EventType: "SPLIT_RULE_MISSING", OccurredAt: now}}}
	effects := SnapshotDeliveryEffects(event, nil, nil)
	service := &Service{Personnel: &pagedEffectsDirectory{users: 1}}
	events, messages, err := service.buildDeliveryEffects(context.Background(), effects)
	if err != nil || len(events) != 0 || len(messages) != 1 || len(messages[0].Recipients) != 1 || messages[0].IdempotencyKey != "split-once" {
		t.Fatalf("split effects=%v/%v/%v", events, messages, err)
	}
}
