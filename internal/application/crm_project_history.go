package application

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/platform"
)

const CRMProjectHistoryScope = "crm.project_history.read"

type CRMProjectSummary struct {
	ProjectID       string    `json:"project_id"`
	ProjectName     string    `json:"project_name"`
	ContractNo      string    `json:"contract_no"`
	Status          string    `json:"status"`
	Progress        int       `json:"progress_pct"`
	Due             string    `json:"due"`
	SourceUpdatedAt time.Time `json:"source_updated_at"`
}

type CRMProjectHistoryPage struct {
	Items    []CRMProjectSummary `json:"items"`
	TenantID string              `json:"tenant_id"`
	Customer string              `json:"customer_id"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
	Total    int64               `json:"total"`
}

type crmCustomerProjectRepository interface {
	CRMCustomerProjects(context.Context, string, string, int, int) ([]CRMProjectSummary, int64, error)
}

// ListCRMCustomerProjects 为已认证的 CRM 机器主体查询指定客户的项目摘要。
// 参数：ctx 为请求上下文，p 为验签主体，customer 为 CRM 客户主键，page/pageSize 为分页参数。
// 返回值：最小项目摘要分页，权限、参数或依赖异常时返回错误。
func (s *Service) ListCRMCustomerProjects(
	ctx context.Context, p platform.Principal, customer string, page, pageSize int,
) (CRMProjectHistoryPage, error) {
	if !p.Has(CRMProjectHistoryScope) || p.TenantID == "" || p.TenantID != strings.TrimSpace(p.TenantID) {
		return CRMProjectHistoryPage{}, ErrForbidden
	}
	id, err := strconv.ParseUint(customer, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != customer ||
		page < 1 || page > 1_000_000 || pageSize < 1 || pageSize > 100 {
		return CRMProjectHistoryPage{}, ErrValidation
	}
	repo, ok := s.Repo.(crmCustomerProjectRepository)
	if !ok {
		return CRMProjectHistoryPage{}, ErrProjectSnapshotUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	items, total, err := repo.CRMCustomerProjects(ctx, p.TenantID, customer, page, pageSize)
	if err != nil {
		return CRMProjectHistoryPage{}, err
	}
	return CRMProjectHistoryPage{
		Items: items, TenantID: p.TenantID, Customer: customer, Page: page, PageSize: pageSize, Total: total,
	}, nil
}
