package mysql

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/migrations"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 隔离 MySQL 用例：只在显式提供 *_import_operation_test 专用库时运行，
// 覆盖迁移可重跑、确认事务整体回滚、同键重放与并发同键只提交一次。
func importOperationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PM_IMPORT_OPERATION_TEST_DSN"))
	if dsn == "" {
		t.Skip("requires dedicated PM_IMPORT_OPERATION_TEST_DSN")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasSuffix(config.DBName, "_import_operation_test") {
		t.Fatal("requires a dedicated *_import_operation_test database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func applyImportOperationMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	body, err := migrations.Files.ReadFile("000043_import_operation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(body)).Error; err != nil {
		t.Fatalf("apply 000043 failed: %v", err)
	}
}

func importOperationTestTenant(t *testing.T, db *gorm.DB) string {
	t.Helper()
	const tenant = "IMPORT-OPERATION-TEST"
	cleanup := func() {
		for _, statement := range []string{
			`DELETE FROM pm_capability WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_import_operation WHERE tenant_id = '` + tenant + `'`,
		} {
			db.Exec(statement)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	return tenant
}

func importOperationFixture(tenant, key, hash string) application.ImportOperation {
	return application.ImportOperation{
		ID:             "01JIMPORTOPERATION" + strings.ToUpper(key[len(key)-4:]),
		TenantID:       tenant,
		IdempotencyKey: key,
		OperationType:  application.ImportOperationEquipment,
		RequestHash:    hash,
		CreatedAt:      time.Now().UTC(),
	}
}

func importOperationEquipment(tenant, resourceID string) domain.Capability {
	return domain.Capability{TenantID: tenant, ResourceType: "EQUIPMENT", ResourceID: resourceID, ResourceName: "扫描设备", Codes: []string{"C1"}, Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny}
}

// TestImportOperationMigrationIsRerunnableOnMySQL 断言 000043 在隔离库可执行且可重跑，
// 唯一键与响应快照列同表存在（同键重放依赖它们）。
func TestImportOperationMigrationIsRerunnableOnMySQL(t *testing.T) {
	db := importOperationTestDB(t)
	applyImportOperationMigration(t, db)
	// DDL 隐式提交后、迁移记账前中断时重跑必须成功（CREATE TABLE IF NOT EXISTS）。
	applyImportOperationMigration(t, db)
	type indexColumn struct {
		ColumnName string
		NonUnique  int
		SeqInIndex int
	}
	var columns []indexColumn
	if err := db.Raw("SELECT COLUMN_NAME, NON_UNIQUE, SEQ_IN_INDEX FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pm_import_operation' AND INDEX_NAME = 'uq_pm_import_operation_key' ORDER BY SEQ_IN_INDEX").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0].ColumnName != "tenant_id" || columns[1].ColumnName != "idempotency_key" || columns[0].NonUnique != 0 || columns[1].NonUnique != 0 {
		t.Fatalf("import operation unique key missing or malformed: %+v", columns)
	}
	var responseColumn string
	if err := db.Raw("SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pm_import_operation' AND COLUMN_NAME = 'response_json' AND DATA_TYPE = 'longtext'").Scan(&responseColumn).Error; err != nil || responseColumn != "response_json" {
		t.Fatalf("response_json column must be LONGTEXT so replayed bytes stay identical: %q %v", responseColumn, err)
	}
}

// TestImportConfirmationRollsBackWholeBatchAndReplays 断言确认事务全有或全无：
// 第二行失败时第一行不落库、不登记记录；成功只登记一次，同键重放返回相同响应。
func TestImportConfirmationRollsBackWholeBatchAndReplays(t *testing.T) {
	db := importOperationTestDB(t)
	applyImportOperationMigration(t, db)
	if err := db.AutoMigrate(&capabilityRecord{}); err != nil {
		t.Fatal(err)
	}
	ensureCapabilityImportProductionUniqueIndex(t, db)
	tenant := importOperationTestTenant(t, db)
	repo := NewRepository(db)
	ctx := context.Background()
	rows := []domain.Capability{importOperationEquipment(tenant, "EQ-OP-0001"), importOperationEquipment(tenant, "EQ-OP-0002")}
	op := importOperationFixture(tenant, "equipment-import-key-1", strings.Repeat("a", 64))

	_, replayed, err := repo.RunImportConfirmation(ctx, op, func(writer application.ImportRowWriter) (application.ImportConfirmationResult, error) {
		if _, writeErr := writer.CreateEquipmentForImport(ctx, rows[0], "tester"); writeErr != nil {
			return application.ImportConfirmationResult{}, writeErr
		}
		return application.ImportConfirmationResult{}, application.ValidationError("第 3 行：设备编号已被其他请求占用")
	})
	if err == nil || replayed {
		t.Fatalf("failed batch must return the row error and not replay: replayed=%v err=%v", replayed, err)
	}
	var capabilityRows int64
	var operationRows int64
	if err := db.Model(&capabilityRecord{}).Where("tenant_id = ?", tenant).Count(&capabilityRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&importOperationRecord{}).Where("tenant_id = ?", tenant).Count(&operationRows).Error; err != nil {
		t.Fatal(err)
	}
	if capabilityRows != 0 || operationRows != 0 {
		t.Fatalf("failed batch leaked writes: capability=%d operation=%d", capabilityRows, operationRows)
	}

	first, replayed, err := repo.RunImportConfirmation(ctx, op, func(writer application.ImportRowWriter) (application.ImportConfirmationResult, error) {
		for _, row := range rows {
			if _, writeErr := writer.CreateEquipmentForImport(ctx, row, "tester"); writeErr != nil {
				return application.ImportConfirmationResult{}, writeErr
			}
		}
		return application.ImportConfirmationResult{Imported: 2, Errors: []string{}, Rows: []application.ImportOutcomeRow{{RowNo: 2, Status: "IMPORTED", Message: "导入成功"}, {RowNo: 3, Status: "IMPORTED", Message: "导入成功"}}}, nil
	})
	if err != nil || replayed || first.RequestHash != op.RequestHash {
		t.Fatalf("first confirmation failed: replayed=%v err=%v record=%+v", replayed, err, first)
	}
	if err := db.Model(&capabilityRecord{}).Where("tenant_id = ?", tenant).Count(&capabilityRows).Error; err != nil {
		t.Fatal(err)
	}
	if capabilityRows != 2 {
		t.Fatalf("committed rows=%d want=2", capabilityRows)
	}

	replay, replayed, err := repo.RunImportConfirmation(ctx, op, func(writer application.ImportRowWriter) (application.ImportConfirmationResult, error) {
		t.Fatal("replay must not execute the write callback")
		return application.ImportConfirmationResult{}, nil
	})
	if err != nil || !replayed || replay.ResponseJSON != first.ResponseJSON || replay.ID != first.ID {
		t.Fatalf("replay mismatch: replayed=%v err=%v replay=%+v first=%+v", replayed, err, replay, first)
	}
	if err := db.Model(&capabilityRecord{}).Where("tenant_id = ?", tenant).Count(&capabilityRows).Error; err != nil {
		t.Fatal(err)
	}
	if capabilityRows != 2 {
		t.Fatalf("replay wrote again: rows=%d", capabilityRows)
	}

	// 同键不同请求指纹由应用层判定 409；仓储只负责稳定回读首次记录。
	different := op
	different.RequestHash = strings.Repeat("b", 64)
	replay, replayed, err = repo.RunImportConfirmation(ctx, different, func(application.ImportRowWriter) (application.ImportConfirmationResult, error) {
		t.Fatal("reused key must not execute the write callback")
		return application.ImportConfirmationResult{}, nil
	})
	if err != nil || !replayed || replay.RequestHash != op.RequestHash {
		t.Fatalf("reused key must read back the first record: replayed=%v err=%v record=%+v", replayed, err, replay)
	}
}

// TestImportConfirmationConcurrentSameKeyCommitsOnce 断言并发同键只有一个事务提交；
// 另一个事务整体回滚并读回首次结果，不会重复写入设备。
func TestImportConfirmationConcurrentSameKeyCommitsOnce(t *testing.T) {
	db := importOperationTestDB(t)
	applyImportOperationMigration(t, db)
	if err := db.AutoMigrate(&capabilityRecord{}); err != nil {
		t.Fatal(err)
	}
	ensureCapabilityImportProductionUniqueIndex(t, db)
	tenant := importOperationTestTenant(t, db)
	repo := NewRepository(db)
	ctx := context.Background()
	op := importOperationFixture(tenant, "equipment-import-key-2", strings.Repeat("c", 64))

	type outcome struct {
		replayed bool
		imported int
		err      error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record, replayed, err := repo.RunImportConfirmation(ctx, op, func(writer application.ImportRowWriter) (application.ImportConfirmationResult, error) {
				if _, writeErr := writer.CreateEquipmentForImport(ctx, importOperationEquipment(tenant, "EQ-OP-CONCURRENT"), "tester"); writeErr != nil {
					return application.ImportConfirmationResult{}, writeErr
				}
				return application.ImportConfirmationResult{Imported: 1, Errors: []string{}, Rows: []application.ImportOutcomeRow{{RowNo: 2, Status: "IMPORTED", Message: "导入成功"}}}, nil
			})
			result := outcome{replayed: replayed, err: err}
			if record.ResponseJSON != "" {
				result.imported = 1
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	committed, replayedCount := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent same-key confirmation failed: %v", result.err)
		}
		if result.replayed {
			replayedCount++
		} else {
			committed++
		}
	}
	if committed != 1 || replayedCount != 1 {
		t.Fatalf("expected exactly one commit and one replay, committed=%d replayed=%d", committed, replayedCount)
	}
	var capabilityRows int64
	var operationRows int64
	if err := db.Model(&capabilityRecord{}).Where("tenant_id = ?", tenant).Count(&capabilityRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&importOperationRecord{}).Where("tenant_id = ?", tenant).Count(&operationRows).Error; err != nil {
		t.Fatal(err)
	}
	if capabilityRows != 1 || operationRows != 1 {
		t.Fatalf("concurrent same-key wrote more than once: capability=%d operation=%d", capabilityRows, operationRows)
	}
}
