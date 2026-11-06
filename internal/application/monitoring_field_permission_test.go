package application

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// stubMonitoringPageRepository 固定返回一个数据库页投影，用于直接验证
// monitorProjectsFromDatabasePage 的脱敏顺序与分面处理，不依赖 MySQL。
type stubMonitoringPageRepository struct {
	data domain.ProjectMonitoringPageData
	err  error
}

func (r stubMonitoringPageRepository) LoadProjectMonitoringPage(context.Context, platform.ScopeFilter, domain.ProjectMonitoringQuery) (domain.ProjectMonitoringPageData, error) {
	return r.data, r.err
}

func monitoringFieldPermissionPrincipal() platform.Principal {
	return platform.Principal{
		TenantID: "tenant-1", UserID: "user-1", IdentityID: "user-1",
		Roles:       []string{"engineer"},
		Permissions: map[string]bool{"project.read": true},
		DataScopes:  []platform.DataScope{{RoleCode: "engineer", ScopeType: "APPLICATION"}},
	}
}

func monitoringFieldPermissionPage() domain.ProjectMonitoringPageData {
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	return domain.ProjectMonitoringPageData{
		Projects: []domain.Project{
			{ID: "PJ-1", Name: "项目一", Customer: "客户甲", Services: 1, Status: "实施中", Team: "A组", UpdatedAt: now},
		},
		ServiceItems: []domain.ServiceItem{
			{ID: "SI-1", ProjectID: "PJ-1", Category: "等保测评", ProjectManagerID: "pm-1"},
		},
		Events: []domain.DeliveryEvent{
			{
				ID: "EV-2", ProjectID: "PJ-1", ServiceItemID: "SI-1", Type: EventTeamAssigned, CreatedAt: now,
				Payload: map[string]any{"team_lead_id": "TL-9", "note": "保留"},
			},
			{
				ID: "EV-1", ProjectID: "PJ-1", ServiceItemID: "SI-1", Type: EventDecompositionAdjusted, CreatedAt: now.Add(-time.Minute),
				Payload: map[string]any{
					"reason": "补充协议",
					"service_items": []any{
						map[string]any{"category": "等保测评", "site": "杭州机房"},
					},
				},
			},
		},
		StatusCounts:      map[string]int{"实施中": 1},
		Categories:        []string{"等保测评"},
		Teams:             []string{"A组"},
		ProjectManagerIDs: []string{"pm-1"},
		Total:             1,
		LatestUpdatedAt:   now,
	}
}

