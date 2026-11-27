package mysql

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

// TestImplementationPersonnelStayIndependentOnMySQL 用途：验证同项目两个服务项发布及重发时分别落库。
// 参数：t 为 Go 测试上下文，数据库由隔离测试 DSN 指定。
// 返回值：无；各项计划须保留自己的人员集合。
func TestImplementationPersonnelStayIndependentOnMySQL(t *testing.T) {
	db := equipmentConcurrencyMySQL(t)
	tenant, items := seedIndependentPersonnelItems(t, db)
	ctx := context.Background()
	service := &application.Service{Repo: NewRepository(db)}
	principal := platform.Principal{TenantID: tenant, UserID: "manager", IdentityID: "manager",
		Permissions: map[string]bool{"project.implementation.plan": true, "project.implementation.revoke": true},
		DataScopes:  []platform.DataScope{{RoleCode: "project_manager", ScopeType: "APPLICATION"}},
	}
	publish := func(item, user string, version uint64) {
		t.Helper()
		input := domain.ImplementationPlanInput{ExpectedVersion: version,
			PlannedStart: "2026-11-27T09:00:00+08:00", PlannedEnd: "2026-11-27T18:00:00+08:00",
			Personnel: []domain.PlanResourceInput{{ResourceType: "PERSON", ResourceID: user}},
		}
		if err := service.PlanImplementation(ctx, principal, item, input); err != nil {
			t.Fatalf("publish %s: %v", item, err)
		}
	}
	publish(items[0], "engineer-a", 1)
	publish(items[1], "engineer-b", 1)
	assertStoredPersonnel(t, db, tenant, items[0], []string{"profile-engineer-a"})
	assertStoredPersonnel(t, db, tenant, items[1], []string{"profile-engineer-b"})

	err := service.RevokeImplementationPlan(ctx, principal, items[0], domain.PhaseRevokeInput{
		Reason: "调整本服务项实施人员", ExpectedVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	publish(items[0], "engineer-a2", 3)
	assertStoredPersonnel(t, db, tenant, items[0], []string{"profile-engineer-a2"})
	assertStoredPersonnel(t, db, tenant, items[1], []string{"profile-engineer-b"})
}

// 用途：在隔离库中建立一项项目、两项服务及其不同的已分配人员。
// 参数：t 为测试上下文，db 为隔离库连接。
// 返回值：生成的租户与两个服务项编号，测试结束后只清理该租户。
func seedIndependentPersonnelItems(t *testing.T, db *gorm.DB) (string, [2]string) {
	t.Helper()
	tenant, project := ulid.Make().String(), ulid.Make().String()
	items := [2]string{ulid.Make().String(), ulid.Make().String()}
	t.Cleanup(func() {
		for _, table := range []string{"pm_notification_outbox", "pm_delivery_effects_outbox", "pm_delivery_event", "pm_impl_plan", "pm_service_item", "pm_project", "pm_capability"} {
			if err := db.Exec("DELETE FROM "+table+" WHERE tenant_id=?", tenant).Error; err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	if err := db.Create(&projectRecord{ID: project, TenantID: tenant, OwnerOrgID: "org", Name: "人员清单隔离", Contract: project, Status: "待分配", SupplementStatus: "NONE", Services: 2, Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for index, engineers := range [][]string{{"engineer-a", "engineer-a2"}, {"engineer-b"}} {
		item := serviceItemRecord{ID: items[index], TenantID: tenant, ProjectID: project,
			SourceServiceID: items[index], Status: "待分配", TestMode: "STANDARD", ReportStatus: "NONE",
			ConflictStatus: "PASSED", TeamLeadID: "lead", ProjectManagerID: "manager",
			EngineerIDs: jsonValue(engineers), Version: 1,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		for _, user := range engineers {
			capability := capabilityRecord{ID: ulid.Make().String(), TenantID: tenant, ResourceType: "PERSON", ResourceID: "profile-" + user, ResourceName: user, UserID: user, Status: "ACTIVE", CapabilityCodes: jsonValue([]string{})}
			if err := db.Create(&capability).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	return tenant, items
}

// 用途：读取指定服务项的活动计划并核对实际保存的人员编号。
// 参数：t 为测试上下文，db 为隔离库，tenant 和 item 定位计划，want 为预期名单。
// 返回值：无；清单缺失或不匹配时结束测试。
func assertStoredPersonnel(t *testing.T, db *gorm.DB, tenant, item string, want []string) {
	t.Helper()
	var record implPlanRecord
	if err := db.Where("tenant_id=? AND service_item_id=?", tenant, item).Take(&record).Error; err != nil {
		t.Fatal(err)
	}
	var rows []domain.PlanResource
	if err := json.Unmarshal(record.Personnel, &rows); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.ResourceID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("服务项 %s 人员 = %v，预期 %v", item, got, want)
	}
}
