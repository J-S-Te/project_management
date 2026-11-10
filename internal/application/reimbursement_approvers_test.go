package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type expenseRoleDirectory struct {
	roles             map[string][]platform.OwnerDirectoryUser
	active            map[string]bool
	queries           []platform.OwnerDirectoryQuery
	failurePage       int
	badMetadata       bool
	managerRoleActive bool
}

// List 提供按精确角色隔离的当前目录替身，保留真实分页契约用于范围测试。
// 参数：ctx为上下文，query为待核对的人员或角色分页请求。
// 返回值：明确当前有效成员或故障，不返回本地业务角色映射。
func (d *expenseRoleDirectory) List(ctx context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	d.queries = append(d.queries, query)
	result := platform.OwnerDirectoryPage{Page: query.Page, PageSize: query.PageSize, Items: []platform.OwnerDirectoryUser{}}
	if query.UserID != "" {
		if query.UserID == "pm" && slices.Contains(query.RoleCodes, "project_manager") && !d.managerRoleActive {
			return result, nil
		}
		if d.active[query.UserID] {
			result.Total = 1
			result.Items = []platform.OwnerDirectoryUser{{UserID: query.UserID, Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "sales-org"}}}}
		}
		return result, nil
	}
	if len(query.RoleCodes) != 1 {
		return result, errors.New("exact role required")
	}
	if query.Page == d.failurePage {
		return result, errors.New("directory unavailable")
	}
	users := d.roles[query.RoleCodes[0]]
	result.Total = int64(len(users))
	start := (query.Page - 1) * query.PageSize
	if start < len(users) {
		result.Items = users[start:min(start+query.PageSize, len(users))]
	}
	if d.badMetadata {
		result.Page = 99
	}
	return result, nil
}

// expenseRoleFixture 构造跨两页技术总监、不同组织及独立销售总监的合成目录。
// 参数：无。
// 返回值：项目和严格角色目录，正确组织目标不等于任意第一人。
func expenseRoleFixture() (domain.Project, *expenseRoleDirectory) {
	d := &expenseRoleDirectory{roles: map[string][]platform.OwnerDirectoryUser{}, active: map[string]bool{"pm": true, "actor": true}, managerRoleActive: true}
	for i := 0; i < 51; i++ {
		org := "foreign"
		if i == 0 || i == 50 {
			org = "org"
		}
		d.roles["technical_director"] = append(d.roles["technical_director"], platform.OwnerDirectoryUser{UserID: fmt.Sprintf("tech-%02d", i), Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: org}}})
	}
	d.roles["sales_director"] = []platform.OwnerDirectoryUser{{UserID: "wrong-sales", Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "org"}}}, {UserID: "sales", Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "sales-org"}}}}
	d.roles["business_admin"] = []platform.OwnerDirectoryUser{{UserID: "not-sales", Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "org"}}}}
	return domain.Project{ID: "p", TenantID: "t", OwnerOrgID: "org", ManagerIdentityID: "pm", OwnerIdentityID: "actor", Version: 7}, d
}

// TestExpenseApproversUseProjectRolesAllPagesAndOrganization 验证全部角色分页、项目组织范围及真实版本/摘要。
// 参数：t为测试上下文。
// 返回值：无；首人猜测、错组织角色或伪造数字版本时失败。
func TestExpenseApproversUseProjectRolesAllPagesAndOrganization(t *testing.T) {
	p, d := expenseRoleFixture()
	service := Service{Personnel: d}
	reference := domain.ExpenseReference(p, nil, "actor", nil, time.Now().UTC())
	resolved, err := service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "sales-org")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string][]string{}
	for _, target := range resolved.RequiredApproverRefs {
		ids[target.NodeRole] = append(ids[target.NodeRole], target.SubjectID)
		if target.NodeRole != domain.ExpenseNodeProjectOwner && (target.SourceVersion != 0 || len(target.SourceDigest) != 64 || (target.NodeRole == domain.ExpenseNodeTechnicalDirector && target.SourceOrgID != "org" || target.NodeRole == domain.ExpenseNodeSalesDirector && target.SourceOrgID != "sales-org") || target.SourceField != "role:technical_director" && target.SourceField != "role:sales_director") {
			t.Fatal("unproven director", target)
		}
	}
	if len(resolved.RequiredApproverRefs) != 4 || !slices.Equal(ids[domain.ExpenseNodeTechnicalDirector], []string{"tech-00", "tech-50"}) || !slices.Equal(ids[domain.ExpenseNodeSalesDirector], []string{"sales"}) || !slices.Equal(ids[domain.ExpenseNodeProjectOwner], []string{"pm"}) {
		t.Fatal("wrong scoped ALL targets", ids)
	}
	for _, query := range d.queries {
		if slices.Contains(query.RoleCodes, "business_admin") {
			t.Fatal("business admin substituted sales")
		}
	}
	firstVersion := resolved.ReferenceVersion
	d.roles["technical_director"][50].Organizations = []platform.OwnerDirectoryOrganization{{OrganizationID: "foreign"}}
	resolved, err = service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "sales-org")
	if err != nil || firstVersion == resolved.ReferenceVersion {
		t.Fatal("role/scope change omitted", err)
	}
}

