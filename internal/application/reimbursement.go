package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type ExpenseCandidate struct {
	ProjectID     string
	ServiceItemID string
}
type ExpenseRepository interface {
	ExpenseCandidates(context.Context, string, string, string, string, string, int) ([]ExpenseCandidate, error)
	ReadExpenseParticipant(context.Context, string, string, string) (*domain.ExpenseParticipant, error)
	ListExpenseParticipants(context.Context, platform.ScopeFilter, string, string, int) ([]domain.ExpenseParticipant, error)
	SaveExpenseParticipant(context.Context, platform.ScopeFilter, domain.ExpenseParticipant, uint64) (domain.ExpenseParticipant, error)
	ApplyExpenseSpend(context.Context, domain.SpendSnapshot) (domain.SpendReceipt, error)
	ReconcileExpenseSpend(context.Context, string, string, string, int) ([]domain.SpendReconciliation, error)
	RebuildExpenseSpend(context.Context, string, string, string, string, string) error
	ReadExpenseSpendSummary(context.Context, string, string, string, int) ([]domain.ExpenseSpendSummary, error)
}
type ExpenseReferenceInput struct {
	ApprovalOrgID string `json:"approval_org_id,omitempty"`
	ProjectID     string `json:"project_id"`
	ServiceItemID string `json:"service_item_id"`
	OccurredDate  string `json:"occurred_date"`
}
type ExpenseSearchInput struct {
	ActorSubjectID string `json:"actor_subject_id"`
	Keyword        string `json:"keyword"`
	Cursor         string `json:"cursor"`
	PageSize       int    `json:"page_size"`
}
type ExpenseBatchInput struct {
	ActorSubjectID string                  `json:"actor_subject_id"`
	ClaimID        string                  `json:"claim_id"`
	DraftVersion   uint64                  `json:"draft_version"`
	References     []ExpenseReferenceInput `json:"references"`
}
type ExpenseReferencePage struct {
	Items        []domain.ExpenseProjectReference `json:"items"`
	NextCursor   string                           `json:"next_cursor,omitempty"`
	ClaimID      string                           `json:"claim_id,omitempty"`
	DraftVersion uint64                           `json:"draft_version,omitempty"`
}
type ExpenseParticipantInput struct {
	SubjectID       string    `json:"subject_id"`
	Status          string    `json:"status"`
	ValidFrom       time.Time `json:"valid_from"`
	ValidUntil      time.Time `json:"valid_until"`
	Reason          string    `json:"reason"`
	ExpectedVersion uint64    `json:"expected_version"`
}

// expenseRepository 获取可用的可靠报销仓储，缺失时失败关闭。
// 参数：无。
// 返回值：完整报销仓储；未接线返回服务不可用错误。
func (s *Service) expenseRepository() (ExpenseRepository, error) {
	repo, ok := s.Repo.(ExpenseRepository)
	if !ok {
		return nil, ErrServiceTimeout
	}
	return repo, nil
}

// validateExpenseActor 经当前平台人员目录精确验证稳定主体及本应用有效授权。
// 参数：ctx 控制外部查询，actor 为机器代理请求中的人员主体。
// 返回值：目录精确包含唯一当前主体时成功；目录故障或无授权失败关闭。
func (s *Service) validateExpenseActor(ctx context.Context, actor string) error {
	if strings.TrimSpace(actor) != actor || actor == "" || len(actor) > 128 {
		return ErrValidation
	}
	if s.Personnel == nil {
		return ErrServiceTimeout
	}
	page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{UserID: actor, Page: 1, PageSize: 2})
	if err != nil {
		return ErrServiceTimeout
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].UserID != actor {
		return ErrForbidden
	}
	return nil
}

// resolveExpenseReference 用权威同租户项目、有效服务项及名单执行唯一资格算法。
// 参数：ctx 为读取上下文，tenant/actor 为可信边界，input 为待验证关联。
// 返回值：最小权威引用，项目不存在或服务项归属不符时拒绝。
func (s *Service) resolveExpenseReference(ctx context.Context, tenant, actor string, input ExpenseReferenceInput) (domain.ExpenseProjectReference, error) {
	scope := platform.ScopeFilter{TenantID: tenant, AllowAll: true}
	p, err := s.Repo.GetProject(ctx, scope, input.ProjectID)
	if err != nil {
		return domain.ExpenseProjectReference{}, err
	}
	repo, err := s.expenseRepository()
	if err != nil {
		return domain.ExpenseProjectReference{}, err
	}
	if p.ID != input.ProjectID || p.TenantID != tenant {
		return domain.ExpenseProjectReference{}, ErrForbidden
	}
	grant, err := repo.ReadExpenseParticipant(ctx, tenant, p.ID, actor)
	if err != nil {
		return domain.ExpenseProjectReference{}, err
	}
	var item *domain.ServiceItem
	if input.ServiceItemID != "" {
		value, err := s.Repo.GetServiceItem(ctx, scope, input.ServiceItemID)
		if err != nil {
			return domain.ExpenseProjectReference{}, err
		}
		if value.ProjectID != p.ID || value.TenantID != tenant {
			return domain.ExpenseProjectReference{}, ErrForbidden
		}
		item = &value
	}
	reference := domain.ExpenseReference(p, item, actor, grant, time.Now().UTC())
	travel, err := s.expenseTravelFacts(ctx, tenant, actor, p, item, reference, input.OccurredDate)
	if err != nil {
		return domain.ExpenseProjectReference{}, err
	}
	reference = domain.AttachExpenseTravelSnapshot(reference, travel)
	reference, err = s.resolveExpenseApprovers(ctx, p, reference, actor, input.ApprovalOrgID)
	if err != nil {
		return domain.ExpenseProjectReference{}, err
	}
	return s.expenseDirectoryLifetime(reference), nil
}

