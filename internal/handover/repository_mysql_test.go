package handover

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 测试用的最小项目模型：只声明仓储真正查询/更新的列。
type testProjectRow struct {
	ID                string    `gorm:"column:id;size:32;primaryKey"`
	TenantID          string    `gorm:"column:tenant_id;size:64"`
	Name              string    `gorm:"column:name"`
	ManagerIdentityID string    `gorm:"column:manager_identity_id;size:128"`
	Status            string    `gorm:"column:status;size:32"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (testProjectRow) TableName() string { return "pm_project" }

// 测试用的最小服务项模型：只声明服务项改派真正查询/更新的列。
type testServiceItemRow struct {
	ID               string     `gorm:"column:id;size:32;primaryKey"`
	TenantID         string     `gorm:"column:tenant_id;size:64"`
	Requirement      string     `gorm:"column:requirement"`
	TeamLeadID       string     `gorm:"column:team_lead_id;size:64"`
	ProjectManagerID string     `gorm:"column:project_manager_id;size:64"`
	EngineerIDs      *string    `gorm:"column:engineer_ids;type:json"`
	ArchivedAt       *time.Time `gorm:"column:archived_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"`
}

func (testServiceItemRow) TableName() string { return "pm_service_item" }

func projectHandoverRepository(t *testing.T) (*GORMRepository, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("PM_HANDOVER_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("PM_HANDOVER_TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open disposable MySQL schema: %v", err)
	}
	// prj_audit_outbox 也在同一事务内写入，测试库必须存在该表。
	models := []any{&freezeRow{}, &receiptRow{}, &testProjectRow{}, &testServiceItemRow{}, &handoverAuditRow{}}
	for _, model := range models {
		_ = db.Migrator().DropTable(model)
	}
	t.Cleanup(func() {
		for _, model := range models {
			_ = db.Migrator().DropTable(model)
		}
	})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("create disposable schema: %v", err)
	}
	repository, err := NewGORMRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	return repository, db
}

func seedProject(t *testing.T, db *gorm.DB, tenantID, id, manager, status string) time.Time {
	t.Helper()
	updated := time.Now().UTC().Truncate(time.Millisecond)
	row := testProjectRow{ID: id, TenantID: tenantID, Name: "项目-" + id, ManagerIdentityID: manager, Status: status, UpdatedAt: updated}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return updated
}

func seedServiceItem(t *testing.T, db *gorm.DB, tenantID, id, teamLead string) time.Time {
	t.Helper()
	updated := time.Now().UTC().Truncate(time.Millisecond)
	row := testServiceItemRow{ID: id, TenantID: tenantID, Requirement: "服务项-" + id, TeamLeadID: teamLead, UpdatedAt: updated}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed service item: %v", err)
	}
	return updated
}

func handoverAuditRows(t *testing.T, db *gorm.DB, tenantID, resourceType, resourceID string) []handoverAuditRow {
	t.Helper()
	var rows []handoverAuditRow
	if err := db.Where("tenant_id = ? AND resource_type = ? AND resource_id = ?", tenantID, resourceType, resourceID).Find(&rows).Error; err != nil {
		t.Fatalf("查询交接审计: %v", err)
	}
	return rows
}

// 只有"未结束项目 + 本人为项目经理"才计入责任；已完成/已终止/他人项目一律排除。
func TestProjectResponsibilitiesOnMySQL(t *testing.T) {
	repository, db := projectHandoverRepository(t)
	ctx := context.Background()
	tenant, leaver := "tenant-pm-1", "user-leaver"
	seedProject(t, db, tenant, "P-INPROGRESS", leaver, "实施中")
	seedProject(t, db, tenant, "P-COMPLETED", leaver, "已完成")
	seedProject(t, db, tenant, "P-TERMINATED", leaver, "已终止")
	seedProject(t, db, tenant, "P-OTHER", "user-other", "实施中")
	seedProject(t, db, "tenant-pm-other", "P-TENANT", leaver, "实施中")

	now := time.Now().UTC()
	if _, created, err := repository.CreateFreeze(ctx, Freeze{ID: "01PMFREEZE000000000000001", TenantID: tenant, BatchID: "01PMBATCH0000000000000001", RequestID: "01PMREQUEST00000000000001", UserID: leaver, Status: FreezeFrozen, FrozenAt: now}); err != nil || !created {
		t.Fatalf("freeze created=%v err=%v", created, err)
	}
	items, err := repository.ListResponsibilities(ctx, tenant, leaver, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ResourceID != "P-INPROGRESS" || items[0].Category != CategoryProjectManager {
		t.Fatalf("responsibilities = %+v, want only the in-progress project", items)
	}
	if items[0].Version == 0 {
		t.Fatal("version must carry the updated_at marker used for concurrency")
	}
}

// 转移使用 updated_at 标记做并发守卫：陈旧标记 → REJECTED，绝不改数据。
func TestProjectTransferOwnershipOnMySQL(t *testing.T) {
	repository, db := projectHandoverRepository(t)
	ctx := context.Background()
	tenant, leaver, successor := "tenant-pm-2", "user-leaver", "user-successor"
	updated := seedProject(t, db, tenant, "P-1", leaver, "实施中")

	items, err := repository.ListResponsibilities(ctx, tenant, leaver, "", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("listing failed: %+v (%v)", items, err)
	}
	input := TransferInput{TenantID: tenant, BatchID: "01PMBATCH0000000000000002", ItemID: "01PMITEM00000000000000001",
		ResourceType: ResourceProject, ResourceID: "P-1", FromUserID: leaver, ToUserID: successor,
		ExpectedVersion: items[0].Version, IdempotencyKey: "handover:pm-batch:item-1"}
	receipt, err := repository.TransferOwnership(ctx, input, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != ReceiptApplied || receipt.EvidenceRef == "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	var project testProjectRow
	if err := db.First(&project, "id = ?", "P-1").Error; err != nil {
		t.Fatal(err)
	}
	if project.ManagerIdentityID != successor {
		t.Fatalf("manager = %s, want the successor", project.ManagerIdentityID)
	}
	if !project.UpdatedAt.After(updated) {
		t.Fatalf("updated_at must advance: %v → %v", updated, project.UpdatedAt)
	}

	replayed, err := repository.TransferOwnership(ctx, input, time.Now().UTC())
	if err != nil || replayed.Status != ReceiptApplied {
		t.Fatalf("replay = %+v err = %v", replayed, err)
	}
	if err := db.First(&project, "id = ?", "P-1").Error; err != nil {
		t.Fatal(err)
	}
	if project.ManagerIdentityID != successor {
		t.Fatal("replay must not change the project")
	}

	stale := input
	stale.IdempotencyKey = "handover:pm-batch:item-stale"
	stale.ExpectedVersion = items[0].Version
	rejected, err := repository.TransferOwnership(ctx, stale, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != ReceiptRejected || rejected.ReasonCode != versionConflictReason {
		t.Fatalf("stale receipt = %+v", rejected)
	}

	// 项目经理路径的审计行为保持不变：resource_type=project，重放与拒绝都不重复写。
	projectAudits := handoverAuditRows(t, db, tenant, ResourceProject, "P-1")
	if len(projectAudits) != 1 {
		t.Fatalf("项目经理交接审计行数=%d, want 1：%+v", len(projectAudits), projectAudits)
	}
	if row := projectAudits[0]; row.Action != "PROJECT_MANAGEMENT:HANDOVER_TRANSFERRED" || row.ActorType != "SYSTEM" || row.Result != "SUCCESS" {
		t.Fatalf("项目经理交接审计字段漂移：%+v", row)
	}
}

// S-3：服务项改派（team_lead_id 路径）必须写资源级审计；幂等重放不得重复写。
func TestServiceItemTransferWritesResourceAuditOnMySQL(t *testing.T) {
	repository, db := projectHandoverRepository(t)
	ctx := context.Background()
	tenant, leaver, successor := "tenant-pm-3", "user-leaver", "user-successor"
	updated := seedServiceItem(t, db, tenant, "SI-1", leaver)

	input := TransferInput{TenantID: tenant, BatchID: "01PMBATCH0000000000000003", ItemID: "01PMITEM00000000000000002",
		ResourceType: ResourceServiceItem, ResourceID: "SI-1", FromUserID: leaver, ToUserID: successor,
		ExpectedVersion: uint64(updated.UnixMilli()), IdempotencyKey: "handover:pm-batch:item-si-1"}
	receipt, err := repository.TransferOwnership(ctx, input, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != ReceiptApplied {
		t.Fatalf("receipt = %+v", receipt)
	}
	var item testServiceItemRow
	if err := db.First(&item, "id = ?", "SI-1").Error; err != nil {
		t.Fatal(err)
	}
	if item.TeamLeadID != successor {
		t.Fatalf("team_lead_id = %s, want the successor", item.TeamLeadID)
	}

	audits := handoverAuditRows(t, db, tenant, ResourceServiceItem, "SI-1")
	if len(audits) != 1 {
		t.Fatalf("服务项交接必须写一条资源级审计：%+v", audits)
	}
	if row := audits[0]; row.Action != "PROJECT_MANAGEMENT:HANDOVER_TRANSFERRED" || row.ActorType != "SYSTEM" || row.Result != "SUCCESS" {
		t.Fatalf("服务项交接审计字段漂移：%+v", row)
	}

	if replayed, err := repository.TransferOwnership(ctx, input, time.Now().UTC()); err != nil || replayed.Status != ReceiptApplied {
		t.Fatalf("replay = %+v err = %v", replayed, err)
	}
	if got := len(handoverAuditRows(t, db, tenant, ResourceServiceItem, "SI-1")); got != 1 {
		t.Fatalf("幂等重放不得重复写审计，实际 %d 条", got)
	}
}
