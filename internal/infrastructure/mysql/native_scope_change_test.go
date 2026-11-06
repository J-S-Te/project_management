package mysql

import (
	"context"
	"errors"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"testing"
)

// scopeFixture 创建独立本地范围审批事务夹具。
// 参数：t 为测试上下文。
// 返回值：真实仓储、首版项目和用于替换的范围。
func scopeFixture(t *testing.T) (*Repository, domain.Project, []domain.ServiceItem) {
	t.Helper()
	repo, filter, project, items := nativeFixture(t)
	if err := repo.db.AutoMigrate(&nativeScopeChangeRecord{}, &deliveryEventRecord{}, &reportRevisionRecord{}, &evidenceFileRecord{}, &detectionCategoryRecord{}, &siteRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateNativeProject(context.Background(), filter, project, items, "scope-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Create(&detectionCategoryRecord{TenantID: project.TenantID, Category: items[0].Category, Enabled: true, RequiredCodes: "QUAL-1"}).Error; err != nil {
		t.Fatal(err)
	}
	items[0].Requirement = "新版委托要求"
	return repo, project, items
}

// TestNativeScopeApprovalIsAtomicAndTwoPerson 验证提案冻结、异人审批、版本和幂等审计。
// 参数：t 为测试上下文。
// 返回值：无；状态或事务不符合业务规则时失败。
func TestNativeScopeApprovalIsAtomicAndTwoPerson(t *testing.T) {
	repo, p, items := scopeFixture(t)
	ctx := context.Background()
	filter := nativeScopeFilter(p)
	in := domain.NativeScopeInput{OperationID: "request-1", ExpectedProjectVersion: 1, BaseScopeVersion: 1, Reason: "范围补充"}
	change, err := repo.RequestNativeScope(ctx, filter, p.ID, "business", in, items)
	if err != nil {
		t.Fatal(err)
	}
	if change.Status != "PENDING" {
		t.Fatalf("change=%+v", change)
	}
	if _, err = repo.ConfirmServiceItems(ctx, filter, []string{"SI-NATIVE-1"}, "business"); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("pending confirmation=%v", err)
	}
	decision := domain.NativeScopeDecision{OperationID: "approve-1", ExpectedVersion: 1, Decision: "APPROVE", Comment: "核验通过"}
	if _, err = repo.DecideNativeScope(ctx, filter, p.ID, change.ID, "business", decision); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("self approval=%v", err)
	}
	applied, err := repo.DecideNativeScope(ctx, filter, p.ID, change.ID, "director", decision)
	if err != nil {
		t.Fatal(err)
	}
	if applied.AppliedScopeVersion != 2 || applied.Status != "APPROVED" {
		t.Fatalf("applied=%+v", applied)
	}
	if _, err = repo.DecideNativeScope(ctx, filter, p.ID, change.ID, "director", decision); err != nil {
		t.Fatal(err)
	}
	var count int64
	repo.db.Model(&nativeScopeRecord{}).Count(&count)
	if count != 2 {
		t.Fatalf("scope versions=%d", count)
	}
	state, err := repo.ReadNativeScope(ctx, filter, p.ID)
	if err != nil || state.ProjectVersion != 2 || state.ScopeVersion != 2 || state.ServiceItems[0].Requirement != "新版委托要求" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err = repo.ConfirmServiceItems(ctx, filter, []string{applied.ServiceItems[0].ID}, "business"); err != nil {
		t.Fatal(err)
	}
}

// TestNativeScopeRejectDriftAndRollback 验证目录漂移和审计故障不会部分生效。
// 参数：t 为测试上下文。
// 返回值：无；失败留下新范围时失败。
func TestNativeScopeRejectDriftAndRollback(t *testing.T) {
	repo, p, items := scopeFixture(t)
	ctx := context.Background()
	filter := nativeScopeFilter(p)
	input := domain.NativeScopeInput{OperationID: "request", ExpectedProjectVersion: 1, BaseScopeVersion: 1, Reason: "修订"}
	change, err := repo.RequestNativeScope(ctx, filter, p.ID, "business", input, items)
	if err != nil {
		t.Fatal(err)
	}
	decision := domain.NativeScopeDecision{OperationID: "approve", ExpectedVersion: 1, Decision: "APPROVE", Comment: "通过"}
	repo.db.Model(&detectionCategoryRecord{}).Where("tenant_id=?", p.TenantID).Update("required_codes", "CHANGED")
	if _, err = repo.DecideNativeScope(ctx, filter, p.ID, change.ID, "director", decision); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("catalog drift=%v", err)
	}
	repo.db.Model(&detectionCategoryRecord{}).Where("tenant_id=?", p.TenantID).Update("required_codes", "QUAL-1")
	if err = repo.db.Callback().Create().Before("gorm:create").Register("scope_audit_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "prj_audit_outbox" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.DecideNativeScope(ctx, filter, p.ID, change.ID, "director", decision); err == nil {
		t.Fatal("audit failure ignored")
	}
	state, err := repo.ReadNativeScope(ctx, filter, p.ID)
	if err != nil || state.ScopeVersion != 1 || state.Changes[0].Status != "PENDING" {
		t.Fatalf("partial commit state=%+v err=%v", state, err)
	}
}