// TestExpenseApproverDirectoryFailsClosedAndMissingOrgDoesNotGuess 验证目录故障不返回部分目标，缺组织只保留真实经理。
// 参数：t为测试上下文。
// 返回值：无；缺目录、错误页或未知组织被放行时失败。
func TestExpenseApproverDirectoryFailsClosedAndMissingOrgDoesNotGuess(t *testing.T) {
	for _, kind := range []string{"page-failure", "bad-metadata", "missing"} {
		t.Run(kind, func(t *testing.T) {
			p, d := expenseRoleFixture()
			service := Service{Personnel: d}
			if kind == "page-failure" {
				d.failurePage = 2
			}
			if kind == "bad-metadata" {
				d.badMetadata = true
			}
			if kind == "missing" {
				service.Personnel = nil
			}
			reference := domain.ExpenseReference(p, nil, "actor", nil, time.Now().UTC())
			if _, err := service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "sales-org"); err == nil {
				t.Fatal("directory failure accepted")
			}
		})
	}
	p, d := expenseRoleFixture()
	p.OwnerOrgID = ""
	service := Service{Personnel: d}
	reference := domain.ExpenseReference(p, nil, "actor", nil, time.Now().UTC())
	resolved, err := service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "")
	if err != nil || len(resolved.RequiredApproverRefs) != 1 || resolved.RequiredApproverRefs[0].NodeRole != domain.ExpenseNodeProjectOwner || !slices.Contains(resolved.ApproverReasonCodes[domain.ExpenseNodeTechnicalDirector], "TECHNICAL_DIRECTOR_MISSING") || !slices.Contains(resolved.ApproverReasonCodes[domain.ExpenseNodeSalesDirector], "SALES_APPROVAL_ORGANIZATION_REQUIRED") {
		t.Fatal("unknown project org guessed", resolved, err)
	}
	for _, query := range d.queries {
		if query.UserID == "" && len(query.RoleCodes) > 0 {
			t.Fatal("queried unrelated global directors without project org")
		}
	}
}

// TestExpenseSalesOrganizationAndNodeMissingStaySeparate 验证销售组织来自当前任职且节点缺失不污染资格障碍。
// 参数：t为测试上下文。
// 返回值：无；技术组织误当销售组织或缺PM阻断销售资格时失败。
func TestExpenseSalesOrganizationAndNodeMissingStaySeparate(t *testing.T) {
	p, d := expenseRoleFixture()
	service := Service{Personnel: d}
	reference := domain.ExpenseReference(p, nil, "actor", nil, time.Now().UTC())
	if _, err := service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "org"); !errors.Is(err, ErrForbidden) {
		t.Fatal("project technical org substituted actor sales org", err)
	}
	d.active["pm"] = false
	resolved, err := service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "sales-org")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Eligible || !resolved.ParticipantEligible || len(resolved.ReasonCodes) != 0 || !slices.Contains(resolved.ApproverReasonCodes[domain.ExpenseNodeProjectOwner], "PROJECT_EXPENSE_OWNER_MISSING") {
		t.Fatal("node absence changed eligibility", resolved)
	}
	found := false
	for _, target := range resolved.RequiredApproverRefs {
		if target.NodeRole == domain.ExpenseNodeSalesDirector {
			found = target.SourceOrgID == "sales-org" && target.SubjectID == "sales"
		}
	}
	if !found {
		t.Fatal("valid sales director lost because PM missing")
	}
}

// TestExpenseDirectorBoundsAndEmptyRoleAreExplicit 验证角色分页和多目标有硬边界且缺角色不回退本地管理员。
// 参数：t为测试上下文。
// 返回值：无；截断或隐式替代时失败。
func TestExpenseDirectorBoundsAndEmptyRoleAreExplicit(t *testing.T) {
	for _, kind := range []string{"users", "targets"} {
		t.Run(kind, func(t *testing.T) {
			p, d := expenseRoleFixture()
			count := 501
			if kind == "targets" {
				count = 21
			}
			d.roles["technical_director"] = nil
			for i := 0; i < count; i++ {
				d.roles["technical_director"] = append(d.roles["technical_director"], platform.OwnerDirectoryUser{UserID: fmt.Sprintf("director-%d", i), Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "org"}}})
			}
			service := Service{Personnel: d}
			reference := domain.ExpenseReference(p, nil, "actor", nil, time.Now().UTC())
			if _, err := service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "sales-org"); err == nil {
				t.Fatal("bounded result silently truncated")
			}
		})
	}
	p, d := expenseRoleFixture()
	d.roles["sales_director"] = nil
	service := Service{Personnel: d}
	reference := domain.ExpenseReference(p, nil, "actor", nil, time.Now().UTC())
	resolved, err := service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "sales-org")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(resolved.ApproverReasonCodes[domain.ExpenseNodeSalesDirector], "SALES_DIRECTOR_MISSING") {
		t.Fatal("missing sales not explicit")
	}
	for _, target := range resolved.RequiredApproverRefs {
		if target.NodeRole == domain.ExpenseNodeSalesDirector {
			t.Fatal("business admin substituted sales")
		}
	}
}

// TestExpenseManagerRoleRevocationRemovesOnlyApprovalTarget 验证稳定经理字段不替代当前项目经理角色，撤销不影响费用参与资格。
// 参数：t为测试上下文。
// 返回值：无；角色撤销后仍有经理目标或参与资格被撤销时失败。
func TestExpenseManagerRoleRevocationRemovesOnlyApprovalTarget(t *testing.T) {
	p, d := expenseRoleFixture()
	d.managerRoleActive = false
	service := Service{Personnel: d}
	reference := domain.ExpenseReference(p, nil, "actor", nil, time.Now().UTC())
	resolved, err := service.resolveExpenseApprovers(context.Background(), p, reference, "actor", "sales-org")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range resolved.RequiredApproverRefs {
		if target.NodeRole == domain.ExpenseNodeProjectOwner {
			t.Fatal("revoked project manager role remained", target)
		}
	}
	if !resolved.Eligible || !resolved.ParticipantEligible || len(resolved.ReasonCodes) != 0 || !slices.Contains(resolved.ApproverReasonCodes[domain.ExpenseNodeProjectOwner], "PROJECT_EXPENSE_OWNER_MISSING") {
		t.Fatal("manager role revocation changed participation", resolved)
	}
}
