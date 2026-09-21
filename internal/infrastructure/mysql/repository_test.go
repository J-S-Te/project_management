package mysql

import (
	"errors"
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestTranslateRuleWriteErrorMapsConcurrentDuplicateRules(t *testing.T) {
	duplicate := &drivermysql.MySQLError{Number: 1062, Message: "duplicate entry"}
	for _, kind := range []string{"automations", "permissions", "sla"} {
		if err := translateRuleWriteError(kind, duplicate); !errors.Is(err, application.ErrResourceConflict) {
			t.Fatalf("kind=%s error=%v, want resource conflict", kind, err)
		}
	}
	if err := translateRuleWriteError("capability-codes", duplicate); !errors.Is(err, application.ErrValidation) {
		t.Fatalf("capability duplicate error=%v, want validation", err)
	}
	original := errors.New("database unavailable")
	if err := translateRuleWriteError("sla", original); err != original {
		t.Fatalf("non-duplicate error was rewritten: %v", err)
	}
}

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

func TestEmbeddedPenetrationParentLifecycleGates(t *testing.T) {
	fieldCases := []struct {
		name string
		row  *penetrationWorkPackageRecord
		pass bool
	}{
		{name: "missing package blocks", pass: false},
		{name: "pending decision blocks", row: &penetrationWorkPackageRecord{DecisionStatus: "PENDING", ExecutionStatus: "NOT_STARTED"}, pass: false},
		{name: "not required passes", row: &penetrationWorkPackageRecord{DecisionStatus: "NOT_REQUIRED", ExecutionStatus: "CANCELLED"}, pass: true},
		{name: "required incomplete blocks", row: &penetrationWorkPackageRecord{DecisionStatus: "REQUIRED", ExecutionStatus: "IN_PROGRESS"}, pass: false},
		{name: "required complete passes", row: &penetrationWorkPackageRecord{DecisionStatus: "REQUIRED", ExecutionStatus: "COMPLETED"}, pass: true},
	}
	for _, test := range fieldCases {
		t.Run(test.name, func(t *testing.T) {
			if passed := penetrationFieldCompletionGate(test.row) == nil; passed != test.pass {
				t.Fatalf("field gate passed=%v, want %v", passed, test.pass)
			}
		})
	}

	if err := penetrationReportGate(&penetrationWorkPackageRecord{DecisionStatus: "REQUIRED", ReportStatus: "APPROVED"}, "ISSUED"); err == nil {
		t.Fatal("parent report issue must wait for embedded report issue")
	}
	if err := penetrationReportGate(&penetrationWorkPackageRecord{DecisionStatus: "REQUIRED", ReportStatus: "ISSUED"}, "ISSUED"); err != nil {
		t.Fatalf("issued embedded report should allow parent issue: %v", err)
	}
	if err := penetrationReportGate(&penetrationWorkPackageRecord{DecisionStatus: "REQUIRED", ReportStatus: "ISSUED"}, "ARCHIVED"); err == nil {
		t.Fatal("parent archive must wait for embedded report archive")
	}
	if err := penetrationReportGate(&penetrationWorkPackageRecord{DecisionStatus: "REQUIRED", ReportStatus: "ARCHIVED"}, "ARCHIVED"); err != nil {
		t.Fatalf("archived embedded report should allow parent archive: %v", err)
	}
}

func TestCountMatchedContractReferencesSupportsStableAndLegacyProjects(t *testing.T) {
	references := []platform.ApprovedContract{
		{ID: "C-1", Number: "HT-1", Version: 4},
		{ID: "C-1", Number: "HT-1", Version: 5},
		{ID: "C-2", Number: "HT-2", Version: 4},
		{ID: "C-3", Number: "HT-3", Version: 4},
		{ID: "C-4", Number: "HT-4", Version: 4},
		{ID: "C-5", Number: "HT-5", Version: 4},
	}
	records := []projectRecord{
		{ID: "PJ-current", ContractID: "C-1", Contract: "HT-1", ContractVersion: "4"},
		// 生产历史记录把合同编号写入 contract_id；必须通过合同号和版本兼容识别。
		{ID: "PJ-legacy", ContractID: "HT-2", Contract: "HT-2", ContractVersion: "4"},
		{ID: "PJ-old-version", ContractID: "HT-3", Contract: "HT-3", ContractVersion: "3"},
		{ID: "PJ-unrelated", ContractID: "E2E-1", Contract: "E2E-1"},
	}

	if got := countMatchedContractReferences(records, references); got != 2 {
		t.Fatalf("matched references=%d, want 2", got)
	}
	matched := matchedContractReferenceKeys(records, references)
	if !matched[approvedContractReferenceKey(references[0])] || matched[approvedContractReferenceKey(references[1])] || !matched[approvedContractReferenceKey(references[2])] || matched[approvedContractReferenceKey(references[3])] || matched[approvedContractReferenceKey(references[4])] || matched[approvedContractReferenceKey(references[5])] {
		t.Fatalf("matched reference keys=%v, want only C-1 v4 and C-2 v4", matched)
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
