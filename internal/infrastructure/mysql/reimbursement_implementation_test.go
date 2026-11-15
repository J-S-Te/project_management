package mysql

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type expenseImplementationDirectory struct{}

// List 返回包含分页、组织及项目经理角色查询证据的测试目录回执。
// 参数：ctx/query 为目录调用上下文和精确人员或角色过滤。
// 返回值：测试主体的完整目录页，不调用或修改真实人员目录。
func (expenseImplementationDirectory) List(
	ctx context.Context, query platform.OwnerDirectoryQuery,
) (platform.OwnerDirectoryPage, error) {
	return platform.OwnerDirectoryPage{Page: query.Page, PageSize: query.PageSize, Total: 1,
		Items: []platform.OwnerDirectoryUser{{UserID: query.UserID, DisplayName: "隔离测试人员",
			Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "qa-org"}}}}}, nil
}

// expenseImplementationFixture 创建资源编号与平台主体不同的当前实施计划。
// 参数：t 为测试上下文；仅使用独立内存数据库。
// 返回值：含同项目两个服务项和一名计划人员的仓储。
func expenseImplementationFixture(t *testing.T) *Repository {
	t.Helper()
	r := expenseRepositoryFixture(t)
	if err := r.db.AutoMigrate(&penetrationWorkPackageRecord{}); err != nil {
		t.Fatal(err)
	}
	seedExpenseImplementation(t, r)
	return r
}

// seedExpenseImplementation 在指定隔离仓储中写入实施人员与平台身份不同的计划。
// 参数：t 为测试上下文，r 为本次测试专用仓储。
// 返回值：无；夹具写入失败时终止测试。
func seedExpenseImplementation(t *testing.T, r *Repository) {
	t.Helper()
	for _, id := range []string{"a", "b"} {
		row := serviceItemRecord{ID: id, TenantID: "t", ProjectID: "p",
			Status: domain.ProjectStatusFieldCompleted, ReportStatus: "COMPILING", Version: 1}
		if id == "a" {
			row.ProjectManagerID = "manager"
		}
		if err := r.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	person := capabilityRecord{ID: "person", TenantID: "t", ResourceType: "PERSON",
		ResourceID: "P-17", ResourceName: "实施人员", UserID: "implementer",
		Status: "ACTIVE", CapabilityCodes: []byte(`[]`)}
	if err := r.db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	plan := implPlanRecord{ID: "plan", TenantID: "t", ServiceItemID: "a",
		Personnel: []byte(`[{"resource_type":"PERSON","resource_id":"P-17","resource_name":"实施人员"}]`)}
	if err := r.db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
}

// TestExpenseImplementationListPaginationAndScope 验证默认名单的独立分页与项目范围。
// 参数：t 为测试上下文。
// 返回值：无；跨服务项同人漏页、租户串用或范围失效时失败。
func TestExpenseImplementationListPaginationAndScope(t *testing.T) {
	r := expenseImplementationFixture(t)
	ctx := context.Background()
	scope := platform.ScopeFilter{TenantID: "t", AllowAll: true}
	plan := implPlanRecord{ID: "plan-b", TenantID: "t", ServiceItemID: "b",
		Personnel: []byte(`[{"resource_type":"PERSON","resource_id":"P-17"}]`)}
	if err := r.db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	first, err := r.ListExpenseImplementationParticipants(ctx, scope, "p", "", 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].ServiceItemID != "a" ||
		first.Items[0].SubjectID != "implementer" || first.NextCursor == "" {
		t.Fatalf("默认名单第一页错误：%+v，%v", first, err)
	}
	second, err := r.ListExpenseImplementationParticipants(ctx, scope, "p", first.NextCursor, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].ServiceItemID != "b" ||
		second.Items[0].ResourceID != "P-17" || second.NextCursor != "" {
		t.Fatalf("默认名单第二页错误：%+v，%v", second, err)
	}
	for _, cursor := range []string{"not-base64!", "e30"} {
		if _, err := r.ListExpenseImplementationParticipants(ctx, scope, "p", cursor, 1); !errors.Is(err, application.ErrValidation) {
			t.Fatalf("非法游标未拒绝：%q，%v", cursor, err)
		}
	}
	for _, denied := range []platform.ScopeFilter{
		{TenantID: "other", AllowAll: true}, {TenantID: "t", ProjectIDs: []string{"other"}},
	} {
		if _, err := r.ListExpenseImplementationParticipants(ctx, denied, "p", "", 1); err == nil {
			t.Fatal("越界默认名单被读取")
		}
	}
}

// TestExpenseImplementationIdentityDoesNotUseNameOrResourceID 验证默认资格只使用平台主体。
// 参数：t 为测试上下文。
// 返回值：无；按名称、资源编号或其他租户档案授予资格时失败。
func TestExpenseImplementationIdentityDoesNotUseNameOrResourceID(t *testing.T) {
	r := expenseImplementationFixture(t)
	foreign := capabilityRecord{ID: "foreign", TenantID: "other", ResourceType: "PERSON",
		ResourceID: "P-17", ResourceName: "实施人员", UserID: "foreign-user",
		Status: "ACTIVE", CapabilityCodes: []byte(`[]`)}
	if err := r.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	s := application.Service{Repo: r, Personnel: expenseImplementationDirectory{}}
	for _, actor := range []string{"P-17", "实施人员", "foreign-user"} {
		page, err := s.SearchExpenseReferences(context.Background(), "t", application.ExpenseSearchInput{
			ActorSubjectID: actor, PageSize: 20,
		})
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("不可信身份匹配：%q，%+v，%v", actor, page, err)
		}
	}
	if err := r.db.Model(&capabilityRecord{}).Where("id=?", "person").Update("user_id", "").Error; err != nil {
		t.Fatal(err)
	}
	page, err := r.ListExpenseImplementationParticipants(context.Background(),
		platform.ScopeFilter{TenantID: "t", AllowAll: true}, "p", "", 20)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("未关联平台账号不应假冒已授权：%+v，%v", page, err)
	}
}

