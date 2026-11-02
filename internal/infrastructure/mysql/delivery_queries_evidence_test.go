package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/migrations"
	"github.com/oklog/ulid/v2"
	drivermysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// BIZ-PROJECT-06：现场完成缺项判定必须按 service_item_id 精确读取该服务项的完整事件流，
// 不能沿用"租户最近 500 条事件"的窗口。本文件覆盖两层：
//   - DryRun 断言 SQL 形状（精确过滤 / id 升序 / 防御性上限），不需要数据库；
//   - PM_TEST_DSN 门控的集成用例验证语义（只取本服务项、跨租户不可见、超限报错）。

func TestListServiceItemDeliveryEventsPushesExactFilterAndAscendingOrder(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, err := repository.ListServiceItemDeliveryEvents(context.Background(), filter, "SI-EVIDENCE-1"); err != nil {
		t.Fatalf("list service item events: %v", err)
	}
	if len(capture.queries) != 1 {
		t.Fatalf("expected exactly one query, got %d: %v", len(capture.queries), capture.queries)
	}
	query := capture.queries[0]
	for _, expected := range []string{"service_item_id = 'SI-EVIDENCE-1'", "tenant_id = 'tenant-1'", "ORDER BY id ASC", fmt.Sprintf("LIMIT %d", application.MaxServiceItemDeliveryEvents+1)} {
		if !strings.Contains(query, expected) {
			t.Fatalf("service item event query must contain %q: %s", expected, query)
		}
	}
	// 窗口近似必须消失：既不能按 created_at 倒排，也不能退化成租户级 limit 500。
	for _, forbidden := range []string{"ORDER BY created_at", "LIMIT 500"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("service item event query must not contain %q: %s", forbidden, query)
		}
	}
	if _, err := repository.ListServiceItemDeliveryEvents(context.Background(), filter, "  "); !errors.Is(err, application.ErrValidation) {
		t.Fatalf("blank service item id error = %v, want validation", err)
	}
}

func TestDeliveryEventWindowFailsClosedInsteadOfTruncating(t *testing.T) {
	if err := assertDeliveryEventWindow(application.MaxServiceItemDeliveryEvents); err != nil {
		t.Fatalf("limit boundary must pass: %v", err)
	}
	if err := assertDeliveryEventWindow(application.MaxServiceItemDeliveryEvents + 1); !errors.Is(err, application.ErrFieldEvidenceWindowExceeded) {
		t.Fatalf("overflow error = %v, want window exceeded", err)
	}
}

func TestListServiceItemDeliveryEventsAgainstRealDatabase(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set dedicated PM_TEST_DSN to run the service-item evidence integration check")
	}
	ctx := context.Background()
	if err := migration.Run(ctx, dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(drivermysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	tenant := ulid.Make().String()
	otherTenant := ulid.Make().String()
	t.Cleanup(func() {
		for _, table := range []string{"pm_delivery_event", "pm_service_item", "pm_project"} {
			if err := db.Exec("DELETE FROM "+table+" WHERE tenant_id IN (?, ?)", tenant, otherTenant).Error; err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.Create(&projectRecord{TenantID: tenant, ID: "PJ-EVIDENCE-IT", Name: "证据链项目", Customer: "示例客户", Status: "实施中", Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&projectRecord{TenantID: otherTenant, ID: "PJ-EVIDENCE-OTHER", Name: "其它租户项目", Customer: "其它客户", Status: "实施中", Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	// 同一项目下两个服务项的事件必须互不串味；跨租户同 ID 也不能被读到。
	for _, record := range []deliveryEventRecord{
		{ID: "EV-IT-01", TenantID: tenant, ProjectID: "PJ-EVIDENCE-IT", ServiceItemID: "SI-EVIDENCE-IT", EventType: application.EventFieldCheckedIn, ActorUserID: "engineer-1", Payload: []byte(`{"latitude":30.2741,"longitude":120.1551}`), CreatedAt: now},
		{ID: "EV-IT-02", TenantID: tenant, ProjectID: "PJ-EVIDENCE-IT", ServiceItemID: "SI-OTHER-IT", EventType: application.EventFieldRecordSubmitted, ActorUserID: "engineer-1", Payload: []byte(`{}`), CreatedAt: now.Add(time.Millisecond)},
		{ID: "EV-IT-03", TenantID: otherTenant, ProjectID: "PJ-EVIDENCE-OTHER", ServiceItemID: "SI-EVIDENCE-IT", EventType: application.EventFieldCheckedIn, ActorUserID: "engineer-2", Payload: []byte(`{}`), CreatedAt: now.Add(2 * time.Millisecond)},
		{ID: "EV-IT-04", TenantID: tenant, ProjectID: "PJ-EVIDENCE-IT", ServiceItemID: "SI-EVIDENCE-IT", EventType: application.EventFieldSignatureCaptured, ActorUserID: "engineer-1", Payload: []byte(`{"signature_file":{"file_id":"FILE-IT-SIGN"}}`), CreatedAt: now.Add(3 * time.Millisecond)},
	} {
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	repository := NewRepository(db)
	// AllowAll 仍受 tenant_id 约束：跨租户同服务项 ID 的事件不得进入判定。
	filter := platform.ScopeFilter{TenantID: tenant, AllowAll: true}
	events, err := repository.ListServiceItemDeliveryEvents(ctx, filter, "SI-EVIDENCE-IT")
	if err != nil {
		t.Fatalf("list service item events: %v", err)
	}
	if len(events) != 2 || events[0].ID != "EV-IT-01" || events[1].ID != "EV-IT-04" {
		t.Fatalf("service item events = %+v, want EV-IT-01 then EV-IT-04", events)
	}
	if events[1].Payload["signature_file"] == nil {
		t.Fatalf("payload must be decoded for evidence file refs: %+v", events[1].Payload)
	}
	// 事件数超过上限：报错而不是返回被截断的前 1000 条。
	overflow := make([]deliveryEventRecord, 0, application.MaxServiceItemDeliveryEvents+1)
	for index := 0; index <= application.MaxServiceItemDeliveryEvents; index++ {
		overflow = append(overflow, deliveryEventRecord{
			ID: fmt.Sprintf("EV-OVF-%05d", index), TenantID: tenant, ProjectID: "PJ-EVIDENCE-IT",
			ServiceItemID: "SI-OVERFLOW-IT", EventType: application.EventFieldCheckedIn, ActorUserID: "engineer-1",
			Payload: []byte(`{}`), CreatedAt: now,
		})
	}
	if err := db.CreateInBatches(&overflow, 200).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ListServiceItemDeliveryEvents(ctx, filter, "SI-OVERFLOW-IT"); !errors.Is(err, application.ErrFieldEvidenceWindowExceeded) {
		t.Fatalf("overflow error = %v, want window exceeded", err)
	}
}
