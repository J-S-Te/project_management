package mysql

import (
	"context"
	"os"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestMonitoringPageUsesDatabasePagination 验证详情聚合仅限选中数据库页，同时总数及分面仍覆盖完整
// 筛选结果集。
// 参数：t 为测试上下文。
// 返回值：无；实际行为偏离断言时用例失败。
func TestMonitoringPageUsesDatabasePagination(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run monitoring pagination integration checks")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	const tenant = "PM-MONITORING-PAGE-TENANT"
	ctx := context.Background()
	cleanup := func() {
		db.WithContext(ctx).Exec("DELETE FROM pm_delivery_event WHERE tenant_id = ?", tenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_service_item WHERE tenant_id = ?", tenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_project WHERE tenant_id = ?", tenant)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_project
		(id, tenant_id, name, customer, contract, contract_version, supplement_status, services, team, status, created_at, updated_at) VALUES
		('PJ-MON-1', ?, '项目一', '客户甲', 'C-MON-1', 'v1', 'NONE', 1, 'A组', '待分配', UTC_TIMESTAMP(3), '2026-09-20 01:00:00.000'),
		('PJ-MON-2', ?, '项目二', '客户乙', 'C-MON-2', 'v1', 'NONE', 1, 'B组', '待分配', UTC_TIMESTAMP(3), '2026-09-20 02:00:00.000'),
		('PJ-MON-3', ?, '项目三', '客户丙', 'C-MON-3', 'v1', 'NONE', 1, 'C组', '待分配', UTC_TIMESTAMP(3), '2026-09-20 03:00:00.000')`, tenant, tenant, tenant).Error; err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_service_item
		(id, tenant_id, project_id, source_service_id, category, requirement, test_mode, status, report_status, conflict_status, created_at, updated_at) VALUES
		('SI-MON-1', ?, 'PJ-MON-1', 'S1', '类别一', 'r', 'STANDARD', '待分配', 'NONE', 'PASSED', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3)),
		('SI-MON-2', ?, 'PJ-MON-2', 'S2', '类别二', 'r', 'STANDARD', '待分配', 'NONE', 'PASSED', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3)),
		('SI-MON-3', ?, 'PJ-MON-3', 'S3', '类别三', 'r', 'STANDARD', '待分配', 'NONE', 'PASSED', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))`, tenant, tenant, tenant).Error; err != nil {
		t.Fatalf("seed service items: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_delivery_event
		(id, tenant_id, project_id, service_item_id, event_type, actor_user_id, payload, created_at) VALUES
		('EV-MON-1', ?, 'PJ-MON-1', 'SI-MON-1', 'TEAM_ASSIGNED', 'seed', JSON_OBJECT(), UTC_TIMESTAMP(3)),
		('EV-MON-2', ?, 'PJ-MON-2', 'SI-MON-2', 'TEAM_ASSIGNED', 'seed', JSON_OBJECT(), UTC_TIMESTAMP(3)),
		('EV-MON-3', ?, 'PJ-MON-3', 'SI-MON-3', 'TEAM_ASSIGNED', 'seed', JSON_OBJECT(), UTC_TIMESTAMP(3))`, tenant, tenant, tenant).Error; err != nil {
		t.Fatalf("seed events: %v", err)
	}

	repo := NewRepository(db)
	page, err := repo.LoadProjectMonitoringPage(ctx, platform.ScopeFilter{TenantID: tenant, AllowAll: true}, domain.ProjectMonitoringQuery{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("load monitoring page: %v", err)
	}
	if page.Total != 3 || len(page.Projects) != 2 {
		t.Fatalf("pagination = total %d, projects %d; want 3 and 2", page.Total, len(page.Projects))
	}
	if page.Projects[0].ID != "PJ-MON-3" || page.Projects[1].ID != "PJ-MON-2" {
		t.Fatalf("page order = [%s %s], want newest projects 3 and 2", page.Projects[0].ID, page.Projects[1].ID)
	}
	for _, item := range page.ServiceItems {
		if item.ProjectID == "PJ-MON-1" {
			t.Fatalf("off-page service item was hydrated: %+v", item)
		}
	}
	for _, event := range page.Events {
		if event.ProjectID == "PJ-MON-1" {
			t.Fatalf("off-page event was hydrated: %+v", event)
		}
	}
	if len(page.ServiceItems) != 2 || len(page.Events) != 2 {
		t.Fatalf("current-page aggregation = %d items, %d events; want 2 and 2", len(page.ServiceItems), len(page.Events))
	}
	if len(page.Categories) != 3 || len(page.Teams) != 3 {
		t.Fatalf("full-result facets = %v categories, %v teams; want all three", page.Categories, page.Teams)
	}
}

// TestMonitoringPageHonoursFieldPermissionsOnMySQL 验证数据库分页路径在真实pm_field_permission 规
// 则下不绕过字段级脱敏：事件 payload 掩码、分面清空。
// 参数：t 为测试上下文。
// 返回值：无；实际行为偏离断言时用例失败。
func TestMonitoringPageHonoursFieldPermissionsOnMySQL(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to run monitoring field-permission integration checks")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	const tenant = "PM-MONITORING-FIELD-PERM-TENANT"
	const role = "quality_manager"
	ctx := context.Background()
	cleanup := func() {
		db.WithContext(ctx).Exec("DELETE FROM pm_field_permission WHERE tenant_id = ?", tenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_delivery_event WHERE tenant_id = ?", tenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_service_item WHERE tenant_id = ?", tenant)
		db.WithContext(ctx).Exec("DELETE FROM pm_project WHERE tenant_id = ?", tenant)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_project
		(id, tenant_id, name, customer, contract, contract_version, supplement_status, services, team, status, created_at, updated_at) VALUES
		('PJ-MASK-1', ?, '脱敏项目', '客户甲', 'C-MASK-1', 'v1', 'NONE', 1, 'A组', '待分配', UTC_TIMESTAMP(3), '2026-09-20 01:00:00.000')`, tenant).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_service_item
		(id, tenant_id, project_id, source_service_id, category, requirement, test_mode, status, report_status, conflict_status, project_manager_id, created_at, updated_at) VALUES
		('SI-MASK-1', ?, 'PJ-MASK-1', 'S1', '类别一', 'r', 'STANDARD', '待分配', 'NONE', 'PASSED', 'pm-1', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))`, tenant).Error; err != nil {
		t.Fatalf("seed service item: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_delivery_event
		(id, tenant_id, project_id, service_item_id, event_type, actor_user_id, payload, created_at) VALUES
		('EV-MASK-1', ?, 'PJ-MASK-1', 'SI-MASK-1', 'TEAM_ASSIGNED', 'seed', JSON_OBJECT('team_lead_id', 'TL-9', 'note', '保留'), UTC_TIMESTAMP(3))`, tenant).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}
	for _, field := range []string{"team", "category", "project_manager_id", "team_lead_id"} {
		if err := db.WithContext(ctx).Create(&fieldPermissionRecord{
			TenantID: tenant, Kind: "permissions", Name: "隐藏 " + field,
			RoleCode: role, FieldName: field, AccessLevel: "hidden", Enabled: true,
		}).Error; err != nil {
			t.Fatalf("seed field permission %s: %v", field, err)
		}
	}

	service := &application.Service{Repo: NewRepository(db)}
	principal := platform.Principal{
		TenantID: tenant, UserID: "auditor", IdentityID: "auditor",
		Roles:       []string{role},
		Permissions: map[string]bool{"project.read": true},
		DataScopes:  []platform.DataScope{{RoleCode: role, ScopeType: "TENANT", ScopeID: tenant}},
	}
	snapshot, err := service.MonitorProjects(ctx, principal, domain.ProjectMonitoringQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("monitor projects: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("monitoring items = %d, want 1", len(snapshot.Items))
	}
	if snapshot.Items[0].Project.Team != "***" {
		t.Fatalf("project team must be masked, got %q", snapshot.Items[0].Project.Team)
	}
	if len(snapshot.Teams) != 0 || len(snapshot.Categories) != 0 || len(snapshot.ProjectManagerIDs) != 0 {
		t.Fatalf("facets must be empty under hidden rules: teams=%v categories=%v managers=%v", snapshot.Teams, snapshot.Categories, snapshot.ProjectManagerIDs)
	}
	if len(snapshot.RecentEvents) != 1 {
		t.Fatalf("recent events = %d, want 1", len(snapshot.RecentEvents))
	}
	if got := snapshot.RecentEvents[0].Payload["team_lead_id"]; got != "***" {
		t.Fatalf("recent event payload must be masked, got %v", got)
	}
	if got := snapshot.RecentEvents[0].Payload["note"]; got != "保留" {
		t.Fatalf("non-hidden payload keys must survive, got %v", got)
	}
	if snapshot.Items[0].LatestEvent == nil || snapshot.Items[0].LatestEvent.Payload["team_lead_id"] != "***" {
		t.Fatalf("latest event must be masked too: %+v", snapshot.Items[0].LatestEvent)
	}
}
