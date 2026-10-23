package application

import (
	"context"
	"fmt"

	"github.com/j-s-te/project-management/internal/platform"
)

// ListSupplementContracts 限于调用方可调整的项目。
// 合同审批仍会激活其自身项目；这不影响其
// 作为其他项目范围调整证据的资格。
func (s *Service) ListSupplementContracts(ctx context.Context, p platform.Principal, projectID string, limit int) ([]platform.ApprovedContract, error) {
	filter, err := authorizeProjectScope(p, "project.decomposition.manage")
	if err != nil {
		return nil, err
	}
	project, err := s.Repo.GetProject(ctx, filter, projectID)
	if err != nil {
		return nil, err
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
