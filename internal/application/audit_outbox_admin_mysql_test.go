package application

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/auditoutbox"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestFailedAuditOutboxQueryAndRetryOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PM_TEST_DSN"))
	if dsn == "" {
		t.Skip("PM_TEST_DSN is required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "PM-AUDIT-OPS-20261018"
	if err := db.Exec(`DELETE FROM prj_audit_outbox WHERE tenant_id=?`, tenant).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM prj_audit_outbox WHERE tenant_id=?`, tenant).Error })
	if err := db.Exec(`INSERT INTO prj_audit_outbox(id,tenant_id,event_id,actor_id,action,resource_type,resource_id,ip_source,result,delivery_status,attempts,next_attempt_at,last_error,created_at) VALUES('01AUDITFAILED0000000000001',?,'01AUDITEVENT00000000000001','actor','PROJECT_MANAGEMENT:test','project','p1','CLIENT','SUCCESS','failed',20,UTC_TIMESTAMP(3),'PLATFORM_AUDIT_UNAVAILABLE',UTC_TIMESTAMP(3))`, tenant).Error; err != nil {
		t.Fatal(err)
	}
	service := &Service{AuditStore: &auditoutbox.GORMStore{DB: db, Lease: time.Minute, MaxTries: 20}}
	p := platform.Principal{TenantID: tenant, UserID: "admin", Permissions: map[string]bool{"project_rule.manage": true}}
	items, err := service.ListFailedAuditEvents(context.Background(), p, 100)
	if err != nil || len(items) != 1 || items[0].LastError != "PLATFORM_AUDIT_UNAVAILABLE" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := service.RetryFailedAuditEvent(context.Background(), p, items[0].ID); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err := db.Raw(`SELECT delivery_status,attempts FROM prj_audit_outbox WHERE tenant_id=? AND id=?`, tenant, items[0].ID).Row().Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "retry" || attempts != 0 {
		t.Fatalf("status=%s attempts=%d, want retry/0", status, attempts)
	}
}
