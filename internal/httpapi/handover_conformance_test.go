package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/handover"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 测试用的最小项目模型（真实仓储查询/更新的列）。
type conformanceProjectRow struct {
	ID                string    `gorm:"column:id;size:32;primaryKey"`
	TenantID          string    `gorm:"column:tenant_id;size:64"`
	Name              string    `gorm:"column:name"`
	ManagerIdentityID string    `gorm:"column:manager_identity_id;size:128"`
	Status            string    `gorm:"column:status;size:32"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (conformanceProjectRow) TableName() string { return "pm_project" }

// handoverConformanceRouter 用真实仓储（隔离 MySQL）+ 真实服务挂载交接端点。
func handoverConformanceRouter(t *testing.T, tenantID string) (*gin.Engine, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("PM_HANDOVER_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("PM_HANDOVER_TEST_MYSQL_DSN is not configured; no database connection attempted")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open disposable MySQL schema: %v", err)
	}
	_ = db.Exec(`CREATE TABLE IF NOT EXISTS pm_handover_freeze (id CHAR(26) NOT NULL, tenant_id VARCHAR(64) NOT NULL, batch_id CHAR(26) NOT NULL, request_id CHAR(26) NOT NULL, user_id VARCHAR(64) NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'FROZEN', frozen_at DATETIME(3) NOT NULL, released_at DATETIME(3) NULL, outcome VARCHAR(16) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL, PRIMARY KEY (id), UNIQUE KEY uk_pm_handover_freeze_batch_user (tenant_id, batch_id, user_id), KEY idx_pm_handover_freeze_active (tenant_id, user_id, status)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error
	_ = db.Exec(`CREATE TABLE IF NOT EXISTS pm_handover_receipt (id CHAR(26) NOT NULL, tenant_id VARCHAR(64) NOT NULL, batch_id CHAR(26) NOT NULL, item_id CHAR(26) NOT NULL, resource_type VARCHAR(64) NOT NULL, resource_id VARCHAR(128) NOT NULL, from_user_id VARCHAR(64) NOT NULL, to_user_id VARCHAR(64) NOT NULL, idempotency_key VARCHAR(160) NOT NULL, status VARCHAR(24) NOT NULL, applied_version BIGINT UNSIGNED NOT NULL DEFAULT 0, reason_code VARCHAR(64) NULL, evidence_ref VARCHAR(255) NULL, received_at DATETIME(3) NOT NULL, created_at DATETIME(3) NOT NULL, PRIMARY KEY (id), UNIQUE KEY uk_pm_handover_receipt_key (tenant_id, idempotency_key), KEY idx_pm_handover_receipt_item (tenant_id, item_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error
	if err := db.AutoMigrate(&conformanceProjectRow{}); err != nil {
		t.Fatalf("create disposable project table: %v", err)
	}
	for _, statement := range []string{
		"DELETE FROM pm_handover_freeze WHERE tenant_id = ?",
		"DELETE FROM pm_handover_receipt WHERE tenant_id = ?",
		"DELETE FROM pm_project WHERE tenant_id = ?",
	} {
		if err := db.Exec(statement, tenantID).Error; err != nil {
			t.Fatalf("clear test data: %v", err)
		}
	}
	repository, err := handover.NewGORMRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := handover.NewService(repository, handover.NewID, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{handover: service}
	router := gin.New()
	internal := router.Group("/internal/v1")
	internal.Use(handler.authenticateHandoverIntegration(HandoverIntegrationOptions{Enabled: true,
		BearerVerifier: handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: tenantID}}}))
	internal.POST("/handover/freeze", handler.handoverFreeze)
	internal.GET("/handover/responsibilities", handler.handoverResponsibilities)
	internal.POST("/handover/transfers", handler.handoverTransfer)
	internal.GET("/handover/transfers/:item_id", handler.handoverReceipt)
	internal.POST("/handover/complete", handler.handoverComplete)
	return router, db
}

// TestProjectHandoverProtocolMatchesPlatformOrchestration 按平台编排器的真实请求序列走完流程。
func TestProjectHandoverProtocolMatchesPlatformOrchestration(t *testing.T) {
	tenant := "tenant-conformance"
	router, db := handoverConformanceRouter(t, tenant)
	leaver, successor := "user-leaver", "user-successor"
	now := time.Now().UTC()
	project := conformanceProjectRow{ID: "P-CONF", TenantID: tenant, Name: "一致性项目", ManagerIdentityID: leaver, Status: "实施中", UpdatedAt: now}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	batchID, itemID := "01PMCONFBATCH000000000001", "01PMCONFITEM0000000000001"

	status, payload := handoverCall(t, router, http.MethodPost, "/internal/v1/handover/freeze",
		fmt.Sprintf(`{"batch_id":"%s","request_id":"%s","user_id":"%s"}`, batchID, itemID, leaver), "machine-token")
	if status != http.StatusOK {
		t.Fatalf("freeze → %d %v", status, payload)
	}
	data := payload["data"].(map[string]any)
	if data["system"] != "project" || data["frozen"] != true || data["responsibility_count"].(float64) != 1 {
		t.Fatalf("freeze payload = %v", payload)
	}

	status, payload = handoverCall(t, router, http.MethodGet, "/internal/v1/handover/responsibilities?user_id="+leaver+"&limit=10", "", "machine-token")
	if status != http.StatusOK {
		t.Fatalf("responsibilities → %d %v", status, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("responsibilities = %v", items)
	}
	listed := items[0].(map[string]any)
	if listed["resource_id"] != "P-CONF" || listed["category"] != handover.CategoryProjectManager {
		t.Fatalf("listed responsibility = %v", listed)
	}
	expectedVersion := uint64(listed["version"].(float64))

	transferBody := fmt.Sprintf(`{"batch_id":"%s","item_id":"%s","resource_type":"project","resource_id":"P-CONF","from_user_id":"%s","to_user_id":"%s","expected_version":%d,"idempotency_key":"handover:%s:%s"}`,
		batchID, itemID, leaver, successor, expectedVersion, batchID, itemID)
	status, payload = handoverCall(t, router, http.MethodPost, "/internal/v1/handover/transfers", transferBody, "machine-token")
	if status != http.StatusOK {
		t.Fatalf("transfer → %d %v", status, payload)
	}
	receipt := payload["data"].(map[string]any)
	if receipt["status"] != handover.ReceiptApplied || receipt["evidence_ref"] == "" {
		t.Fatalf("receipt = %v", receipt)
	}
	var stored conformanceProjectRow
	if err := db.First(&stored, "id = ?", "P-CONF").Error; err != nil {
		t.Fatal(err)
	}
	if stored.ManagerIdentityID != successor {
		t.Fatalf("manager = %s, want successor", stored.ManagerIdentityID)
	}

	// 重放同一幂等键：返回同回执且不再改动数据。
	status, payload = handoverCall(t, router, http.MethodPost, "/internal/v1/handover/transfers", transferBody, "machine-token")
	if status != http.StatusOK || payload["data"].(map[string]any)["status"] != handover.ReceiptApplied {
		t.Fatalf("replay → %d %v", status, payload)
	}
	before := stored.UpdatedAt
	if err := db.First(&stored, "id = ?", "P-CONF").Error; err != nil {
		t.Fatal(err)
	}
	if !stored.UpdatedAt.Equal(before) {
		t.Fatalf("replay changed updated_at: %v → %v", before, stored.UpdatedAt)
	}

	// 陈旧标记：REJECTED + 原因码，且不改数据。
	staleBody := strings.Replace(transferBody, fmt.Sprintf(`"expected_version":%d`, expectedVersion), `"expected_version":1`, 1)
	staleBody = strings.Replace(staleBody, "handover:", "handover-stale:", 1)
	status, payload = handoverCall(t, router, http.MethodPost, "/internal/v1/handover/transfers", staleBody, "machine-token")
	if status != http.StatusOK {
		t.Fatalf("stale transfer → %d %v", status, payload)
	}
	stale := payload["data"].(map[string]any)
	if stale["status"] != handover.ReceiptRejected || stale["reason_code"] != "PM_VERSION_CONFLICT" {
		t.Fatalf("stale receipt = %v", stale)
	}

	// 完成：解冻，已完成转移保留。
	status, payload = handoverCall(t, router, http.MethodPost, "/internal/v1/handover/complete",
		fmt.Sprintf(`{"batch_id":"%s","outcome":"COMPLETED"}`, batchID), "machine-token")
	if status != http.StatusOK {
		t.Fatalf("complete → %d %v", status, payload)
	}
	var freezeStatus string
	if err := db.Table("pm_handover_freeze").Select("status").Where("tenant_id = ? AND batch_id = ?", tenant, batchID).Row().Scan(&freezeStatus); err != nil {
		t.Fatal(err)
	}
	if freezeStatus != handover.FreezeReleased {
		t.Fatalf("freeze status = %s", freezeStatus)
	}
	if err := db.First(&stored, "id = ?", "P-CONF").Error; err != nil {
		t.Fatal(err)
	}
	if stored.ManagerIdentityID != successor {
		t.Fatalf("completed transfer must be preserved, manager = %s", stored.ManagerIdentityID)
	}
}
