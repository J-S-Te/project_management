package mysql

import (
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestCanStartPreparationAllowsReentryAfterFieldRollback(t *testing.T) {
	for _, status := range []string{"待实施", "实施准备中"} {
		if !canStartPreparation(status) {
			t.Fatalf("status %q should allow preparation", status)
		}
	}
	for _, status := range []string{"待分配", "实施中", "现场实施完成"} {
		if canStartPreparation(status) {
			t.Fatalf("status %q must not allow preparation", status)
		}
	}
}

func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "project:secret@tcp(127.0.0.1:3306)/project_management", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func scopedProjectSQL(t *testing.T, filter platform.ScopeFilter) string {
	t.Helper()
	var records []projectRecord
	statement := applyProjectScope(dryRunDB(t).Model(&projectRecord{}), filter, "pm_project").Find(&records).Statement
	return statement.SQL.String()
}

func TestApplicationAndEnvironmentScopeSQLAllowsTenantRows(t *testing.T) {
	for _, filter := range []platform.ScopeFilter{{TenantID: "tenant-1", AllowAll: true}, {TenantID: "tenant-1", AllowAll: true, IdentityID: "identity-1"}} {
		sql := scopedProjectSQL(t, filter)
		if !strings.Contains(sql, "pm_project.tenant_id = ?") || strings.Contains(sql, "owner_org_id") || strings.Contains(sql, "owner_identity_id") || strings.Contains(sql, "1 = 0") {
			t.Fatalf("unexpected allow-all SQL: %s", sql)
		}
	}
}

func TestOrganizationSelfAndProjectScopesAreAppliedInSQL(t *testing.T) {
	filter := platform.ScopeFilter{TenantID: "tenant-1", IdentityID: "identity-1", OrganizationIDs: []string{"org-1"}, ProjectIDs: []string{"PJ-1"}, AllowSelf: true}
	sql := scopedProjectSQL(t, filter)
	for _, expected := range []string{"pm_project.tenant_id = ?", "pm_project.owner_org_id IN (?)", "pm_project.id IN (?)", "pm_project.owner_identity_id = ?", "pm_service_item"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("scope SQL missing %q: %s", expected, sql)
		}
	}
}

func TestEmptyBusinessScopeFailsClosedInSQL(t *testing.T) {
	sql := scopedProjectSQL(t, platform.ScopeFilter{TenantID: "tenant-1", IdentityID: "identity-1"})
	if !strings.Contains(sql, "1 = 0") {
		t.Fatalf("empty scope SQL did not fail closed: %s", sql)
	}
}

func TestAssignedItemScopeUsesPlatformUserIDAndDoesNotExposeSiblingItems(t *testing.T) {
	filter := platform.ScopeFilter{TenantID: "tenant-1", IdentityID: "identity-1", UserID: "user-1", AllowSelf: true, AssignedItemsOnly: true}
	projectSQL := scopedProjectSQL(t, filter)
	for _, expected := range []string{"EXISTS", "team_lead_id = ?", "project_manager_id = ?", "engineer_ids"} {
		if !strings.Contains(projectSQL, expected) {
			t.Fatalf("assigned project SQL missing %q: %s", expected, projectSQL)
		}
	}
	if strings.Contains(projectSQL, "owner_identity_id") {
		t.Fatalf("operational assignee scope must not inherit project ownership: %s", projectSQL)
	}
	var records []serviceItemRecord
	itemSQL := applyServiceItemScope(dryRunDB(t).Model(&serviceItemRecord{}), dryRunDB(t), filter).Find(&records).Statement.SQL.String()
	if !strings.Contains(itemSQL, "pm_service_item.team_lead_id = ?") || strings.Contains(itemSQL, "scope_project") {
		t.Fatalf("assigned service-item SQL must filter the row itself: %s", itemSQL)
	}
}

func TestAssignablePersonnelMatchesLinkedUserIDWithoutDateRestriction(t *testing.T) {
	var records []capabilityRecord
	statement := activePersonnelCapabilitiesQuery(dryRunDB(t), "tenant-1", "2026-09-15T00:00:00Z", []string{"user-1"}).Find(&records).Statement
	sql := statement.SQL.String()
	for _, expected := range []string{"resource_type='PERSON'", "resource_id IN", "user_id IN", "identity_status="} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("qualified personnel SQL missing %q: %s", expected, sql)
		}
	}
	if strings.Contains(sql, "valid_from") || strings.Contains(sql, "valid_until") {
		t.Fatalf("personnel assignment query must not apply validity dates: %s", sql)
	}
}

// 项目状态必须由完整服务项集合派生：项目权限只决定项目是否可见，不能裁剪状态输入。
func TestProjectStatusInputQueryUsesTenantWideProjectItems(t *testing.T) {
	var rows []projectStatusRow
	statement := projectStatusInputQuery(dryRunDB(t), "tenant-1", []string{"PJ-1"}).Find(&rows).Statement
	sql := statement.SQL.String()
	for _, expected := range []string{"report_status", "tenant_id = ?", "project_id IN"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("project status input SQL missing %q: %s", expected, sql)
		}
	}
	for _, forbidden := range []string{"scope_item", "team_lead_id = ?", "project_manager_id = ?", "owner_identity_id = ?"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("project status input must not depend on the viewer's service-item scope, found %q: %s", forbidden, sql)
		}
	}
}

// 拆解调整的服务项编号由仓储层顺延既有最大序号补齐：主键为空会让多行互相冲突而整单回滚，
// 单行则落成 id=” 的孤儿行——事件分发以 service_item_id != "" 判定，该服务项之后再也走不动。
func TestServiceItemIDFollowsExistingSequence(t *testing.T) {
	if got := serviceItemIDFor("PJ-2026-ABC123", 2); got != "SI-2026-ABC123-002" {
		t.Fatalf("serviceItemIDFor = %q, want SI-2026-ABC123-002", got)
	}
	for itemID, want := range map[string]int{
		"SI-2026-ABC123-007": 7,
		"SI-2026-ABC123-001": 1,
		"":                   0,
		"garbage":            0,
	} {
		if got := serviceItemSequence(itemID); got != want {
			t.Fatalf("serviceItemSequence(%q) = %d, want %d", itemID, got, want)
		}
	}
}
