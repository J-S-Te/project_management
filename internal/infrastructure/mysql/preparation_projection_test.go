package mysql

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// TestPreparationProjectionOutlivesRecentEvents 验证退回标记被全局窗口淘汰后仍能读取当前节点。
// 参数：t 为 SQLite 隔离测试上下文。
// 返回值：无；读取错租户事件、旧标记或未返回当前节点时失败。
func TestPreparationProjectionOutlivesRecentEvents(t *testing.T) {
	db, item := fieldReturnFixture(t)
	if err := db.AutoMigrate(&penetrationWorkPackageRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&serviceItemRecord{}).Where("id=?", item.ID).
		Update("status", domain.ProjectStatusPreparing).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	records := []deliveryEventRecord{
		preparationProjectionMarker(item, "T01", "PREPARATION_STARTED", "{}", now),
		preparationProjectionMarker(item, "T02", "FIELD_RETURNED_TO_PREPARATION", "{}", now.Add(time.Second)),
		preparationProjectionMarker(item, "T03", "ROLLBACK_APPROVED", `{"kind":"REPORT_TO_FIELD"}`, now.Add(2*time.Second)),
		preparationProjectionMarker(item, "T04", "PREPARATION_STARTED", "{}", now.Add(3*time.Second)),
	}
	records[3].TenantID = "foreign-tenant"
	for i := range 505 {
		records = append(records, preparationProjectionMarker(item, fmt.Sprintf("NOISE-%03d", i),
			application.EventFieldRecordSubmitted, "{}", now.Add(4*time.Second)))
	}
	if err := db.CreateInBatches(records, 100).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: item.TenantID, AllowAll: true}
	recent, err := repository.ListDeliveryEvents(t.Context(), filter, "")
	if err != nil || len(recent) != 500 {
		t.Fatalf("全局事件窗口数量=%d，错误=%v", len(recent), err)
	}
	for _, event := range recent {
		if event.Type != application.EventFieldRecordSubmitted {
			t.Fatalf("准备标记应已被最新 500 条事件淘汰，仍返回了 %+v", event)
		}
	}
	assertPreparationProjection(t, repository, item, pointerTo(false))
	assertRepeatedPreparationProjection(t, repository, item, now.Add(5*time.Second))
}

// assertRepeatedPreparationProjection 验证多轮准备、旧式回退和同时间戳的次序。
// 参数：t 为测试上下文，repository 为隔离仓储，item 为服务项，at 为标记开始时间。
// 返回值：无；服务项节点未随较新标记变化或其他状态未省略字段时失败。
func assertRepeatedPreparationProjection(t *testing.T, repository *Repository, item serviceItemRecord, at time.Time) {
	t.Helper()
	for _, step := range []struct {
		id, typ, payload string
		at               time.Time
		want             bool
	}{
		{"T05", "PREPARATION_STARTED", "{}", at, true},
		{"T06", "ROLLBACK_APPROVED", `{"kind":"FIELD_TO_PREPARATION"}`, at, false},
		{"T07", "PREPARATION_STARTED", "{}", at.Add(time.Second), true},
	} {
		marker := preparationProjectionMarker(item, step.id, step.typ, step.payload, step.at)
		if err := repository.db.Create(&marker).Error; err != nil {
			t.Fatal(err)
		}
		assertPreparationProjection(t, repository, item, pointerTo(step.want))
	}
	if err := repository.db.Model(&serviceItemRecord{}).Where("id=?", item.ID).
		Update("status", domain.ProjectStatusInProgress).Error; err != nil {
		t.Fatal(err)
	}
	assertPreparationProjection(t, repository, item, nil)
}

// TestPreparationProjectionOmitsUnknownHistory 验证无节点证据的存量准备服务项省略可选字段。
// 参数：t 为 SQLite 隔离测试上下文。
// 返回值：无；缺失标记时返回布尔猜测则失败。
func TestPreparationProjectionOmitsUnknownHistory(t *testing.T) {
	db, item := fieldReturnFixture(t)
	if err := db.AutoMigrate(&penetrationWorkPackageRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&serviceItemRecord{}).Where("id=?", item.ID).
		Update("status", domain.ProjectStatusPreparing).Error; err != nil {
		t.Fatal(err)
	}
	assertPreparationProjection(t, NewRepository(db), item, nil)
}

// preparationProjectionMarker 构造用于节点顺序验证的交付事件记录。
// 参数：item 为服务项，id/typ/payload 为事件编号、类型和载荷，at 为创建时间。
// 返回值：待插入的隔离数据库行。
func preparationProjectionMarker(item serviceItemRecord, id, typ, payload string, at time.Time) deliveryEventRecord {
	return deliveryEventRecord{ID: id, TenantID: item.TenantID, ProjectID: item.ProjectID,
		ServiceItemID: item.ID, EventType: typ, Payload: []byte(payload), CreatedAt: at}
}

// assertPreparationProjection 核对授权列表和精准读取返回的 JSON 字段。
// 参数：t 为测试上下文，repository 为隔离仓储，item 为服务项，want 为预期值或 nil。
// 返回值：无；字段值错误，或应省略时输出 null/布尔值则失败。
func assertPreparationProjection(t *testing.T, repository *Repository, item serviceItemRecord, want *bool) {
	t.Helper()
	filter := platform.ScopeFilter{TenantID: item.TenantID, AllowAll: true}
	items, total, err := repository.ListServiceItems(t.Context(), filter, item.ProjectID, 1, 10)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("服务项列表 total=%d，实际=%d，错误=%v", total, len(items), err)
	}
	detail, err := repository.GetServiceItem(t.Context(), filter, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []domain.ServiceItem{items[0], detail} {
		body, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(body, &data); err != nil {
			t.Fatal(err)
		}
		value := string(data["preparation_confirmed"])
		expected := ""
		if want != nil {
			expected = fmt.Sprint(*want)
		}
		if value != expected {
			t.Fatalf("服务项 %s 的 preparation_confirmed=%q，期望=%q", row.ID, value, expected)
		}
	}
}