// nativeScopeFilter 返回测试项目的租户全范围授权。
// 参数：p 为合成项目。
// 返回值：仅该测试租户的范围。
func nativeScopeFilter(p domain.Project) platform.ScopeFilter {
	return platform.ScopeFilter{TenantID: p.TenantID, AllowAll: true}
}

// TestNativeScopeRejectWithdrawScopeAndPhase 验证拒绝/撤回不改版本并保留来源和履约边界。
// 参数：t 为测试上下文。
// 返回值：无；越权、非法来源、阶段或旧范围事件被放行时失败。
func TestNativeScopeRejectWithdrawScopeAndPhase(t *testing.T) {
	repo, p, items := scopeFixture(t)
	ctx := context.Background()
	filter := nativeScopeFilter(p)
	input := domain.NativeScopeInput{OperationID: "request", ExpectedProjectVersion: 1, BaseScopeVersion: 1, Reason: "修订"}
	foreign := filter
	foreign.TenantID = "another-tenant"
	if _, err := repo.RequestNativeScope(ctx, foreign, p.ID, "business", input, items); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("tenant=%v", err)
	}
	repo.db.Model(&projectRecord{}).Where("id=?", p.ID).Update("source_kind", "CONTRACT")
	if _, err := repo.RequestNativeScope(ctx, filter, p.ID, "business", input, items); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("contract source=%v", err)
	}
	repo.db.Model(&projectRecord{}).Where("id=?", p.ID).Update("source_kind", "NATIVE")
	repo.db.Model(&serviceItemRecord{}).Where("project_id=?", p.ID).Update("status", "现场实施完成")
	if _, err := repo.RequestNativeScope(ctx, filter, p.ID, "business", input, items); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("started phase=%v", err)
	}
	repo.db.Model(&serviceItemRecord{}).Where("project_id=?", p.ID).Update("status", "待确认")
	change, err := repo.RequestNativeScope(ctx, filter, p.ID, "business", input, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RequestNativeScope(ctx, filter, p.ID, "business", domain.NativeScopeInput{OperationID: "other", ExpectedProjectVersion: 1, BaseScopeVersion: 1, Reason: "第二个待审批"}, items); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("parallel pending=%v", err)
	}
	decision := domain.NativeScopeDecision{OperationID: "withdraw", ExpectedVersion: 1, Decision: "WITHDRAW", Comment: "撤回修订"}
	if _, err = repo.DecideNativeScope(ctx, filter, p.ID, change.ID, "other-business", decision); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("foreign withdraw=%v", err)
	}
	result, err := repo.DecideNativeScope(ctx, filter, p.ID, change.ID, "business", decision)
	if err != nil || result.Status != "WITHDRAWN" {
		t.Fatalf("withdraw=%+v %v", result, err)
	}
	state, err := repo.ReadNativeScope(ctx, filter, p.ID)
	if err != nil || state.ScopeVersion != 1 {
		t.Fatalf("withdraw mutated range=%+v %v", state, err)
	}
	input.OperationID = "request-2"
	change, err = repo.RequestNativeScope(ctx, filter, p.ID, "business", input, items)
	if err != nil {
		t.Fatal(err)
	}
	decision = domain.NativeScopeDecision{OperationID: "reject", ExpectedVersion: 1, Decision: "REJECT", Comment: "范围依据需补齐"}
	result, err = repo.DecideNativeScope(ctx, filter, p.ID, change.ID, "director", decision)
	if err != nil || result.Status != "REJECTED" {
		t.Fatalf("reject=%+v %v", result, err)
	}
	if err = repo.db.Transaction(func(tx *gorm.DB) error {
		var project projectRecord
		tx.Where("id=?", p.ID).Take(&project)
		return applyProjectEvent(tx, &project, domain.DeliveryEvent{Type: application.EventDecompositionAdjusted})
	}); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("legacy range bypass=%v", err)
	}
}

