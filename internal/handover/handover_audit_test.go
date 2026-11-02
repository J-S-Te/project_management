package handover

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/j-s-te/project-management/internal/auditctx"
)

// 统一审计方案 §3.1：离职交接变更项目责任人属于关键业务变更，
// 必须与责任变更同事务写审计，且标注为系统执行（交接批次驱动）。
func TestTransferOwnershipWritesHandoverAudit(t *testing.T) {
	source, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("读取 repository.go: %v", err)
	}
	text := string(source)
	if !strings.Contains(text, "PROJECT_MANAGEMENT:HANDOVER_TRANSFERRED") {
		t.Fatal("交接缺少审计事件")
	}
	start := strings.Index(text, "func (repository *GORMRepository) TransferOwnership(")
	end := strings.Index(text[start:], "\n}\n")
	body := text[start : start+end]
	if !strings.Contains(body, "writeHandoverAudit(tx, in)") {
		t.Fatalf("交接审计必须与责任变更同事务：%s", body)
	}
	if !strings.Contains(text, `ActorType: "SYSTEM"`) {
		t.Fatal("交接由平台批次驱动，必须标注 SYSTEM")
	}
}

func newHandoverAuditDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "handover-audit.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开审计测试库: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取测试库连接: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&handoverAuditRow{}); err != nil {
		t.Fatalf("建审计表: %v", err)
	}
	return db
}

func auditTestContext() context.Context {
	return auditctx.WithMeta(context.Background(), auditctx.Meta{
		ActorID: "operator-1", LoginIP: "10.0.0.1", ClientIP: "10.0.0.2", IPSource: "TRUSTED_PROXY",
	})
}

// S-3：服务项改派必须与项目交接同口径写资源级审计（resource_type=service_item、
// resource_id=服务项 ID），并记录操作者；项目经理路径的事件格式保持不变。
func TestWriteHandoverAuditCoversServiceItemAndProject(t *testing.T) {
	db := newHandoverAuditDB(t)
	ctx := auditTestContext()
	inputs := []TransferInput{
		{TenantID: "tenant-1", BatchID: "batch-1", ItemID: "item-1", ResourceType: ResourceServiceItem,
			ResourceID: "SI-1", FromUserID: "user-leaver", ToUserID: "user-successor", IdempotencyKey: "handover:batch-1:item-1"},
		{TenantID: "tenant-1", BatchID: "batch-1", ItemID: "item-2", ResourceType: ResourceProject,
			ResourceID: "P-1", FromUserID: "user-leaver", ToUserID: "user-successor", IdempotencyKey: "handover:batch-1:item-2"},
	}
	for _, in := range inputs {
		if err := db.Transaction(func(tx *gorm.DB) error { return writeHandoverAudit(tx.WithContext(ctx), in) }); err != nil {
			t.Fatalf("写 %s 交接审计: %v", in.ResourceType, err)
		}
	}

	var rows []handoverAuditRow
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("审计行数=%d, want 2：%+v", len(rows), rows)
	}
	byType := make(map[string]handoverAuditRow, len(rows))
	for _, row := range rows {
		byType[row.ResourceType] = row
	}
	for _, want := range []struct{ resourceType, resourceID string }{
		{ResourceServiceItem, "SI-1"},
		{ResourceProject, "P-1"},
	} {
		row, ok := byType[want.resourceType]
		if !ok {
			t.Fatalf("缺少 %s 的资源级审计：%+v", want.resourceType, rows)
		}
		if row.ResourceID != want.resourceID {
			t.Fatalf("%s 审计 resource_id=%q, want %q", want.resourceType, row.ResourceID, want.resourceID)
		}
		// 事件格式沿用既有口径：同一 action、系统执行、成功待投递。
		if row.Action != "PROJECT_MANAGEMENT:HANDOVER_TRANSFERRED" || row.ActorType != "SYSTEM" ||
			row.Result != "SUCCESS" || row.DeliveryStatus != "pending" {
			t.Fatalf("%s 审计字段漂移：%+v", want.resourceType, row)
		}
		if row.ActorID == nil || *row.ActorID != "operator-1" {
			t.Fatalf("%s 审计未记录操作者：%+v", want.resourceType, row)
		}
		if len(row.ID) != 26 || len(row.EventID) != 26 {
			t.Fatalf("%s 审计主键/事件号必须是 26 字符：%+v", want.resourceType, row)
		}
		if row.IPSource != "TRUSTED_PROXY" || len(row.LoginIP) == 0 || len(row.ClientIP) == 0 {
			t.Fatalf("%s 审计来源字段缺失：%+v", want.resourceType, row)
		}
	}
}

