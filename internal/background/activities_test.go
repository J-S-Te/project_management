package background

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// Embed unused repository contracts: an accidental database path panics instead of
// accepting arbitrary arguments. The exercised boundary methods assert real inputs.
type activityRepo struct {
	application.Repository
	application.DeliveryRepository
	claims, completed, retried int
	owners                     []string
	claimErr, ackErr           error
	effect                     *domain.DeliveryEffectsItem
	notification               *domain.NotificationOutboxItem
	code                       string
	dead                       bool
	gate                       func()
	seen                       map[string]bool
	queued                     []domain.NotificationMessage
}

func (r *activityRepo) claim(tenant, owner string, limit int, now time.Time) error {
	if tenant != "tenant-a" || owner == "" || limit != 1 || now.Location() != time.UTC {
		return errors.New("invalid claim boundary")
	}
	r.claims++
	r.owners = append(r.owners, owner)
	if r.gate != nil {
		r.gate()
	}
	return r.claimErr
}
func (r *activityRepo) ClaimDeliveryEffects(_ context.Context, tenant, owner string, limit int, now time.Time) ([]domain.DeliveryEffectsItem, error) {
	if err := r.claim(tenant, owner, limit, now); err != nil {
		return nil, err
	}
	if r.effect == nil {
		return nil, nil
	}
	return []domain.DeliveryEffectsItem{*r.effect}, nil
}
func (r *activityRepo) CompleteDeliveryEffects(_ context.Context, tenant string, id uint64, owner string, _ []domain.DeliveryEvent, _ []domain.NotificationMessage, _ time.Time) error {
	if tenant != "tenant-a" || id != 7 || owner != r.owners[len(r.owners)-1] {
		return errors.New("invalid completion identity")
	}
	if r.ackErr != nil {
		return r.ackErr
	}
	r.completed++
	r.effect = nil
	return nil
}
func (r *activityRepo) RetryDeliveryEffects(_ context.Context, tenant string, id uint64, owner string, now, next time.Time, code string) error {
	if tenant != "tenant-a" || id != 7 || owner != r.owners[len(r.owners)-1] || !next.After(now) {
		return errors.New("invalid retry boundary")
	}
	r.retried++
	r.code = code
	return r.ackErr
}
func (r *activityRepo) ClaimNotificationOutbox(_ context.Context, tenant, owner string, limit int, now time.Time) ([]domain.NotificationOutboxItem, error) {
	if err := r.claim(tenant, owner, limit, now); err != nil {
		return nil, err
	}
	if r.notification == nil {
		return nil, nil
	}
	return []domain.NotificationOutboxItem{*r.notification}, nil
}
func (r *activityRepo) MarkNotificationOutboxSent(_ context.Context, tenant string, id uint64, owner string, _ time.Time) error {
	if tenant != "tenant-a" || id != 7 || owner != r.owners[len(r.owners)-1] {
		return errors.New("invalid sent boundary")
	}
	if r.ackErr != nil {
		return r.ackErr
	}
	r.completed++
	r.notification = nil
	return nil
}
func (r *activityRepo) MarkNotificationOutboxRetry(_ context.Context, tenant string, id uint64, owner string, now, next time.Time, dead bool) error {
	if tenant != "tenant-a" || id != 7 || owner != r.owners[len(r.owners)-1] || !next.After(now) {
		return errors.New("invalid retry boundary")
	}
	r.retried++
	r.dead = dead
	return r.ackErr
}
func (r *activityRepo) ListSlaOverdue(_ context.Context, filter platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	if filter.TenantID != "tenant-a" || !filter.AllowAll {
		return nil, errors.New("invalid SLA tenant")
	}
	return []domain.SlaOverdueItem{{ID: "item-1", Status: "实施中", StatusChangedAt: time.Now().Add(-30 * time.Hour)}}, nil
}
func (r *activityRepo) ListRules(_ context.Context, tenant, kind string) ([]domain.Rule, error) {
	if tenant != "tenant-a" || kind != "sla" {
		return nil, errors.New("invalid SLA rule scope")
	}
	return []domain.Rule{{Kind: "sla", Enabled: true, Status: "实施中", DeadlineHours: 10, RemindHours: 2}}, nil
}
func (r *activityRepo) GetServiceItem(_ context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	if filter.TenantID != "tenant-a" || id != "item-1" {
		return domain.ServiceItem{}, errors.New("invalid assignee scope")
	}
	return domain.ServiceItem{ID: id, TeamLeadID: "user-1"}, nil
}
func (r *activityRepo) EnqueueNotification(_ context.Context, tenant string, message domain.NotificationMessage) (bool, error) {
	if tenant != "tenant-a" {
		return false, errors.New("invalid enqueue tenant")
	}
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if r.seen[message.IdempotencyKey] {
		return false, nil
	}
	r.seen[message.IdempotencyKey] = true
	r.queued = append(r.queued, message)
	return true, nil
}

