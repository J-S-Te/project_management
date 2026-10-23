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
	models := []any{&freezeRow{}, &receiptRow{}, &testProjectRow{}}
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
}
