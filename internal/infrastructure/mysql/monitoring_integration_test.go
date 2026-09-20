package mysql

import (
	"context"
	"os"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestMonitoringPageUsesDatabasePagination verifies that detail aggregation is
// restricted to the selected database page while totals and facets still cover
// the complete filtered result set.
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