// 数据库分页路径必须与内存兜底路径同口径：latest_event / recent_events
// 的 payload 掩码，分面在字段隐藏时清空（安全审计 M-8）。
func TestMonitoringDatabasePageHonoursFieldPermissions(t *testing.T) {
	rules := []domain.Rule{
		{Kind: "permissions", Enabled: true, RoleCode: "engineer", FieldName: "team", AccessLevel: "hidden"},
		{Kind: "permissions", Enabled: true, RoleCode: "engineer", FieldName: "category", AccessLevel: "hidden"},
		{Kind: "permissions", Enabled: true, RoleCode: "engineer", FieldName: "project_manager_id", AccessLevel: "hidden"},
		{Kind: "permissions", Enabled: true, RoleCode: "engineer", FieldName: "team_lead_id", AccessLevel: "hidden"},
		{Kind: "permissions", Enabled: true, RoleCode: "engineer", FieldName: "site", AccessLevel: "hidden"},
	}
	service := Service{Repo: &hookRepository{rules: rules}}
	snapshot, err := service.monitorProjectsFromDatabasePage(context.Background(), monitoringFieldPermissionPrincipal(), stubMonitoringPageRepository{data: monitoringFieldPermissionPage()}, domain.ProjectMonitoringQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("monitorProjectsFromDatabasePage failed: %v", err)
	}

	if len(snapshot.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(snapshot.Items))
	}
	row := snapshot.Items[0]
	if row.Project.Team != maskedFieldValue {
		t.Fatalf("project team must be masked, got %q", row.Project.Team)
	}
	if row.LatestEvent == nil || row.LatestEvent.ID != "EV-2" {
		t.Fatalf("latest_event = %+v, want EV-2", row.LatestEvent)
	}
	if got := row.LatestEvent.Payload["team_lead_id"]; got != maskedFieldValue {
		t.Fatalf("latest_event payload must be masked before selection, got %v", got)
	}
	if got := row.LatestEvent.Payload["note"]; got != "保留" {
		t.Fatalf("non-hidden payload keys must survive, got %v", got)
	}

	if len(snapshot.RecentEvents) != 2 {
		t.Fatalf("recent_events = %d, want 2", len(snapshot.RecentEvents))
	}
	if got := snapshot.RecentEvents[0].Payload["team_lead_id"]; got != maskedFieldValue {
		t.Fatalf("recent_events payload must be masked, got %v", got)
	}
	if got := snapshot.RecentEvents[0].Payload["note"]; got != "保留" {
		t.Fatalf("recent_events must keep non-hidden keys, got %v", got)
	}
	snapshotItems, ok := snapshot.RecentEvents[1].Payload["service_items"].([]any)
	if !ok || len(snapshotItems) != 1 {
		t.Fatalf("nested service_items snapshot lost: %+v", snapshot.RecentEvents[1].Payload)
	}
	entry, _ := snapshotItems[0].(map[string]any)
	if entry["category"] != maskedFieldValue || entry["site"] != maskedFieldValue {
		t.Fatalf("nested snapshot must be masked too, got %+v", entry)
	}
	if snapshot.RecentEvents[1].Payload["reason"] != "补充协议" {
		t.Fatalf("event metadata must survive, got %+v", snapshot.RecentEvents[1].Payload)
	}

	for name, facet := range map[string][]string{
		"teams": snapshot.Teams, "categories": snapshot.Categories, "project_manager_ids": snapshot.ProjectManagerIDs,
	} {
		if facet == nil {
			t.Fatalf("%s must be an empty slice, not nil", name)
		}
		if len(facet) != 0 {
			t.Fatalf("%s must be empty when the field is hidden, got %v", name, facet)
		}
	}
}

// 未配置 hidden 规则时分页投影逐字段直通：脱敏不能改变正常响应。
func TestMonitoringDatabasePagePassThroughWithoutHiddenRules(t *testing.T) {
	page := monitoringFieldPermissionPage()
	service := Service{Repo: &hookRepository{}}
	snapshot, err := service.monitorProjectsFromDatabasePage(context.Background(), monitoringFieldPermissionPrincipal(), stubMonitoringPageRepository{data: page}, domain.ProjectMonitoringQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("monitorProjectsFromDatabasePage failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(snapshot.Items))
	}
	if !reflect.DeepEqual(snapshot.Items[0].Project, page.Projects[0]) {
		t.Fatalf("project changed without rules: %+v", snapshot.Items[0].Project)
	}
	if snapshot.Items[0].LatestEvent == nil || !reflect.DeepEqual(snapshot.Items[0].LatestEvent.Payload, page.Events[0].Payload) {
		t.Fatalf("latest_event changed without rules: %+v", snapshot.Items[0].LatestEvent)
	}
	if !reflect.DeepEqual(snapshot.RecentEvents, page.Events) {
		t.Fatalf("recent_events changed without rules: %+v", snapshot.RecentEvents)
	}
	if !reflect.DeepEqual(snapshot.Teams, page.Teams) || !reflect.DeepEqual(snapshot.Categories, page.Categories) || !reflect.DeepEqual(snapshot.ProjectManagerIDs, page.ProjectManagerIDs) {
		t.Fatalf("facets changed without rules: teams=%v categories=%v managers=%v", snapshot.Teams, snapshot.Categories, snapshot.ProjectManagerIDs)
	}
	if snapshot.Total != page.Total || snapshot.Page != 1 || snapshot.PageSize != 20 {
		t.Fatalf("pagination projection = total %d page %d size %d", snapshot.Total, snapshot.Page, snapshot.PageSize)
	}
}