// TestExpenseImplementationPersonnelInheritOwnServiceItem 验证计划人员自动获得本服务项资格。
// 参数：t 为测试上下文。
// 返回值：无；搜索、批验或作用域不一致时失败。
func TestExpenseImplementationPersonnelInheritOwnServiceItem(t *testing.T) {
	r := expenseImplementationFixture(t)
	s := application.Service{Repo: r, Personnel: expenseImplementationDirectory{}}
	ctx := context.Background()
	page, err := s.SearchExpenseReferences(ctx, "t", application.ExpenseSearchInput{
		ActorSubjectID: "implementer", PageSize: 20,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].ServiceItemID != "a" {
		t.Fatalf("计划人员没有默认本服务项资格：%+v，%v", page, err)
	}
	in := application.ExpenseBatchInput{ActorSubjectID: "implementer", ClaimID: "claim",
		DraftVersion: 1, References: []application.ExpenseReferenceInput{
			{ProjectID: "p", ServiceItemID: "a", OccurredDate: "2026-11-13"},
			{ProjectID: "p", ServiceItemID: "b", OccurredDate: "2026-11-13"},
			{ProjectID: "p", OccurredDate: "2026-11-13"},
		}}
	result, err := s.BatchValidateExpenseReferences(ctx, "t", in)
	if err != nil || len(result.Items) != 3 {
		t.Fatalf("批验失败：%+v，%v", result, err)
	}
	if !result.Items[0].Eligible || result.Items[0].EligibilitySource != "IMPLEMENTATION_PERSONNEL" ||
		result.Items[1].Eligible || result.Items[2].Eligible {
		t.Fatalf("计划资格扩散或未生效：%+v", result.Items)
	}
	var grants int64
	if err := r.db.Table("pm_expense_participant").Count(&grants).Error; err != nil || grants != 0 {
		t.Fatalf("自动资格不应生成手工授权：%d，%v", grants, err)
	}
}

// TestExpenseImplementationPersonnelFollowCurrentPlan 验证计划人员退出后不残留默认资格。
// 参数：t 为测试上下文。
// 返回值：无；搜索或批验仍保留已退出人员时失败。
func TestExpenseImplementationPersonnelFollowCurrentPlan(t *testing.T) {
	r := expenseImplementationFixture(t)
	s := application.Service{Repo: r, Personnel: expenseImplementationDirectory{}}
	ctx := context.Background()
	if err := r.db.Model(&implPlanRecord{}).Where("id=?", "plan").
		Update("personnel", []byte(`[]`)).Error; err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchExpenseReferences(ctx, "t", application.ExpenseSearchInput{
		ActorSubjectID: "implementer", PageSize: 20,
	})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("退出人员仍出现在搜索：%+v，%v", page, err)
	}
	result, err := s.BatchValidateExpenseReferences(ctx, "t", application.ExpenseBatchInput{
		ActorSubjectID: "implementer", ClaimID: "claim", DraftVersion: 1,
		References: []application.ExpenseReferenceInput{
			{ProjectID: "p", ServiceItemID: "a", OccurredDate: "2026-11-13"},
		},
	})
	if err != nil || result.Items[0].ParticipantEligible {
		t.Fatalf("退出人员仍可引用：%+v，%v", result, err)
	}
}
