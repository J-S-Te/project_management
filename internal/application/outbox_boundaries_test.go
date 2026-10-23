package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// The external repository/publisher are controlled boundaries; dispatch itself is real.
type boundaryOutboxRepo struct {
	hookRepository
	calls, sent, retried int
	tenant, owner        string
	item                 *domain.NotificationOutboxItem
	ackErr               error
	dead                 bool
	claimTimes, ackTimes []time.Time
}

func (r *boundaryOutboxRepo) ClaimDeliveryEffects(ctx context.Context, tenant, owner string, limit int, now time.Time) ([]domain.DeliveryEffectsItem, error) {
	r.calls++
	r.tenant, r.owner = tenant, owner
	return r.hookRepository.ClaimDeliveryEffects(ctx, tenant, owner, limit, now)
}
func (r *boundaryOutboxRepo) ClaimNotificationOutbox(_ context.Context, tenant, owner string, limit int, now time.Time) ([]domain.NotificationOutboxItem, error) {
	r.calls++
	r.tenant, r.owner = tenant, owner
	r.claimTimes = append(r.claimTimes, now)
	if limit != 1 {
		return nil, errors.New("dispatch must claim one item")
	}
	if r.item == nil {
		return nil, nil
	}
	return []domain.NotificationOutboxItem{*r.item}, nil
}
func (r *boundaryOutboxRepo) MarkNotificationOutboxSent(_ context.Context, tenant string, _ uint64, owner string, now time.Time) error {
	r.sent++
	r.ackTimes = append(r.ackTimes, now)
	if tenant != r.tenant || owner != r.owner {
		return ErrConflict
	}
	if r.ackErr != nil {
		return r.ackErr
	}
	r.item = nil
	return nil
}
func (r *boundaryOutboxRepo) MarkNotificationOutboxRetry(_ context.Context, tenant string, _ uint64, owner string, now, next time.Time, dead bool) error {
	r.retried++
	r.dead = dead
	r.ackTimes = append(r.ackTimes, now)
	if tenant != r.tenant || owner != r.owner || !next.After(now) {
		return ErrConflict
	}
	r.item = nil
	return nil
}

type boundaryPublisher func(context.Context, platform.NotificationEvent) error

func (f boundaryPublisher) Publish(ctx context.Context, e platform.NotificationEvent) error {
	return f(ctx, e)
}

func TestOutboxDispatchLicenseAndTenantBoundaries(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	denied := errors.New("license paused")
	t.Run("denied before repository", func(t *testing.T) {
		r := &boundaryOutboxRepo{}
		s := &Service{Repo: r, Notifications: &notificationStub{}, CheckBusinessLicense: func(context.Context) error { return denied }}
		if _, err := s.DispatchDeliveryEffects(ctx, "tenant-a", "owner", 1, now); !errors.Is(err, denied) {
			t.Fatalf("effects error=%v", err)
		}
		if _, err := s.DispatchNotificationOutbox(ctx, "tenant-a", "owner", 1, now); !errors.Is(err, denied) {
			t.Fatalf("notification error=%v", err)
		}
		if r.calls != 0 {
			t.Fatalf("denied license touched repository %d times", r.calls)
		}
	})
	for _, publishErr := range []error{nil, errors.New("publish failed")} {
		t.Run("license revoked after publish "+publishErrString(publishErr), func(t *testing.T) {
			r := &boundaryOutboxRepo{item: &domain.NotificationOutboxItem{ID: 7}}
			active := true
			s := &Service{Repo: r, CheckBusinessLicense: func(context.Context) error {
				if !active {
					return denied
				}
				return nil
			}}
			s.Notifications = boundaryPublisher(func(context.Context, platform.NotificationEvent) error { active = false; return publishErr })
			if _, err := s.DispatchNotificationOutbox(ctx, "tenant-a", "owner", 1, now); !errors.Is(err, denied) {
				t.Fatalf("error=%v", err)
			}
			if r.sent != 0 || r.retried != 0 || r.item == nil {
				t.Fatal("license pause acknowledged or consumed attempt")
			}
			if r.tenant != "tenant-a" || r.owner != "owner" {
				t.Fatal("trusted boundary not forwarded")
			}
		})
	}
	for _, tc := range []struct{ tenant, owner string }{{"", "owner"}, {"tenant", ""}, {strings.Repeat("t", 65), "owner"}, {"tenant", strings.Repeat("o", 129)}} {
		r := &boundaryOutboxRepo{}
		s := &Service{Repo: r, Notifications: &notificationStub{}}
		if _, err := s.DispatchNotificationOutbox(ctx, tc.tenant, tc.owner, 1, now); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid notification identity accepted: %v", err)
		}
		if _, err := s.DispatchDeliveryEffects(ctx, tc.tenant, tc.owner, 1, now); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid effects identity accepted: %v", err)
		}
		if r.calls != 0 {
			t.Fatal("invalid identity touched repository")
		}
	}
}
func publishErrString(err error) string {
	if err == nil {
		return "success"
	}
	return "failure"
}

func TestNotificationAcceptedThenAckLostKeepsOriginalKey(t *testing.T) {
	now := time.Now().UTC()
	r := &boundaryOutboxRepo{item: &domain.NotificationOutboxItem{ID: 7, Message: domain.NotificationMessage{EventID: "event", IdempotencyKey: "original-key", Title: "title", Recipients: []string{"user"}, OccurredAt: now}}, ackErr: ErrConflict}
	var published []platform.NotificationEvent
	s := &Service{Repo: r, Notifications: boundaryPublisher(func(_ context.Context, e platform.NotificationEvent) error {
		published = append(published, e)
		return nil
	})}
	if _, err := s.DispatchNotificationOutbox(context.Background(), "tenant", "old", 1, now); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	r.ackErr = nil
	if n, err := s.DispatchNotificationOutbox(context.Background(), "tenant", "new", 1, now.Add(3*time.Minute)); err != nil || n != 1 {
		t.Fatalf("retry=%d/%v", n, err)
	}
	if len(published) != 2 || !reflect.DeepEqual(published[0], published[1]) || published[0].IdempotencyKey != "original-key" {
		t.Fatalf("retry payload changed: %+v", published)
	}
	if r.retried != 0 {
		t.Fatal("accepted notification consumed business retry")
	}
	for _, retry := range []uint{10, 11} {
		r := &boundaryOutboxRepo{item: &domain.NotificationOutboxItem{ID: 7, RetryCount: retry}}
		s.Repo = r
		s.Notifications = boundaryPublisher(func(context.Context, platform.NotificationEvent) error { return errors.New("offline") })
		if _, err := s.DispatchNotificationOutbox(context.Background(), "tenant", "owner", 1, now); err != nil {
			t.Fatal(err)
		}
		if r.retried != 1 || r.dead != (retry == 11) {
			t.Fatalf("dead letter at retry=%d dead=%v", retry, r.dead)
		}
	}
}
