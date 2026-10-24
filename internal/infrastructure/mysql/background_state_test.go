package mysql

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestBackgroundLeaseCountIsTenantScopedAndDoesNotTreatMissingTablesAsZero(t *testing.T) {
	db := equipmentAtomicityDB(t)
	r := NewRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	if n, e := r.CountActiveBackgroundLeases(context.Background(), "tenant-a", now); e == nil || n != -1 {
		t.Fatalf("missing table became %d %v", n, e)
	}
	if e := db.AutoMigrate(&deliveryEffectsRecord{}, &notificationOutboxRecord{}); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"pm_delivery_effects_outbox", "pm_notification_outbox"} {
		for _, row := range []map[string]interface{}{{"tenant_id": "tenant-a", "status": "PROCESSING", "locked_until": now.Add(time.Minute)}, {"tenant_id": "tenant-b", "status": "PROCESSING", "locked_until": now.Add(time.Minute)}, {"tenant_id": "tenant-a", "status": "PROCESSING", "locked_until": now}, {"tenant_id": "tenant-a", "status": "COMPLETED", "locked_until": now.Add(time.Minute)}} {
			row["payload"] = []byte(`{}`)
			row["created_at"] = now
			if table == "pm_notification_outbox" {
				row["event_id"] = "event"
				row["locked_by"] = "owner"
				row["last_error_summary"] = ""
				row["idempotency_key"] = fmt.Sprint(table, row["tenant_id"], row["status"], row["locked_until"])
				row["event_type"] = "test"
				row["aggregate_type"] = "test"
				row["aggregate_id"] = "test"
			}
			if e := db.Table(table).Create(row).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	if n, e := r.CountActiveBackgroundLeases(context.Background(), "tenant-a", now); e != nil || n != 2 {
		t.Fatalf("%d %v", n, e)
	}
	stats, e := r.ReadBackgroundQueueStats(context.Background(), "tenant-a", now)
	if e != nil || len(stats) != 2 {
		t.Fatalf("%+v %v", stats, e)
	}
	for _, s := range stats {
		if s.Processing != 2 || s.OldestDueAt == nil || !s.OldestDueAt.Equal(now) || s.Dead != 0 {
			t.Fatalf("tenant status incorrect %+v", s)
		}
	}
	if n, e := r.CountActiveBackgroundLeases(context.Background(), "", now); e == nil || n != -1 {
		t.Fatalf("invalid tenant became %d %v", n, e)
	}
}
