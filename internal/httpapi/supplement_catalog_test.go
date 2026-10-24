package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

type supplementCatalogVerifier struct{ approvedContractVerifier }

func (supplementCatalogVerifier) List(context.Context, int) ([]platform.ApprovedContract, error) {
	return []platform.ApprovedContract{
		{ID: "original", Number: "HT-ORIGINAL", CustomerID: "customer", Status: "active", ApprovalPassed: true},
		{ID: "supplement", Number: "HT-SUP", CustomerID: "customer", Status: "active", ApprovalPassed: true},
		{ID: "approved", Number: "HT-APP", CustomerID: "customer", Status: "approved", ApprovalPassed: true},
		{ID: "other", CustomerID: "other", Status: "active", ApprovalPassed: true},
		{ID: "terminated", CustomerID: "customer", Status: "terminated", ApprovalPassed: true},
		{ID: "pending", CustomerID: "customer", Status: "pending", ApprovalPassed: true},
		{ID: "unapproved", CustomerID: "customer", Status: "active"},
	}, nil
}

type unavailableSupplementCatalog struct{ approvedContractVerifier }

func (unavailableSupplementCatalog) List(context.Context, int) ([]platform.ApprovedContract, error) {
	return nil, errors.New("contract directory unavailable")
}

func TestSupplementCatalogDependencyFailureReturns503(t *testing.T) {
	p := platform.Principal{TenantID: "tenant-1", UserID: "adjuster", Permissions: map[string]bool{"project.decomposition.manage": true}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}}}
	r := &repo{projects: []domain.Project{{ID: "PJ-1", TenantID: "tenant-1", CustomerID: "customer"}}}
	for _, contracts := range []platform.ApprovedContractVerifier{nil, unavailableSupplementCatalog{}} {
		h := httpapi.NewRouter(&application.Service{Repo: r, Contracts: contracts}, identity{p: p}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		res := perform(h, http.MethodGet, "/api/v1/projects/PJ-1/supplement-contracts", "")
		if res.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
		}
	}
}

func TestCompatibilityActivationHTTPWithoutNumberUsesAuthorityOrFailsClosed(t *testing.T) {
	p := platform.Principal{TenantID: "tenant-1", UserID: "contract", Permissions: map[string]bool{"project.contract.import": true}, DataScopes: []platform.DataScope{{RoleCode: "system_integration", ScopeType: "APPLICATION"}}}
	input := `{"contract_id":"canonical","contract_version":"2","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","services":[{"source_id":"S1","site":"上海","batch":"B1","category":"等保","test_mode":"STANDARD"}]}`
	for _, tc := range []struct {
		name       string
		verifier   platform.ApprovedContractVerifier
		wantStatus int
	}{
		{"no authority", nil, http.StatusServiceUnavailable},
		{"authority maps number", approvedContractVerifier{}, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &repo{}
			h := httpapi.NewRouter(&application.Service{Repo: r, Contracts: tc.verifier}, identity{p: p}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			res := perform(h, http.MethodPost, "/api/v1/contracts/activate", input)
			if res.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
			if tc.wantStatus == http.StatusServiceUnavailable && len(r.projects) != 0 {
				t.Fatal("unresolved identity persisted")
			}
			if tc.wantStatus == http.StatusCreated && (len(r.projects) != 1 || r.projects[0].Contract != "HT-1" || r.projects[0].ContractID != "canonical") {
				t.Fatalf("wrong contract snapshot: %+v", r.projects)
			}
		})
	}
}

func TestSupplementCatalogIncludesAutoCreatedContractWithAdjustmentPermission(t *testing.T) {
	r := &repo{projects: []domain.Project{
		{ID: "PJ-1", TenantID: "tenant-1", Contract: "HT-ORIGINAL", CustomerID: "customer"},
		{ID: "PJ-2", TenantID: "tenant-1", ContractID: "supplement", Contract: "HT-SUP"},
	}}
	p := platform.Principal{TenantID: "tenant-1", UserID: "adjuster", Permissions: map[string]bool{"project.decomposition.manage": true}, DataScopes: []platform.DataScope{{RoleCode: "business_admin", ScopeType: "APPLICATION"}}}
	h := httpapi.NewRouter(&application.Service{Repo: r, Contracts: supplementCatalogVerifier{}}, identity{p: p}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(h, http.MethodGet, "/api/v1/projects/PJ-1/supplement-contracts?limit=200", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data []platform.ApprovedContract `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 || body.Data[0].ID != "supplement" || body.Data[1].ID != "approved" {
		t.Fatalf("wrong eligibility: %+v", body.Data)
	}
	for _, limit := range []string{"0", "201", "bad"} {
		res := perform(h, http.MethodGet, "/api/v1/projects/PJ-1/supplement-contracts?limit="+limit, "")
		if res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("limit=%s status=%d", limit, res.Code)
		}
	}
	res := perform(h, http.MethodGet, "/api/v1/projects/missing/supplement-contracts", "")
	if res.Code != http.StatusNotFound {
		t.Fatalf("missing project status=%d", res.Code)
	}
	res = perform(router(t, map[string]bool{"project.create": true}, nil), http.MethodGet, "/api/v1/projects/PJ-1/supplement-contracts", "")
	if res.Code != http.StatusForbidden {
		t.Fatalf("create-only status=%d", res.Code)
	}
}
