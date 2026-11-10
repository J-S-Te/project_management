package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

const expenseDirectorMaximumUsers = 500
const expenseDirectorMaximumTargets = 20
const expenseDirectorPageSize = 50

type expenseDirectorProof struct {
	SubjectID       string   `json:"subject_id"`
	OrganizationIDs []string `json:"organization_ids"`
}

// resolveExpenseApprovers 解析费用经理与当前组织角色全集，不把未用节点缺失混入参与资格障碍。
// 参数：ctx为上下文，project为权威项目，reference为当前资格，actor为可信人员，approvalOrg为后端员工档案销售组织。
// 返回值：有真实版本或目录摘要的分节点目标；目录故障或销售组织不属当前主体时失败关闭。
func (s *Service) resolveExpenseApprovers(ctx context.Context, project domain.Project, reference domain.ExpenseProjectReference, actor, approvalOrg string) (domain.ExpenseProjectReference, error) {
	reference.ApproverReasonCodes = map[string][]string{}
	targets := []domain.ExpenseApprover{}
	if !reference.ParticipantEligible {
		return domain.AttachExpenseApprovers(reference, targets, "ACTOR_NOT_ELIGIBLE"), nil
	}
	if s.Personnel == nil {
		return domain.ExpenseProjectReference{}, ErrServiceTimeout
	}
	for _, target := range reference.RequiredApproverRefs {
		if target.NodeRole != domain.ExpenseNodeProjectOwner || target.SourceVersion == 0 {
			continue
		}
		users, err := s.expenseApproverCurrentRole(ctx, target.SubjectID)
		if err != nil {
			return domain.ExpenseProjectReference{}, err
		}
		if len(users) == 1 {
			target.SourceDigest = expenseDirectorDigest(project, "project_manager", "", users)
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		reference.ApproverReasonCodes[domain.ExpenseNodeProjectOwner] = []string{"PROJECT_EXPENSE_OWNER_MISSING"}
	}
	if approvalOrg != "" {
		if strings.TrimSpace(approvalOrg) != approvalOrg || len(approvalOrg) > 128 {
			return domain.ExpenseProjectReference{}, ErrValidation
		}
		if err := s.expenseSalesOrganization(ctx, actor, approvalOrg); err != nil {
			return domain.ExpenseProjectReference{}, err
		}
	}
	digests := []string{}
	for _, spec := range []struct{ node, role, org string }{{domain.ExpenseNodeTechnicalDirector, "technical_director", project.OwnerOrgID}, {domain.ExpenseNodeSalesDirector, "sales_director", approvalOrg}} {
		if strings.TrimSpace(spec.org) == "" {
			reason := spec.node + "_MISSING"
			if spec.node == domain.ExpenseNodeSalesDirector {
				reason = "SALES_APPROVAL_ORGANIZATION_REQUIRED"
			}
			reference.ApproverReasonCodes[spec.node] = []string{reason}
			continue
		}
		users, err := s.expenseDirectorUsers(ctx, spec.role)
		if err != nil {
			return domain.ExpenseProjectReference{}, err
		}
		digest := expenseDirectorDigest(project, spec.role, spec.org, users)
		digests = append(digests, digest)
		count := 0
		for _, user := range users {
			if !expenseDirectoryMember(user, spec.org) {
				continue
			}
			count++
			if count > expenseDirectorMaximumTargets {
				return domain.ExpenseProjectReference{}, ErrServiceTimeout
			}
			targets = append(targets, domain.ExpenseApprover{SubjectID: user.UserID, NodeRole: spec.node, SourceField: "role:" + spec.role, SourceOrgID: spec.org, SourceDigest: digest})
		}
		if count == 0 {
			reference.ApproverReasonCodes[spec.node] = []string{spec.node + "_MISSING"}
		}
	}
	raw, _ := json.Marshal(struct {
		Tenant, Project, ProjectOrg, ApprovalOrg string
		Digests                                  []string
	}{project.TenantID, project.ID, project.OwnerOrgID, approvalOrg, digests})
	hash := sha256.Sum256(raw)
	return domain.AttachExpenseApprovers(reference, targets, hex.EncodeToString(hash[:])), nil
}

// expenseApproverCurrentRole 精确复核已指派经理当前在职且持有项目经理角色，不限制为项目同组织。
// 参数：ctx为上下文，subject为对象明确指派的稳定经理身份。
// 返回值：当前角色及有效组织证据，缺资格返回空；目录故障或响应矛盾返回错误。
func (s *Service) expenseApproverCurrentRole(ctx context.Context, subject string) ([]platform.OwnerDirectoryUser, error) {
	if subject == "" || strings.TrimSpace(subject) != subject {
		return nil, nil
	}
	result, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{UserID: subject, RoleCodes: []string{"project_manager"}, Page: 1, PageSize: 2})
	if err != nil {
		return nil, ErrServiceTimeout
	}
	if result.Total == 0 && len(result.Items) == 0 {
		return nil, nil
	}
	if result.Page != 1 || result.PageSize != 2 || result.Total != 1 || len(result.Items) != 1 || result.Items[0].UserID != subject {
		return nil, ErrServiceTimeout
	}
	for _, org := range result.Items[0].Organizations {
		if org.OrganizationID != "" && strings.TrimSpace(org.OrganizationID) == org.OrganizationID {
			return result.Items, nil
		}
	}
	return nil, nil
}