// TestNativeScopeConfirmationRejectsFullFieldDrift 验证原生确认不能绕过版本化完整范围。
// 参数：t 为测试上下文。
// 返回值：无；技术要求漂移未拒绝或凭旧范围进入履约时失败。
func TestNativeScopeConfirmationRejectsFullFieldDrift(t *testing.T) {
	repo, p, _ := scopeFixture(t)
	filter := nativeScopeFilter(p)
	repo.db.Model(&serviceItemRecord{}).Where("project_id=?", p.ID).Update("requirement", "直接篡改技术要求")
	if _, err := repo.ConfirmServiceItems(context.Background(), filter, []string{"SI-NATIVE-1"}, "business"); !errors.Is(err, application.ErrPrecondition) {
		t.Fatalf("scope drift=%v", err)
	}
}

// TestNativeScopeReplaySurvivesCatalogDisable 验证已提交提案精确重放不因目录后续停用而丢失真实回执。
// 参数：t 为测试上下文。
// 返回值：无；同号换载荷仍拒绝且目录停用不制造第二提案。
func TestNativeScopeReplaySurvivesCatalogDisable(t *testing.T) {
	repo, p, items := scopeFixture(t)
	ctx := context.Background()
	actor := platform.Principal{TenantID: p.TenantID, IdentityID: "business", UserID: "business", Roles: []string{"business_admin"}, Permissions: map[string]bool{"project.decomposition.manage": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	svc := &application.Service{Repo: repo, NativeEnabled: true}
	input := domain.NativeScopeInput{OperationID: "stable-request", ExpectedProjectVersion: 1, BaseScopeVersion: 1, Reason: "新版范围", ServiceItems: []domain.ContractService{{SourceID: items[0].SourceServiceID, Site: items[0].Site, Category: items[0].Category, Requirement: "新要求"}}}
	first, err := svc.RequestNativeScope(ctx, actor, p.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	repo.db.Model(&detectionCategoryRecord{}).Where("tenant_id=?", p.TenantID).Update("enabled", false)
	replay, err := svc.RequestNativeScope(ctx, actor, p.ID, input)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("lost exact receipt=%+v err=%v", replay, err)
	}
	input.Reason = "另一个载荷"
	if _, err = svc.RequestNativeScope(ctx, actor, p.ID, input); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("changed payload=%v", err)
	}
}

// TestNativeScopeReplayAfterApproval 验证原提案提交重放返回已生效真实版本而不重新建单。
// 参数：t 为测试上下文。
// 返回值：无；丢失批准状态或再次产生范围时失败。
func TestNativeScopeReplayAfterApproval(t *testing.T) {
	repo, p, items := scopeFixture(t)
	ctx := context.Background()
	filter := nativeScopeFilter(p)
	actor := platform.Principal{TenantID: p.TenantID, IdentityID: "business", UserID: "business", Roles: []string{"business_admin"}, Permissions: map[string]bool{"project.decomposition.manage": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	svc := &application.Service{Repo: repo, NativeEnabled: true}
	input := domain.NativeScopeInput{OperationID: "stable-after-approval", ExpectedProjectVersion: 1, BaseScopeVersion: 1, Reason: "范围修订", ServiceItems: []domain.ContractService{{SourceID: items[0].SourceServiceID, Site: items[0].Site, Category: items[0].Category, Requirement: "新要求"}, {Site: "新增场所", Category: items[0].Category}}}
	first, err := svc.RequestNativeScope(ctx, actor, p.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	final, err := repo.DecideNativeScope(ctx, filter, p.ID, first.ID, "director", domain.NativeScopeDecision{OperationID: "approve", ExpectedVersion: 1, Decision: "APPROVE", Comment: "通过"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.RequestNativeScope(ctx, actor, p.ID, input)
	if err != nil || replay.ID != first.ID || replay.Status != "APPROVED" || replay.AppliedScopeVersion != 2 || replay.ServiceItems[1].SourceServiceID != final.ServiceItems[1].SourceServiceID {
		t.Fatalf("final replay=%+v err=%v", replay, err)
	}
}
