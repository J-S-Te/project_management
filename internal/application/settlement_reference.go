package application

import (
	"context"
	"strings"

	"github.com/j-s-te/project-management/internal/platform"
)

type SettlementProjectReference struct {
	TenantID   string `json:"tenant_id"`
	ProjectID  string `json:"project_id"`
	ContractID string `json:"contract_id"`
	CustomerID string `json:"customer_id"`
	Name       string `json:"name"`
	Version    uint64 `json:"version"`
	Active     bool   `json:"active"`
}

// ResolveSettlementProjectReference 为独立机器主体返回同租户最小权威引用。
// 参数：ctx 控制查询，p 是验签后的机器主体，id 为项目标识。
// 返回值：稳定项目/合同/客户标识；缺失、越权或不完整时返回错误。
func (s *Service) ResolveSettlementProjectReference(ctx context.Context, p platform.Principal, id string) (SettlementProjectReference, error) {
	if !p.Has("settlement.project_reference.read") || p.TenantID == "" || p.TenantID != strings.TrimSpace(p.TenantID) {
		return SettlementProjectReference{}, ErrForbidden
	}
	if id == "" || id != strings.TrimSpace(id) || len(id) > 128 {
		return SettlementProjectReference{}, ErrValidation
	}
	project, err := s.Repo.GetProject(ctx, platform.ScopeFilter{TenantID: p.TenantID, AllowAll: true}, id)
	if err != nil {
		return SettlementProjectReference{}, err
	}
	if project.ID != id || project.TenantID != p.TenantID || project.ContractID == "" || project.CustomerID == "" || project.Version == 0 {
		return SettlementProjectReference{}, ErrConflict
	}
	return SettlementProjectReference{TenantID: p.TenantID, ProjectID: project.ID, ContractID: project.ContractID, CustomerID: project.CustomerID, Name: project.Name, Version: project.Version, Active: project.Status != "已终止" && project.Status != "terminated"}, nil
}
