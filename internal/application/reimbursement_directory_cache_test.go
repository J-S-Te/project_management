package application

import (
	"context"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type expenseApproverRepository struct {
	Repository
	ExpenseRepository
	project domain.Project
}

// GetProject 提供严格同租户项目的隔离应用测试来源。
// 参数：ctx/filter/id为查询边界。
// 返回值：明确合成项目；错范围不返回其他项目。
func (r *expenseApproverRepository) GetProject(ctx context.Context, filter platform.ScopeFilter, id string) (domain.Project, error) {
	if filter.TenantID != r.project.TenantID || id != r.project.ID {
		return domain.Project{}, ErrForbidden
	}
	return r.project, nil
}

// ReadExpenseParticipant 表达测试中没有显式费用资格，由真实项目owner规则计算。
// 参数：ctx/tenant/project/actor为精确读取上下文。
// 返回值：无显式资格，不模拟无条件资格成功。
func (r *expenseApproverRepository) ReadExpenseParticipant(ctx context.Context, tenant, project, actor string) (*domain.ExpenseParticipant, error) {
	return nil, nil
}

// TestExpenseDirectoryCacheIsPerBatchAndDoesNotExtendReferenceWindow 验证同批重复引用只查一次目录且下一批立即重验经理角色。
// 参数：t为测试上下文。
// 返回值：无；请求内重复调用、跨请求缓存或有效期被延长时失败。
func TestExpenseDirectoryCacheIsPerBatchAndDoesNotExtendReferenceWindow(t *testing.T) {
	project, directory := expenseRoleFixture()
	service := Service{Repo: &expenseApproverRepository{project: project}, Personnel: directory}
	request := ExpenseBatchInput{ActorSubjectID: "actor", ClaimID: "c", DraftVersion: 1, References: []ExpenseReferenceInput{{ProjectID: "p", OccurredDate: "2026-11-10", ApprovalOrgID: "sales-org"}, {ProjectID: "p", OccurredDate: "2026-11-11", ApprovalOrgID: "sales-org"}}}
	result, err := service.BatchValidateExpenseReferences(context.Background(), "t", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || len(directory.queries) != 5 {
		t.Fatal("same-batch directory queried repeatedly", len(directory.queries))
	}
	if !result.Items[0].ValidUntil.Equal(result.Items[1].ValidUntil) {
		t.Fatal("cached authority lifetime extended per reference")
	}
	directory.managerRoleActive = false
	result, err = service.BatchValidateExpenseReferences(context.Background(), "t", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(directory.queries) != 10 {
		t.Fatal("cache escaped request lifetime", len(directory.queries))
	}
	for _, reference := range result.Items {
		for _, target := range reference.RequiredApproverRefs {
			if target.NodeRole == domain.ExpenseNodeProjectOwner {
				t.Fatal("new batch ignored manager role revocation")
			}
		}
	}
}