type activityPublisher func(context.Context, platform.NotificationEvent) error

func (f activityPublisher) Publish(ctx context.Context, event platform.NotificationEvent) error {
	return f(ctx, event)
}
func enabledService(r *activityRepo) *application.Service {
	return &application.Service{Repo: r, CheckBusinessLicense: func(context.Context) error { return nil }, Notifications: activityPublisher(func(context.Context, platform.NotificationEvent) error { return nil })}
}

func TestActivitiesRejectForeignTenantBeforeDatabase(t *testing.T) {
	for _, in := range []TickInput{{TenantID: "foreign", Kind: TaskEffects}, {TenantID: "", Kind: TaskSLA}, {TenantID: "tenant-a", Kind: "unknown"}, {TenantID: "tenant-a ", Kind: TaskNotifications}} {
		r := &activityRepo{}
		gateCalls := 0
		s := enabledService(r)
		s.CheckBusinessLicense = func(context.Context) error { gateCalls++; return nil }
		a := Activities{Service: s, TenantID: "tenant-a"}
		if _, err := a.RunOne(context.Background(), in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
		if r.claims != 0 || gateCalls != 0 {
			t.Fatalf("invalid input reached dependencies: claims/gates=%d/%d", r.claims, gateCalls)
		}
	}
}

func TestActivitiesRequireLicenseAndPauseWithoutAttempt(t *testing.T) {
	for _, kind := range []TaskKind{TaskSLA, TaskEffects, TaskNotifications} {
		t.Run(string(kind), func(t *testing.T) {
			r := &activityRepo{}
			s := enabledService(r)
			a := Activities{Service: s, TenantID: "tenant-a"}
			s.CheckBusinessLicense = nil
			if _, err := a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: kind}); err == nil {
				t.Fatal("omitted license gate accepted")
			}
			s.CheckBusinessLicense = func(context.Context) error { return errors.New("denied") }
			got, err := a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: kind})
			if err != nil || got.PausedReason != "LICENSE" || got.Processed != 0 || r.claims != 0 || r.retried != 0 {
				t.Fatalf("got=%+v error=%v repo=%+v", got, err, r)
			}
		})
	}
}

func TestActivitiesMissingPublisherFailsBeforeClaim(t *testing.T) {
	r := &activityRepo{notification: &domain.NotificationOutboxItem{ID: 7}}
	s := enabledService(r)
	s.Notifications = nil
	a := Activities{Service: s, TenantID: "tenant-a"}
	if _, err := a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: TaskNotifications}); err == nil {
		t.Fatal("missing notification publisher reported as an empty outbox")
	}
	if r.claims != 0 {
		t.Fatal("missing publisher claimed a job")
	}
}

func TestActivitiesUseFreshOwnerAndReturnDurableResults(t *testing.T) {
	for _, kind := range []TaskKind{TaskEffects, TaskNotifications} {
		t.Run(string(kind), func(t *testing.T) {
			r := &activityRepo{}
			s := enabledService(r)
			a := Activities{Service: s, TenantID: "tenant-a"}
			for i := 0; i < 2; i++ {
				if kind == TaskEffects {
					r.effect = &domain.DeliveryEffectsItem{ID: 7, TenantID: "tenant-a"}
				} else {
					r.notification = &domain.NotificationOutboxItem{ID: 7}
				}
				got, err := a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: kind, Limit: 50})
				if err != nil || got.Processed != 1 {
					t.Fatalf("got=%+v err=%v", got, err)
				}
			}
			if r.claims != 2 || r.completed != 2 || r.owners[0] == r.owners[1] {
				t.Fatalf("claim/completion/owners=%d/%d/%v", r.claims, r.completed, r.owners)
			}
			r.claimErr = errors.New("database down")
			if _, err := a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: kind}); !errors.Is(err, r.claimErr) {
				t.Fatalf("dependency error swallowed: %v", err)
			}
		})
	}
}

