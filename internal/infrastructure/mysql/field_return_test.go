package mysql

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// fieldReturnFixture 创建隔离的现场服务项、设备计划及回退事件表。
// 参数：t 为 SQLite 内存测试上下文。
// 返回值：数据库连接与实施中服务项；不使用外部数据库。
func fieldReturnFixture(t *testing.T) (*gorm.DB, serviceItemRecord) {
	t.Helper()
	db := equipmentAtomicityDB(t)
	if err := db.AutoMigrate(&deliveryEventRecord{}, &auditOutboxRecord{}, &automationRecord{},
		&deliveryEffectsRecord{}, &evidenceFileRecord{}, &notificationOutboxRecord{}); err != nil {
		t.Fatal(err)
	}
	item := atomicityPlan(t, db, "field-return", "实施中", []domain.PlanResource{{
		ResourceType: "EQUIPMENT", ResourceID: "EQ-1",
	}})
	if err := db.Create(&projectRecord{ID: item.ProjectID, TenantID: item.TenantID, SourceKind: "CONTRACT"}).Error; err != nil {
		t.Fatal(err)
	}
	rows := []any{
		&equipmentReservationRecord{TenantID: item.TenantID, ServiceItemID: item.ID,
			ResourceID: "EQ-1", UseDate: *item.PlannedStart},
		&evidenceFileRecord{TenantID: item.TenantID, ServiceItemID: item.ID,
			EvidenceKind: "FIELD", FileID: "FILE-1"},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, item
}

// TestFieldReturnClosesPriorRollbackAndPreservesEvidence 验证直退的事务状态、原证据及旧申请收口。
// 参数：t 为 SQLite 回归测试上下文。
// 返回值：无；丢失历史证据、未落审计或误批准历史申请时失败。
func TestFieldReturnClosesPriorRollbackAndPreservesEvidence(t *testing.T) {
	db, item := fieldReturnFixture(t)
	repository := NewRepository(db)
	event := domain.DeliveryEvent{
		ID: "RETURN-1", TenantID: item.TenantID, ServiceItemID: item.ID,
		Type: "FIELD_RETURNED_TO_PREPARATION", ActorUserID: "manager",
		CreatedAt: time.Now().UTC(), Payload: map[string]any{"reason": "补核条件", "expected_version": uint64(1)},
	}
	legacy := event
	legacy.ID, legacy.Type = "LEGACY-REQUEST", application.EventRollbackRequested
	legacy.Payload = map[string]any{"kind": "FIELD_TO_PREPARATION", "expected_version": uint64(1), "reason": "旧申请"}
	if err := repository.ApplyDeliveryEvent(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if err := repository.ApplyDeliveryEvent(t.Context(), event); err != nil {
		t.Fatalf("现场服务项直退：%v", err)
	}
	assertFieldReturnPersistence(t, db, item, event)
	var workflow workflowRevisionRecord
	if err := db.Where("tenant_id=? AND project_id=? AND service_item_id=?",
		item.TenantID, item.ProjectID, item.ID).Take(&workflow).Error; err != nil || workflow.Revision != 3 {
		t.Fatalf("原申请、自动关闭和现场直退须共同推进工作流修订：修订=%d，错误=%v", workflow.Revision, err)
	}
	restart := event
	restart.ID, restart.Type = "FIELD-RESTART", application.EventFieldStarted
	restart.Payload = map[string]any{"expected_version": uint64(2)}
	if err := repository.ApplyDeliveryEvent(t.Context(), restart); err != nil {
		t.Fatalf("重新开始现场：%v", err)
	}
	approve := event
	approve.ID, approve.Type, approve.ActorUserID = "LATE-APPROVAL", application.EventRollbackApproved, "approver"
	approve.Payload = map[string]any{"request_id": legacy.ID, "expected_version": uint64(3)}
	if err := repository.ApplyDeliveryEvent(t.Context(), approve); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("历史申请不应作用于新一轮实施，实际错误=%v", err)
	}
}

// assertFieldReturnPersistence 检查直退后的业务状态、审计与存量数据。
// 参数：t 为测试上下文，db 为隔离连接，item 为旧服务项，event 为直退事件。
// 返回值：无；任一状态或持久化不变量不成立时失败。
func assertFieldReturnPersistence(t *testing.T, db *gorm.DB, item serviceItemRecord, event domain.DeliveryEvent) {
	t.Helper()
	var current serviceItemRecord
	if err := db.Where("id=?", item.ID).Take(&current).Error; err != nil {
		t.Fatal(err)
	}
	var project projectRecord
	if err := db.Where("id=?", item.ProjectID).Take(&project).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != "实施准备中" || project.Status != "实施准备中" || current.Version != 2 ||
		current.StatusChangedAt == nil || !current.StatusChangedAt.Equal(event.CreatedAt) {
		t.Fatalf("服务项=%s/%d 项目=%s 状态时间=%v", current.Status, current.Version, project.Status, current.StatusChangedAt)
	}
	var closed deliveryEventRecord
	if err := db.Where("event_type=?", application.EventRollbackRejected).Take(&closed).Error; err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(closed.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if closed.ActorUserID != "SYSTEM" || payload["cause_event_id"] != event.ID ||
		payload["request_id"] != "LEGACY-REQUEST" || payload["initiated_by"] != event.ActorUserID {
		t.Fatalf("历史申请未保留自动失效因果：%+v", payload)
	}
	for _, query := range []*gorm.DB{
		db.Model(&equipmentReservationRecord{}).Where("service_item_id=? AND resource_id=?", item.ID, "EQ-1"),
		db.Model(&implPlanRecord{}).Where("service_item_id=?", item.ID),
		db.Model(&evidenceFileRecord{}).Where("service_item_id=? AND file_id=?", item.ID, "FILE-1"),
		db.Model(&auditOutboxRecord{}).Where("action=? AND actor_type=?", "PROJECT_MANAGEMENT:"+event.Type, "USER"),
		db.Model(&notificationOutboxRecord{}).
			Where("event_id=? AND event_type=? AND status=?", closed.ID, application.EventRollbackRejected, "PENDING"),
	} {
		var count int64
		if err := query.Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("现有证据或直退审计未保留，数量=%d，错误=%v", count, err)
		}
	}
}