// 不支持的资源类型沿用原守卫，不写审计行。
func TestWriteHandoverAuditSkipsUnsupportedResource(t *testing.T) {
	db := newHandoverAuditDB(t)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return writeHandoverAudit(tx.WithContext(auditTestContext()), TransferInput{TenantID: "tenant-1", ResourceType: ResourceTechnicalTask, ResourceID: "TT-1"})
	}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&handoverAuditRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("不支持的资源类型写了 %d 条审计", count)
	}
}

// 源码守卫：服务项的两条成功分支（标量负责人改派、engineer_ids 改派）都必须先写回执、
// 再写资源级审计，否则"责任已改派但没有资源级审计"会再次出现。
func TestServiceItemTransferWritesHandoverAudit(t *testing.T) {
	source, err := os.ReadFile("service_item_transfer.go")
	if err != nil {
		t.Fatalf("读取 service_item_transfer.go: %v", err)
	}
	body := string(source)
	if got := strings.Count(body, "writeHandoverAudit(tx, in)"); got != 2 {
		t.Fatalf("服务项两条成功分支都要写审计，实际 %d 处", got)
	}
	if got := strings.Count(body, "writeReceipt(tx, in, now, ReceiptApplied"); got != 2 {
		t.Fatalf("服务项成功分支回执数=%d, want 2", got)
	}
	receipt := strings.Index(body, "writeReceipt(tx, in, now, ReceiptApplied")
	audit := strings.Index(body, "writeHandoverAudit(tx, in)")
	if receipt < 0 || audit < 0 || receipt > audit {
		t.Fatal("资源级审计必须在成功回执落库之后写")
	}
}

// 统一审计方案：交接审计行必须带上请求关联号（prj_audit_outbox.request_id），
// 投递器才能把它映射为 AuditEvent.RequestID/CorrelationID；
// 请求没有关联号时保持 SQL NULL，不写空串。
func TestWriteHandoverAuditCarriesRequestID(t *testing.T) {
	db := newHandoverAuditDB(t)
	for _, tc := range []struct {
		name      string
		requestID string
		want      *string
	}{
		{"request_id_present", "req-handover-0003", requestIDPointer("req-handover-0003")},
		{"request_id_absent", "", nil},
	} {
		resourceID := "P-AUDIT-" + tc.name
		ctx := auditctx.WithMeta(context.Background(), auditctx.Meta{
			ActorID: "operator-1", LoginIP: "10.0.0.1", ClientIP: "10.0.0.2", IPSource: "TRUSTED_PROXY", RequestID: tc.requestID,
		})
		in := TransferInput{TenantID: "tenant-1", BatchID: "batch-audit", ItemID: "item-audit", ResourceType: ResourceProject,
			ResourceID: resourceID, FromUserID: "user-leaver", ToUserID: "user-successor", IdempotencyKey: "handover:audit:" + tc.name}
		if err := db.Transaction(func(tx *gorm.DB) error { return writeHandoverAudit(tx.WithContext(ctx), in) }); err != nil {
			t.Fatalf("%s 交接审计: %v", tc.name, err)
		}
		var row handoverAuditRow
		if err := db.Where("tenant_id = ? AND resource_id = ?", in.TenantID, resourceID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if tc.want == nil {
			if row.RequestID != nil {
				t.Fatalf("%s request_id=%q, want NULL", tc.name, *row.RequestID)
			}
			var nullRows int64
			if err := db.Table("prj_audit_outbox").Where("id = ? AND request_id IS NULL", row.ID).Count(&nullRows).Error; err != nil || nullRows != 1 {
				t.Fatalf("%s request_id must be stored as SQL NULL: rows=%d err=%v", tc.name, nullRows, err)
			}
			continue
		}
		if row.RequestID == nil || *row.RequestID != *tc.want {
			t.Fatalf("%s request_id=%v, want %q", tc.name, row.RequestID, *tc.want)
		}
	}
}

// requestIDPointer 提供测试期望值的指针，避免取循环变量地址。
func requestIDPointer(value string) *string { return &value }
