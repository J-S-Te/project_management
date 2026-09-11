package mysql

import (
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

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

// 派生项目状态的输入查询必须复用服务项数据范围，否则不同角色会看到不一致或越权的派生结果。
func TestProjectStatusInputQueryAppliesServiceItemScope(t *testing.T) {
	filter := platform.ScopeFilter{TenantID: "tenant-1", IdentityID: "identity-1", AllowSelf: true}
	var rows []projectStatusRow
	statement := projectStatusInputQuery(dryRunDB(t), filter, []string{"PJ-1"}).Find(&rows).Statement
	sql := statement.SQL.String()
	for _, expected := range []string{"report_status", "pm_service_item.tenant_id = ?", "pm_service_item.project_id IN", "scope_item.team_lead_id = ?"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("project status input SQL missing %q: %s", expected, sql)
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
