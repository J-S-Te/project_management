package mysql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
)

type identityContractDirectory struct{}

func (identityContractDirectory) List(context.Context, int) ([]platform.ApprovedContract, error) {
	return []platform.ApprovedContract{{ID: "SUP", Number: "HT-SUP", CustomerID: "customer", Status: "active", ApprovalPassed: true}}, nil
}
func (identityContractDirectory) Get(context.Context, string) (platform.ApprovedContract, error) {
	return platform.ApprovedContract{}, errors.New("unexpected get")
}
func (identityContractDirectory) ListReferences(context.Context, string, int) ([]platform.ApprovedContract, string, error) {
	return nil, "", errors.New("unexpected references")
}
func (identityContractDirectory) GetServiceItems(context.Context, string) (platform.ApprovedContractServiceCatalog, error) {
	return platform.ApprovedContractServiceCatalog{}, errors.New("unexpected services")
}

func TestLegacyContractIdentityLookupAndSupplementCatalogKeepTenantAndProjectScope(t *testing.T) {
	db := equipmentAtomicityDB(t) // 仅使用内存 SQLite，不使用 DSN 或 MySQL 连接。
	now := time.Now().UTC()
	for _, row := range []projectRecord{
		{ID: "legacy-empty", TenantID: "tenant", Contract: "HT-OLD", ContractVersion: "1", CustomerID: "customer", OwnerOrgID: "org-allowed", CreatedAt: now, UpdatedAt: now},
		{ID: "legacy-number", TenantID: "tenant", ContractID: "HT-NUMBER", Contract: "HT-NUMBER", ContractVersion: "1", CustomerID: "customer", OwnerOrgID: "org-denied", CreatedAt: now, UpdatedAt: now},
		{ID: "other-tenant", TenantID: "other", Contract: "HT-OLD", ContractVersion: "2", CustomerID: "customer", OwnerOrgID: "org-allowed", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepository(db)
	ctx := context.Background()
	for _, tc := range []struct{ number, wantID string }{{"HT-OLD", "legacy-empty"}, {"HT-NUMBER", "legacy-number"}} {
		project, err := repo.FindProjectByContract(ctx, platform.ScopeFilter{TenantID: "tenant", AllowAll: true}, "canonical", tc.number)
		if err != nil || project.ID != tc.wantID {
			t.Fatalf("lookup %s: %+v %v", tc.number, project, err)
		}
	}
	_, err := repo.FindProjectByContract(ctx, platform.ScopeFilter{TenantID: "tenant", OrganizationIDs: []string{"org-allowed"}}, "canonical", "HT-NUMBER")
	if !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("lookup widened organization scope: %v", err)
	}
	s := application.Service{Repo: repo, Contracts: identityContractDirectory{}}
	p := platform.Principal{TenantID: "tenant", UserID: "adjuster", Permissions: map[string]bool{"project.decomposition.manage": true}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "ORG", ScopeID: "org-allowed"}}}
	contracts, err := s.ListSupplementContracts(ctx, p, "legacy-empty", 200)
	if err != nil || len(contracts) != 1 || contracts[0].ID != "SUP" {
		t.Fatalf("authorized adjuster: %+v %v", contracts, err)
	}
	for _, deniedID := range []string{"legacy-number", "other-tenant", "missing"} {
		if _, err := s.ListSupplementContracts(ctx, p, deniedID, 200); !errors.Is(err, application.ErrNotFound) {
			t.Fatalf("catalog leaked project %s: %v", deniedID, err)
		}
	}
	p.DataScopes = nil
	if _, err := s.ListSupplementContracts(ctx, p, "legacy-empty", 200); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("missing data scope accepted: %v", err)
	}
}
