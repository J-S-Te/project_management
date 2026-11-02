package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/auditctx"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

// 统一审计方案：写侧必须把请求关联号落进 prj_audit_outbox.request_id，
// 投递器才能把它作为 AuditEvent.RequestID/CorrelationID 发给平台；
// 没有请求号时保持 SQL NULL，由平台生成，不得写空串冒充关联成功。
func TestAuditOutboxWritePathsCarryRequestID(t *testing.T) {
	db := equipmentAtomicityDB(t) // 仅使用内存 SQLite，不使用 DSN 或 MySQL 连接。
	if err := db.AutoMigrate(&auditOutboxRecord{}, &deliveryEventRecord{}, &deliveryEffectsRecord{}, &automationRecord{}, &warningRuleRecord{}); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	tenant := "tenant-audit-request-id"

	outboxCases := []struct {
		name      string
		requestID string
		assert    func(t *testing.T, row auditOutboxRecord)
	}{
		{"request_id_present", "req-contract-0002", assertRequestID("req-contract-0002")},
		{"request_id_absent", "", assertNullRequestID(t, db)},
	}

	// 建项审计：CreateProject 与项目行同事务写 outbox。
	t.Run("project_created_with_request_id", func(t *testing.T) {
		project := domain.Project{ID: "PJ-AUDIT-CREATE", TenantID: tenant, Name: "建项审计项目", Customer: "客户", CustomerID: "customer", Contract: "HT-AUDIT-CREATE", ContractID: "HT-AUDIT-CREATE", ContractVersion: "1", SupplementStatus: "NONE", Status: "待拆解确认", CreatedAt: now, UpdatedAt: now}
		ctx := auditctx.WithMeta(context.Background(), auditctx.Meta{ActorID: "operator-1", LoginIP: "10.0.0.1", ClientIP: "10.0.0.2", IPSource: "DIRECT", RequestID: "req-create-0001"})
		if err := repo.CreateProject(ctx, project); err != nil {
			t.Fatal(err)
		}
		var row auditOutboxRecord
		if err := db.Where("tenant_id = ? AND resource_id = ? AND action = ?", tenant, project.ID, "PROJECT_MANAGEMENT:PROJECT_CREATED").Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		assertRequestID("req-create-0001")(t, row)
	})

	// 建项审计：请求未携带关联号时保持 NULL。
	t.Run("project_created_without_request_id", func(t *testing.T) {
		project := domain.Project{ID: "PJ-AUDIT-CREATE-EMPTY", TenantID: tenant, Name: "建项审计项目", Customer: "客户", CustomerID: "customer", Contract: "HT-AUDIT-CREATE-EMPTY", ContractID: "HT-AUDIT-CREATE-EMPTY", ContractVersion: "1", SupplementStatus: "NONE", Status: "待拆解确认", CreatedAt: now, UpdatedAt: now}
		ctx := auditctx.WithMeta(context.Background(), auditctx.Meta{ActorID: "operator-1", LoginIP: "10.0.0.1", ClientIP: "10.0.0.2", IPSource: "DIRECT"})
		if err := repo.CreateProject(ctx, project); err != nil {
			t.Fatal(err)
		}
		var row auditOutboxRecord
		if err := db.Where("tenant_id = ? AND resource_id = ? AND action = ?", tenant, project.ID, "PROJECT_MANAGEMENT:PROJECT_CREATED").Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		assertNullRequestID(t, db)(t, row)
	})

	// 交付事件审计：走真实仓储入口 ActivateContract，由 createEvent 写 outbox。
	for _, tc := range outboxCases {
		t.Run("contract_activated/"+tc.name, func(t *testing.T) {
			project := domain.Project{ID: "PJ-AUDIT-" + tc.name, TenantID: tenant, Name: "交付事件审计项目", Customer: "客户", CustomerID: "customer", Contract: "HT-AUDIT-" + tc.name, ContractID: "HT-AUDIT-" + tc.name, ContractVersion: "1", SupplementStatus: "NONE", Status: "待确认", CreatedAt: now, UpdatedAt: now}
			event := domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: tenant, ProjectID: project.ID, Type: application.EventContractActivated, ActorUserID: "operator-1", CreatedAt: now, Payload: map[string]any{"contract_id": project.ContractID}}
			ctx := auditctx.WithMeta(context.Background(), auditctx.Meta{ActorID: "operator-1", LoginIP: "10.0.0.1", ClientIP: "10.0.0.2", IPSource: "DIRECT", RequestID: tc.requestID})
			if err := repo.ActivateContract(ctx, project, nil, event); err != nil {
				t.Fatal(err)
			}
			var row auditOutboxRecord
			if err := db.Where("tenant_id = ? AND resource_id = ? AND action = ?", tenant, project.ID, "PROJECT_MANAGEMENT:"+string(application.EventContractActivated)).Take(&row).Error; err != nil {
				t.Fatal(err)
			}
			tc.assert(t, row)
		})
	}
}

func assertRequestID(want string) func(t *testing.T, row auditOutboxRecord) {
	return func(t *testing.T, row auditOutboxRecord) {
		t.Helper()
		if row.RequestID == nil || *row.RequestID != want {
			t.Fatalf("request_id=%v, want %q", row.RequestID, want)
		}
	}
}

// assertNullRequestID 不仅要求读回为空，还要求库里存的是 SQL NULL 而不是空串：
// 空串会被投递器当成有效关联号发给平台。
func assertNullRequestID(t *testing.T, db *gorm.DB) func(t *testing.T, row auditOutboxRecord) {
	t.Helper()
	return func(t *testing.T, row auditOutboxRecord) {
		t.Helper()
		if row.RequestID != nil {
			t.Fatalf("request_id=%q, want NULL when the request carries no request id", *row.RequestID)
		}
		var nullRows int64
		if err := db.Table("prj_audit_outbox").Where("id = ? AND request_id IS NULL", row.ID).Count(&nullRows).Error; err != nil || nullRows != 1 {
			t.Fatalf("request_id must be stored as SQL NULL: rows=%d err=%v", nullRows, err)
		}
	}
}
