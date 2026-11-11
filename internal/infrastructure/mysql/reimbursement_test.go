package mysql

import (
	"context"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

// expenseRepositoryFixture 创建与现行业务库无关的隔离SQLite事务测试。
// 参数：t 为测试上下文。
// 返回值：已创建报销台账表及项目的仓储。
func expenseRepositoryFixture(t *testing.T) *Repository {
	t.Helper()
	db := equipmentAtomicityDB(t)
	if err := db.AutoMigrate(&deliveryEventRecord{}); err != nil {
		t.Fatal(err)
	}
	for _, table := range expenseTables {
		if err := db.Table(table.name).AutoMigrate(table.model); err != nil {
			t.Fatal(err)
		}
	}
	project := projectRecord{
		ID: "p", TenantID: "t", Name: "项目", Status: domain.ProjectStatusReporting, Version: 1,
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	return NewRepository(db)
}

// expenseSpendFixture 创建测试使用的规范付款快照。
// 参数：无。
// 返回值：带有正确摘要的完整快照。
func expenseSpendFixture() domain.SpendSnapshot {
	s := domain.SpendSnapshot{SchemaVersion: 1, EventID: "e1", SourceApplication: "reimbursement", SourceEnvironment: "test", TenantID: "t", ClaimID: "c", ClaimNo: "BX-1", DocumentRevision: 1, ApprovalRound: 1, ProjectionRevision: 1, CompleteSnapshot: true, Trigger: "PAYMENT_RECORDED", Currency: "CNY", Effects: []domain.SpendEffect{{EffectID: "pay", PaymentRecordID: "pay", Kind: "PAYMENT_RECORDED", AmountMinor: "100", OccurredAt: "2026-11-10T01:00:00Z", RecordedAt: "2026-11-10T01:00:00Z", ApprovedContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Allocations: []domain.SpendAllocation{{ProjectID: "p", FeeType: "TRAVEL", AmountMinor: "100"}}}}, Contributions: []domain.SpendContribution{{ProjectID: "p", FeeType: "TRAVEL", PaidNetMinor: "100"}}}
	s.SnapshotHash, _ = domain.SpendSnapshotHash(s)
	return s
}

// TestExpenseSpendReplayConflictAndRebuild 验证提交后冻结回执、版本冲突和损坏重建。
// 参数：t 为测试上下文。
// 返回值：无；持久化或重建不满足协议时失败。
func TestExpenseSpendReplayConflictAndRebuild(t *testing.T) {
	r := expenseRepositoryFixture(t)
	ctx := context.Background()
	s := expenseSpendFixture()
	receipt, err := r.ApplyExpenseSpend(ctx, s)
	if err != nil || receipt.Status != "APPLIED" {
		t.Fatalf("%+v %v", receipt, err)
	}
	replay, err := r.ApplyExpenseSpend(ctx, s)
	if err != nil || replay != receipt {
		t.Fatalf("frozen replay=%+v %v", replay, err)
	}
	altered := s
	altered.ClaimNo = "tampered"
	altered.SnapshotHash, _ = domain.SpendSnapshotHash(altered)
	if _, err = r.ApplyExpenseSpend(ctx, altered); err == nil {
		t.Fatal("event conflict accepted")
	}
	altered.EventID = "new-event"
	if _, err = r.ApplyExpenseSpend(ctx, altered); err == nil {
		t.Fatal("revision conflict accepted")
	}
	if err = r.db.Table("pm_expense_projection").Where("tenant_id=?", "t").Delete(&expenseProjectionRow{}).Error; err != nil {
		t.Fatal(err)
	}
	checks, err := r.ReconcileExpenseSpend(ctx, "t", "test", "", 10)
	if err != nil || len(checks) != 1 || checks[0].Consistent {
		t.Fatalf("damage not found %+v %v", checks, err)
	}
	if err = r.RebuildExpenseSpend(ctx, "t", "test", "c", "operator", "repair test"); err != nil {
		t.Fatal(err)
	}
	checks, err = r.ReconcileExpenseSpend(ctx, "t", "test", "", 10)
	if err != nil || !checks[0].Consistent {
		t.Fatalf("rebuild invalid %+v %v", checks, err)
	}
}

type expenseDirectory struct{ Active bool }

// List 提供明确的当前平台目录人员状态供真实应用链测试。
// 参数：ctx/query 为查询上下文和精确主体过滤。
// 返回值：当前有效时返回唯一主体，无效时返回空目录。
func (d expenseDirectory) List(ctx context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if d.Active {
		return platform.OwnerDirectoryPage{Total: 1, Items: []platform.OwnerDirectoryUser{{UserID: query.UserID, DisplayName: "人员"}}}, nil
	}
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{}}, nil
}

// TestExpenseSearchAndBatchUseSameQualification 验证真实SQL搜索与批验的服务项边界及目录关闭。
// 参数：t 为测试上下文。
// 返回值：无；原生项目、项目范围或人员撤销处理不符时失败。
func TestExpenseSearchAndBatchUseSameQualification(t *testing.T) {
	r := expenseRepositoryFixture(t)
	ctx := context.Background()
	if err := r.db.AutoMigrate(&penetrationWorkPackageRecord{}, &auditOutboxRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Model(&projectRecord{}).Where("id=?", "p").Update("source_kind", "NATIVE").Error; err != nil {
		t.Fatal(err)
	}
	for _, s := range []serviceItemRecord{
		{ID: "a", TenantID: "t", ProjectID: "p", EngineerIDs: []byte(`["u"]`),
			Status: domain.ProjectStatusFieldCompleted, ReportStatus: "COMPILING", Version: 1},
		{ID: "b", TenantID: "t", ProjectID: "p", EngineerIDs: []byte(`[]`),
			Status: domain.ProjectStatusFieldCompleted, ReportStatus: "COMPILING", Version: 1},
	} {
		if err := r.db.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := application.Service{Repo: r, Personnel: expenseDirectory{Active: true}}
	results, err := service.SearchExpenseReferences(ctx, "t", application.ExpenseSearchInput{ActorSubjectID: "u", PageSize: 20})
	if err != nil || len(results.Items) != 1 || results.Items[0].ServiceItemID != "a" || results.Items[0].SourceKind != "NATIVE" {
		t.Fatalf("search=%+v %v", results, err)
	}
	in := application.ExpenseBatchInput{ActorSubjectID: "u", ClaimID: "c", DraftVersion: 1, References: []application.ExpenseReferenceInput{{ProjectID: "p", ServiceItemID: "a", OccurredDate: "2026-11-10"}, {ProjectID: "p", ServiceItemID: "b", OccurredDate: "2026-11-10"}, {ProjectID: "p", OccurredDate: "2026-11-10"}}}
	validated, err := service.BatchValidateExpenseReferences(ctx, "t", in)
	if err != nil || len(validated.Items) != 3 || !validated.Items[0].Eligible || validated.Items[1].Eligible || validated.Items[2].Eligible {
		t.Fatalf("batch=%+v %v", validated, err)
	}
	actor := platform.Principal{TenantID: "t", IdentityID: "manager", Permissions: map[string]bool{"project.expense_participant.manage": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	grant, err := service.ManageExpenseParticipant(ctx, actor, "p", application.ExpenseParticipantInput{SubjectID: "u", Status: "ACTIVE", ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), Reason: "参与销售"})
	if err != nil || grant.Version != 1 {
		t.Fatalf("grant=%+v %v", grant, err)
	}
	validated, err = service.BatchValidateExpenseReferences(ctx, "t", in)
	if err != nil || !validated.Items[1].Eligible || !validated.Items[2].Eligible {
		t.Fatalf("explicit=%+v %v", validated, err)
	}
	actor.DataScopes = []platform.DataScope{{ScopeType: "PROJECT", ScopeID: "other"}}
	if _, err = service.ManageExpenseParticipant(ctx, actor, "p", application.ExpenseParticipantInput{SubjectID: "u", Status: "REVOKED", Reason: "outside scope", ExpectedVersion: 1}); err == nil {
		t.Fatal("out-of-scope manager accepted")
	}
	service.Personnel = expenseDirectory{}
	if _, err = service.SearchExpenseReferences(ctx, "t", application.ExpenseSearchInput{ActorSubjectID: "u", PageSize: 20}); err == nil {
		t.Fatal("inactive actor accepted")
	}
}

// TestExpenseReversalOutOfOrderAndImmutableHistory 验证冲正先达、旧版晚达和历史效果不可丢失。
// 参数：t 为测试上下文。
// 返回值：无；金额回退、漏账或历史篡改未被拦截时失败。
func TestExpenseReversalOutOfOrderAndImmutableHistory(t *testing.T) {
	r := expenseRepositoryFixture(t)
	ctx := context.Background()
	first := expenseSpendFixture()
	second := first
	second.EventID = "e2"
	second.ProjectionRevision = 2
	second.Trigger = "PAYMENT_FULLY_REVERSED"
	reverse := first.Effects[0]
	reverse.EffectID = "rev"
	reverse.Kind = "PAYMENT_FULLY_REVERSED"
	reverse.ReversesEffectID = "pay"
	reverse.AmountMinor = "-100"
	reverse.Allocations = []domain.SpendAllocation{{ProjectID: "p", FeeType: "TRAVEL", AmountMinor: "-100"}}
	second.Effects = append(append([]domain.SpendEffect{}, first.Effects...), reverse)
	second.Contributions = []domain.SpendContribution{{ProjectID: "p", FeeType: "TRAVEL", PaidNetMinor: "0"}}
	second.SnapshotHash, _ = domain.SpendSnapshotHash(second)
	receipt, err := r.ApplyExpenseSpend(ctx, second)
	if err != nil || receipt.AppliedRevision != 2 {
		t.Fatal(receipt, err)
	}
	receipt, err = r.ApplyExpenseSpend(ctx, first)
	if err != nil || receipt.Status != "SUPERSEDED" || receipt.AppliedRevision != 2 {
		t.Fatal(receipt, err)
	}
	var actual expenseProjectionRow
	if err = r.db.Table("pm_expense_projection").Take(&actual).Error; err != nil || actual.PaidNetMinor != 0 {
		t.Fatal(actual, err)
	}
	incomplete := first
	incomplete.EventID = "e3"
	incomplete.ProjectionRevision = 3
	incomplete.SnapshotHash, _ = domain.SpendSnapshotHash(incomplete)
	if _, err = r.ApplyExpenseSpend(ctx, incomplete); err == nil {
		t.Fatal("new complete snapshot lost known reversal")
	}
	bad := first
	bad.EventID = "old-replacement"
	bad.ProjectionRevision = 0
	if _, err = r.ApplyExpenseSpend(ctx, bad); err == nil {
		t.Fatal("zero revision accepted")
	}
}

// TestExpenseSpendInvalidProjectRollsBackWholeTransaction 验证错误租户项目不能留下半份效果或回执。
// 参数：t 为测试上下文。
// 返回值：无；回滚或隔离证据缺失时测试失败。
func TestExpenseSpendInvalidProjectRollsBackWholeTransaction(t *testing.T) {
	r := expenseRepositoryFixture(t)
	s := expenseSpendFixture()
	s.Effects[0].Allocations[0].ProjectID = "foreign"
	s.Contributions[0].ProjectID = "foreign"
	s.SnapshotHash, _ = domain.SpendSnapshotHash(s)
	if _, err := r.ApplyExpenseSpend(context.Background(), s); err == nil {
		t.Fatal("unknown project accepted")
	}
	for _, table := range []string{"pm_expense_cursor", "pm_expense_inbox", "pm_expense_snapshot", "pm_expense_effect", "pm_expense_allocation", "pm_expense_projection"} {
		var count int64
		if err := r.db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s retained %d rows: %v", table, count, err)
		}
	}
	var conflicts int64
	if err := r.db.Table("pm_expense_conflict").Count(&conflicts).Error; err != nil || conflicts != 1 {
		t.Fatal("conflict evidence missing", conflicts, err)
	}
}