func TestActivitiesRetainedFailureIsNotDelivered(t *testing.T) {
	r := &activityRepo{effect: &domain.DeliveryEffectsItem{ID: 7, TenantID: "tenant-a", InvalidPayload: true}}
	a := Activities{Service: enabledService(r), TenantID: "tenant-a"}
	got, err := a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: TaskEffects})
	if err != nil || got.Processed != 0 || r.retried != 1 || r.code != "INVALID_EFFECTS_PAYLOAD" || r.completed != 0 {
		t.Fatalf("got=%+v err=%v retry=%d code=%s", got, err, r.retried, r.code)
	}
	r.notification = &domain.NotificationOutboxItem{ID: 7, RetryCount: 11}
	a.Service.Notifications = activityPublisher(func(context.Context, platform.NotificationEvent) error { return errors.New("offline") })
	got, err = a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: TaskNotifications})
	if err != nil || got.Processed != 0 || r.retried != 2 || !r.dead {
		t.Fatalf("got=%+v err=%v retry=%d dead=%v", got, err, r.retried, r.dead)
	}
	r.ackErr = errors.New("retry persistence failed")
	if _, err = a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: TaskEffects}); !errors.Is(err, r.ackErr) {
		t.Fatalf("retry persistence error swallowed: %v", err)
	}
}

func TestActivitiesMidFlightLicensePausePreservesLease(t *testing.T) {
	for _, kind := range []TaskKind{TaskEffects, TaskNotifications} {
		r := &activityRepo{effect: &domain.DeliveryEffectsItem{ID: 7, TenantID: "tenant-a", InvalidPayload: true}, notification: &domain.NotificationOutboxItem{ID: 7}}
		s := enabledService(r)
		active := true
		s.CheckBusinessLicense = func(context.Context) error {
			if !active {
				return errors.New("expired")
			}
			return nil
		}
		if kind == TaskEffects {
			r.gate = func() { active = false }
		} else {
			s.Notifications = activityPublisher(func(context.Context, platform.NotificationEvent) error { active = false; return errors.New("offline") })
		}
		a := Activities{Service: s, TenantID: "tenant-a"}
		got, err := a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: kind})
		if err != nil || got.PausedReason != "LICENSE" || got.Processed != 0 || r.completed != 0 || r.retried != 0 {
			t.Fatalf("kind=%s got=%+v err=%v", kind, got, err)
		}
	}
}

func TestActivitiesSlaUsesExecutionUtcAndExistingDedup(t *testing.T) {
	r := &activityRepo{}
	a := Activities{Service: enabledService(r), TenantID: "tenant-a"}
	before := time.Now().UTC()
	got, err := a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: TaskSLA})
	after := time.Now().UTC()
	if err != nil || got.Processed != 1 || len(r.queued) != 1 {
		t.Fatalf("got=%+v err=%v queued=%v", got, err, r.queued)
	}
	m := r.queued[0]
	if m.OccurredAt.Location() != time.UTC || m.OccurredAt.Before(before) || m.OccurredAt.After(after) {
		t.Fatalf("not execution UTC: %v", m.OccurredAt)
	}
	if m.EventType != "SLA_STATUS_DEADLINE_OVERDUE" || !strings.HasPrefix(m.IdempotencyKey, "sla-item-1-STATUS_DEADLINE_OVERDUE-") || !strings.HasSuffix(m.IdempotencyKey, m.OccurredAt.Format("2006-01-02")) {
		t.Fatalf("threshold/dedup changed: %+v", m)
	}
	got, err = a.RunOne(context.Background(), TickInput{TenantID: "tenant-a", Kind: TaskSLA})
	if err != nil || got.Processed != 0 || len(r.queued) != 1 {
		t.Fatalf("repeat scan=%+v/%v queued=%d", got, err, len(r.queued))
	}
}