// SearchExpenseReferences 按可信主体分页扫描并用批验相同算法筛选最小引用。
// 参数：ctx 为请求上下文，tenant 来自验签令牌，in 为限定长度的查询。
// 返回值：参与资格有效的最小引用；终止仍Eligible为false，依赖失败不使用缓存。
func (s *Service) SearchExpenseReferences(ctx context.Context, tenant string, in ExpenseSearchInput) (ExpenseReferencePage, error) {
	out := ExpenseReferencePage{Items: []domain.ExpenseProjectReference{}}
	if tenant == "" || len(in.Keyword) > 128 || len(in.Cursor) > 512 || in.PageSize < 1 || in.PageSize > 50 {
		return out, ErrValidation
	}
	s = s.expenseDirectoryRequest()
	if err := s.validateExpenseActor(ctx, in.ActorSubjectID); err != nil {
		return out, err
	}
	repo, err := s.expenseRepository()
	if err != nil {
		return out, err
	}
	var after ExpenseCandidate
	if in.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(in.Cursor)
		if e != nil || json.Unmarshal(b, &after) != nil {
			return out, ErrValidation
		}
	}
	candidates, err := repo.ExpenseCandidates(ctx, tenant, in.ActorSubjectID, in.Keyword, after.ProjectID, after.ServiceItemID, in.PageSize+1)
	if err != nil {
		return out, err
	}
	more := len(candidates) > in.PageSize
	if more {
		candidates = candidates[:in.PageSize]
	}
	for _, candidate := range candidates {
		ref, err := s.resolveExpenseReference(ctx, tenant, in.ActorSubjectID, ExpenseReferenceInput{ProjectID: candidate.ProjectID, ServiceItemID: candidate.ServiceItemID})
		if err != nil {
			return out, err
		}
		if ref.Eligible || ref.ParticipantEligible {
			out.Items = append(out.Items, ref)
		}
	}
	if more && len(candidates) > 0 {
		b, _ := json.Marshal(candidates[len(candidates)-1])
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}

// BatchValidateExpenseReferences 在提交前重新验证主体、项目和费用日期形状。
// 参数：ctx 为请求上下文，tenant 来自机器令牌，in 为草稿绑定的引用集合。
// 返回值：逐项最新权威资格；未找到或跨租户不伪造可用引用。
func (s *Service) BatchValidateExpenseReferences(ctx context.Context, tenant string, in ExpenseBatchInput) (ExpenseReferencePage, error) {
	out := ExpenseReferencePage{Items: []domain.ExpenseProjectReference{}, ClaimID: in.ClaimID, DraftVersion: in.DraftVersion}
	if tenant == "" || in.ClaimID == "" || in.DraftVersion == 0 || len(in.References) == 0 || len(in.References) > 100 {
		return out, ErrValidation
	}
	s = s.expenseDirectoryRequest()
	if err := s.validateExpenseActor(ctx, in.ActorSubjectID); err != nil {
		return out, err
	}
	for _, input := range in.References {
		if input.ProjectID == "" || len(input.ProjectID) > 128 || len(input.ServiceItemID) > 128 || len(input.ApprovalOrgID) > 128 {
			return out, ErrValidation
		}
		at, err := time.Parse("2006-01-02", input.OccurredDate)
		if err != nil || at.Format("2006-01-02") != input.OccurredDate {
			return out, ErrValidation
		}
		ref, err := s.resolveExpenseReference(ctx, tenant, in.ActorSubjectID, input)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, ref)
	}
	return out, nil
}

// ManageExpenseParticipant 在当前项目范围与受控人员目录内新增、调整或撤销资格。
// 参数：ctx 为上下文，p 为真实会话，project 为目标，in 为带版本与原因的名单命令。
// 返回值：持久化后的新版本资格；权限、范围或人员无效时失败。
func (s *Service) ManageExpenseParticipant(ctx context.Context, p platform.Principal, project string, in ExpenseParticipantInput) (domain.ExpenseParticipant, error) {
	if !p.Has("project.expense_participant.manage") {
		return domain.ExpenseParticipant{}, ErrForbidden
	}
	scope, err := p.ProjectScopeFilter()
	if err != nil {
		return domain.ExpenseParticipant{}, ErrForbidden
	}
	if _, err = s.Repo.GetProject(ctx, scope, project); err != nil {
		return domain.ExpenseParticipant{}, err
	}
	if in.Status != "ACTIVE" && in.Status != "REVOKED" || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1000 || in.SubjectID == "" || len(in.SubjectID) > 128 {
		return domain.ExpenseParticipant{}, ErrValidation
	}
	if in.Status == "ACTIVE" {
		if !in.ValidFrom.Before(in.ValidUntil) || in.ValidUntil.Sub(in.ValidFrom) > 366*24*time.Hour {
			return domain.ExpenseParticipant{}, ErrValidation
		}
		if err := s.validateExpenseActor(ctx, in.SubjectID); err != nil {
			return domain.ExpenseParticipant{}, err
		}
	}
	repo, err := s.expenseRepository()
	if err != nil {
		return domain.ExpenseParticipant{}, err
	}
	return repo.SaveExpenseParticipant(ctx, scope, domain.ExpenseParticipant{TenantID: p.TenantID, ProjectID: project, SubjectID: in.SubjectID, Status: in.Status, ValidFrom: in.ValidFrom, ValidUntil: in.ValidUntil, GrantedBy: p.IdentityID, Reason: strings.TrimSpace(in.Reason)}, in.ExpectedVersion)
}