// expenseSalesOrganization 验证服务端费用档案组织仍是申请人当前有效任职组织。
// 参数：ctx为上下文，actor为已验证主体，org为可信后端选择的销售组织。
// 返回值：非当前组织拒绝；目录故障失败关闭，不以项目技术组织替换销售组织。
func (s *Service) expenseSalesOrganization(ctx context.Context, actor, org string) error {
	page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{UserID: actor, Page: 1, PageSize: 2})
	if err != nil {
		return ErrServiceTimeout
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].UserID != actor {
		return ErrForbidden
	}
	if !expenseDirectoryMember(page.Items[0], org) {
		return ErrForbidden
	}
	return nil
}

// expenseDirectoryMember 仅比较平台有效组织稳定ID，不按名称、前缀或主组织猜范围。
// 参数：user为本次目录用户，org为权威业务组织ID。
// 返回值：目录是否明确包含该组织。
func expenseDirectoryMember(user platform.OwnerDirectoryUser, org string) bool {
	for _, membership := range user.Organizations {
		if membership.OrganizationID == org {
			return true
		}
	}
	return false
}

// expenseDirectorUsers 按单个精确角色读取完整有界目录，分页变化或重复主体不得产生部分目标。
// 参数：ctx为上下文，role为固定项目应用角色码。
// 返回值：最多500个当前有效角色用户；总数、页码、大小、记录数异常返回不可用。
func (s *Service) expenseDirectorUsers(ctx context.Context, role string) ([]platform.OwnerDirectoryUser, error) {
	users := []platform.OwnerDirectoryUser{}
	seen := map[string]bool{}
	total := int64(-1)
	for number := 1; number <= expenseDirectorMaximumUsers/expenseDirectorPageSize; number++ {
		page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{RoleCodes: []string{role}, Page: number, PageSize: expenseDirectorPageSize})
		if err != nil {
			return nil, ErrServiceTimeout
		}
		if page.Page != number || page.PageSize != expenseDirectorPageSize || page.Total < 0 || page.Total > expenseDirectorMaximumUsers || total >= 0 && page.Total != total {
			return nil, ErrServiceTimeout
		}
		total = page.Total
		expected := min(int(total)-len(users), expenseDirectorPageSize)
		if len(page.Items) != expected {
			return nil, ErrServiceTimeout
		}
		for _, user := range page.Items {
			if user.UserID == "" || strings.TrimSpace(user.UserID) != user.UserID || len(user.UserID) > 128 || seen[user.UserID] || len(user.Organizations) > 100 {
				return nil, ErrServiceTimeout
			}
			seen[user.UserID] = true
			users = append(users, user)
		}
		if len(users) == int(total) {
			return users, nil
		}
	}
	return nil, ErrServiceTimeout
}

// expenseDirectorDigest 摘要真实精确角色查询和有效组织集合，不伪造平台没有的授权数字版本。
// 参数：project为租户项目边界，role/org为实际审批角色组织，users为完整分页结果。
// 返回值：不包含姓名的规范SHA256来源证明，仍由目录机器身份绑定实际应用环境。
func expenseDirectorDigest(project domain.Project, role, org string, users []platform.OwnerDirectoryUser) string {
	proofs := []expenseDirectorProof{}
	for _, user := range users {
		ids := []string{}
		for _, membership := range user.Organizations {
			if !slices.Contains(ids, membership.OrganizationID) {
				ids = append(ids, membership.OrganizationID)
			}
		}
		sort.Strings(ids)
		proofs = append(proofs, expenseDirectorProof{user.UserID, ids})
	}
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].SubjectID < proofs[j].SubjectID })
	raw, _ := json.Marshal(struct {
		Schema, Tenant, Project, Role, Organization string
		Members                                     []expenseDirectorProof
	}{"PROJECT_OWNER_DIRECTORY_ROLE_ORG_V1", project.TenantID, project.ID, role, org, proofs})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
