package application

import (
	"context"
	"fmt"

	"github.com/j-s-te/project-management/internal/platform"
)

// ListSupplementContracts 返回合同来源项目的受控补充协议候选。
// 参数：ctx 为请求，p 为真实主体，projectID 为目标项目，limit 为候选上限。
// 返回值：同客户已审批合同；原生来源、越权或目录失败时返回错误。
func (s *Service) ListSupplementContracts(ctx context.Context, p platform.Principal, projectID string, limit int) ([]platform.ApprovedContract, error) {
	filter, err := authorizeProjectScope(p, "project.decomposition.manage")
	if err != nil {
		return nil, err
	}
	project, err := s.Repo.GetProject(ctx, filter, projectID)
	if err != nil {
		return nil, err
	}
	if project.SourceKind == "NATIVE" {
		return nil, PreconditionError("原生项目范围变更尚未启用受控审批流程")
	}
	if s.Contracts == nil {
		return nil, ErrContractUnavailable
	}
	contracts, err := s.Contracts.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContractUnavailable, err)
	}
	eligible := make([]platform.ApprovedContract, 0, len(contracts))
	for _, contract := range contracts {
		if contract.ID != "" && validateSupplementContract(project, contract.ID, contract) == nil {
			eligible = append(eligible, contract)
		}
	}
	return eligible, nil
}
