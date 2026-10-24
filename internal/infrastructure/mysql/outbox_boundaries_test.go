package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// SQLite exercises actual predicates and persisted state; it does not prove MySQL row locks.
func TestOutboxLateAttemptCannotAcknowledgeReclaimedJob(t *testing.T) {
	db := equipmentAtomicityDB(t)
	if err := db.AutoMigrate(&deliveryEffectsRecord{}, &notificationOutboxRecord{}); err != nil {
		t.Fatal(err)
	}
	r := NewRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, kind := range []string{"effects", "notification"} {
		t.Run(kind, func(t *testing.T) {
			var id uint64
			if kind == "effects" {
				row := deliveryEffectsRecord{TenantID: "tenant-b", Payload: []byte(`{}`), Status: "PENDING", CreatedAt: now}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				id = row.ID
			} else {
				row := notificationOutboxRecord{TenantID: "tenant-b", Payload: []byte(`{}`), Status: "PENDING", CreatedAt: now}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				id = row.ID
			}
			claim := func(tenant, owner string, at time.Time) int {
				t.Helper()
				if kind == "effects" {
					items, err := r.ClaimDeliveryEffects(ctx, tenant, owner, 1, at)
					if err != nil {
						t.Fatal(err)
					}
					return len(items)
				}
				items, err := r.ClaimNotificationOutbox(ctx, tenant, owner, 1, at)
				if err != nil {
					t.Fatal(err)
				}
				return len(items)
			}
			ack := func(tenant, owner string, at time.Time) error {
				if kind == "effects" {
					return r.CompleteDeliveryEffects(ctx, tenant, id, owner, nil, nil, at)
				}
				return r.MarkNotificationOutboxSent(ctx, tenant, id, owner, at)
			}
			retry := func(tenant, owner string, at time.Time) error {
				if kind == "effects" {
					return r.RetryDeliveryEffects(ctx, tenant, id, owner, at, at.Add(time.Hour), "FAIL")
				}
				return r.MarkNotificationOutboxRetry(ctx, tenant, id, owner, at, at.Add(time.Hour), false)
			}
			if claim("tenant-a", "old", now) != 0 || claim("tenant-b", "old", now) != 1 {
				t.Fatal("claim crossed tenant")
			}
			for _, op := range []func(string, string, time.Time) error{ack, retry} {
				if err := op("tenant-a", "old", now); !errors.Is(err, application.ErrConflict) {
					t.Fatalf("cross tenant accepted: %v", err)
				}
				if err := op("tenant-b", "old", now.Add(2*time.Minute)); !errors.Is(err, application.ErrConflict) {
					t.Fatalf("expired owner accepted: %v", err)
				}
			}
			if claim("tenant-b", "new", now.Add(3*time.Minute)) != 1 {
				t.Fatal("expired job not recovered")
			}
			for _, op := range []func(string, string, time.Time) error{ack, retry} {
				if err := op("tenant-b", "old", now.Add(3*time.Minute)); !errors.Is(err, application.ErrConflict) {
					t.Fatalf("late owner accepted: %v", err)
				}
			}
			if err := retry("tenant-b", "new", now.Add(3*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if claim("tenant-b", "final", now.Add(2*time.Hour)) != 1 {
				t.Fatal("retry lost")
			}
			if err := ack("tenant-b", "final", now.Add(2*time.Hour)); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct{ tenant, owner string }{{"", "o"}, {"t", ""}, {strings.Repeat("t", 65), "o"}, {"t", strings.Repeat("o", 129)}} {
		_, err := r.ClaimDeliveryEffects(ctx, tc.tenant, tc.owner, 1, now)
		if !errors.Is(err, application.ErrValidation) {
			t.Fatal("invalid effects identity accepted", err)
		}
		_, err = r.ClaimNotificationOutbox(ctx, tc.tenant, tc.owner, 1, now)
		if !errors.Is(err, application.ErrValidation) {
			t.Fatal("invalid notification identity accepted", err)
		}
		for _, err := range []error{r.CompleteDeliveryEffects(ctx, tc.tenant, 1, tc.owner, []domain.DeliveryEvent{}, nil, now), r.RetryDeliveryEffects(ctx, tc.tenant, 1, tc.owner, now, now.Add(time.Minute), "FAIL"), r.MarkNotificationOutboxSent(ctx, tc.tenant, 1, tc.owner, now), r.MarkNotificationOutboxRetry(ctx, tc.tenant, 1, tc.owner, now, now.Add(time.Minute), false)} {
			if !errors.Is(err, application.ErrValidation) {
				t.Fatal("invalid update identity accepted", err)
			}
		}
	}
}

func TestNotificationClaimPreservesPersistedKeyAcrossLeaseRecovery(t *testing.T) {
	db := equipmentAtomicityDB(t)
	if err := db.AutoMigrate(&notificationOutboxRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := notificationOutboxRecord{TenantID: "tenant", EventID: "event", IdempotencyKey: "durable-original-key", Payload: []byte(`{"event_id":"event","title":"original title","recipient_user_ids":["user"]}`), Status: "PENDING", CreatedAt: now}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	r := NewRepository(db)
	for i, owner := range []string{"old", "new"} {
		items, err := r.ClaimNotificationOutbox(context.Background(), "tenant", owner, 1, now.Add(time.Duration(i)*3*time.Minute))
		if err != nil || len(items) != 1 {
			t.Fatalf("claim=%+v/%v", items, err)
		}
		if items[0].Message.IdempotencyKey != "durable-original-key" || items[0].Message.Title != "original title" || len(items[0].Message.Recipients) != 1 || items[0].Message.Recipients[0] != "user" {
			t.Fatalf("persisted payload/key lost: %+v", items[0])
		}
	}
}

// A valid JSON value with incompatible field types must return through the
// dispatcher's license boundary instead of spending a retry inside Claim.
func TestInvalidEffectsPayloadRespectsPostClaimLicense(t *testing.T) {
	for _, pause := range []bool{true, false} {
		name := "licensed retry"
		if pause {
			name = "paused after claim"
		}
		t.Run(name, func(t *testing.T) {
			db := equipmentAtomicityDB(t)
			if err := db.AutoMigrate(&deliveryEffectsRecord{}); err != nil {
				t.Fatal(err)
			}
			row := deliveryEffectsRecord{TenantID: "tenant", SourceEventID: "bad-source", Payload: []byte(`{"targets":42}`), Status: "PENDING", CreatedAt: time.Now().UTC()}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			active := true
			claimed := false
			var claimNow time.Time
			if err := db.Callback().Update().After("gorm:update").Register("t1_invalid_payload_claim_boundary", func(tx *gorm.DB) {
				changes, ok := tx.Statement.Dest.(map[string]any)
				if !ok || tx.Statement.Table != "pm_delivery_effects_outbox" || changes["status"] != "PROCESSING" || tx.RowsAffected != 1 {
					return
				}
				claimed = true
				claimNow = changes["locked_until"].(time.Time).Add(-2 * time.Minute)
				// Exercise a fresh retry timestamp rather than the pre-claim timestamp.
				time.Sleep(5 * time.Millisecond)
				if pause {
					active = false
				}
			}); err != nil {
				t.Fatal(err)
			}
			denied := errors.New("license paused during claim")
			service := &application.Service{Repo: NewRepository(db), CheckBusinessLicense: func(context.Context) error {
				if !active {
					return denied
				}
				return nil
			}}
			count, dispatchErr := service.DispatchDeliveryEffects(context.Background(), "tenant", "owner", 1, time.Now().UTC())
			if !claimed {
				t.Fatal("real repository never claimed fixture")
			}
			var stored deliveryEffectsRecord
			if err := db.Where("tenant_id = ? AND id = ?", "tenant", row.ID).Take(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("invalid payload completed %d jobs", count)
			}
			if pause {
				if !errors.Is(dispatchErr, denied) {
					t.Errorf("post-claim license denial lost: %v", dispatchErr)
				}
				if stored.Status != "PROCESSING" || stored.RetryCount != 0 || stored.NextRetryAt != nil || stored.LockedBy != "owner" || stored.LockedUntil == nil || stored.LastErrorSummary != "" {
					t.Fatalf("paused claim spent an attempt or lost its lease: %+v", stored)
				}
			} else {
				if dispatchErr != nil {
					t.Fatal(dispatchErr)
				}
				if stored.Status != "RETRY_WAIT" || stored.RetryCount != 1 || stored.LastErrorSummary != "INVALID_EFFECTS_PAYLOAD" || stored.LockedBy != "" || stored.LockedUntil != nil || stored.NextRetryAt == nil {
					t.Fatalf("invalid payload retention policy lost: %+v", stored)
				}
				if !stored.NextRetryAt.After(claimNow.Add(time.Hour + 2*time.Millisecond)) {
					t.Fatalf("retry reused claim time instead of fresh now: claim=%s next=%v", claimNow, stored.NextRetryAt)
				}
			}
			if string(stored.Payload) != `{"targets":42}` {
				t.Fatal("invalid intent payload was changed")
			}
		})
	}
}
