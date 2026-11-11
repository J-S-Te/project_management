package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
)

// 后台身份复核只应枚举"有人员档案"的租户：设备租户没有可复核的人员身份，
// 空表必须返回空列表而不是错误，否则后台会把"还没有人员档案"误判成故障。
func TestListCapabilityTenantsOnlyReturnsPersonnelTenants(t *testing.T) {
	db := equipmentAtomicityDB(t) // 仅使用内存 SQLite，不连接真实 MySQL。
	repo := NewRepository(db)
	ctx := context.Background()

	empty, err := repo.ListCapabilityTenants(ctx)
	if err != nil {
		t.Fatalf("empty capability table must not fail: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty capability table = %#v, want non-nil empty list", empty)
	}

	now := time.Now().UTC()
	rows := []capabilityRecord{
		{ID: ulid.Make().String(), TenantID: "tenant-b", ResourceType: "PERSON", ResourceID: "P-1", ResourceName: "人员一", CapabilityCodes: []byte("[]"), Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny, IdentityStatus: domain.IdentityStatusUnlinked, UpdatedAt: now},
		{ID: ulid.Make().String(), TenantID: "tenant-a", ResourceType: "PERSON", ResourceID: "P-2", ResourceName: "人员二", CapabilityCodes: []byte("[]"), Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny, IdentityStatus: domain.IdentityStatusUnlinked, UpdatedAt: now},
		{ID: ulid.Make().String(), TenantID: "tenant-a", ResourceType: "PERSON", ResourceID: "P-3", ResourceName: "人员三", CapabilityCodes: []byte("[]"), Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny, IdentityStatus: domain.IdentityStatusUnlinked, UpdatedAt: now},
		{ID: ulid.Make().String(), TenantID: "tenant-c", ResourceType: "EQUIPMENT", ResourceID: "EQ-1", ResourceName: "设备", CapabilityCodes: []byte("[]"), Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny, IdentityStatus: domain.IdentityStatusUnlinked, UpdatedAt: now},
		{ID: ulid.Make().String(), TenantID: "  ", ResourceType: "PERSON", ResourceID: "P-4", ResourceName: "空租户", CapabilityCodes: []byte("[]"), Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny, IdentityStatus: domain.IdentityStatusUnlinked, UpdatedAt: now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	tenants, err := repo.ListCapabilityTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(tenants, ","), "tenant-a,tenant-b"; got != want {
		t.Fatalf("tenants = %q, want %q (distinct, sorted, personnel only, blank dropped)", got, want)
	}

	// 查询必须遵守调用方上下文：单轮超时/停机取消不能被当成"成功空结果"。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.ListCapabilityTenants(cancelled); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v, want context.Canceled preserved", err)
	}
}

// 每轮只写一条系统汇总审计：actor 固定为系统身份，计数编码在 action 中
// （outbox 没有扩展列），结果按失败数给 SUCCESS/FAILURE，交给既有投递器重试。
func TestRecordIdentitySyncAuditWritesOneSystemRowPerRound(t *testing.T) {
	db := equipmentAtomicityDB(t) // 仅使用内存 SQLite。
	if err := db.AutoMigrate(&auditOutboxRecord{}); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	ctx := context.Background()

	first := application.IdentitySyncSummary{TenantID: "tenant-a", Tenants: 3, Checked: 12, Changed: 2, Failed: 0, StartedAt: time.Now().UTC()}
	if err := repo.RecordIdentitySyncAudit(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Failed = 1
	second.Changed = 0
	if err := repo.RecordIdentitySyncAudit(ctx, second); err != nil {
		t.Fatal(err)
	}

	var rows []auditOutboxRecord
	if err := db.Where("tenant_id = ? AND resource_type = ?", "tenant-a", "capability_identity_sync").Order("created_at").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("audit rows = %d, want exactly one row per round", len(rows))
	}
	okRow, failedRow := rows[0], rows[1]
	if okRow.ActorType != "SYSTEM" || okRow.ActorID == nil || *okRow.ActorID != application.IdentitySyncSystemActor {
		t.Fatalf("audit actor = %+v, want system actor", okRow)
	}
	if okRow.Result != "SUCCESS" || failedRow.Result != "FAILURE" {
		t.Fatalf("audit results = %q/%q, want SUCCESS/FAILURE", okRow.Result, failedRow.Result)
	}
	if okRow.Action != "PROJECT_MANAGEMENT:IDENTITY_SYNC_COMPLETED tenants=3 checked=12 changed=2 failed=0" {
		t.Fatalf("audit action = %q", okRow.Action)
	}
	if failedRow.Action != "PROJECT_MANAGEMENT:IDENTITY_SYNC_COMPLETED tenants=3 checked=12 changed=0 failed=1" {
		t.Fatalf("failed audit action = %q", failedRow.Action)
	}
	for _, row := range rows {
		if row.DeliveryStatus != "pending" || row.NextAttemptAt.IsZero() || row.CreatedAt.IsZero() {
			t.Fatalf("audit row must enter the existing outbox retry path: %+v", row)
		}
		if row.EventID == "" || row.EventID == row.ID || row.ResourceID == "" {
			t.Fatalf("audit row lacks stable identifiers for platform dedup: %+v", row)
		}
		if row.RequestID != nil || row.LoginIP != nil || row.ClientIP != nil {
			t.Fatalf("system maintenance audit must not invent request/IP evidence: %+v", row)
		}
	}
	if rows[0].EventID == rows[1].EventID {
		t.Fatal("each round must be a distinct audit event")
	}

	if err := repo.RecordIdentitySyncAudit(ctx, application.IdentitySyncSummary{TenantID: " tenant-a"}); err == nil {
		t.Fatal("padded tenant must be rejected instead of silently trimmed into another tenant's audit")
	}
	if err := repo.RecordIdentitySyncAudit(ctx, application.IdentitySyncSummary{TenantID: ""}); err == nil {
		t.Fatal("audit without a tenant must be rejected")
	}
}
