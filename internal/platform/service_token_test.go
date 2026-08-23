package platform

import (
	"errors"
	"testing"
)

func TestClientCredentialsClaimsRequireAccessTokenContract(t *testing.T) {
	verifier := &keycloakClientCredentialsVerifier{
		clientID:              "contract_management-integration",
		audience:              "project_management-internal",
		tenantID:              "tenant-1",
		callerApplicationCode: "contract_management",
		callerEnvironmentCode: "prod",
	}

	valid := serviceTokenClaims{
		AuthorizedParty: "contract_management-integration",
		Type:            "Bearer",
		TokenUse:        "access_token",
		TenantID:        "tenant-1",
		ApplicationCode: "contract_management",
		EnvironmentCode: "prod",
	}
	if err := verifier.validateClaims(valid, []string{"account", "project_management-internal"}); err != nil {
		t.Fatalf("valid Keycloak client_credentials claims rejected: %v", err)
	}
	keycloakDefault := serviceTokenClaims{
		AuthorizedParty: "contract_management-integration",
		ClientID:        "contract_management-integration",
		Type:            "Bearer",
	}
	if err := verifier.validateClaims(keycloakDefault, []string{"account", "project_management-internal"}); err != nil {
		t.Fatalf("default Keycloak service-account claims rejected: %v", err)
	}

	cases := []struct {
		name      string
		claims    serviceTokenClaims
		audiences []string
	}{
		{name: "wrong token use", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, Type: valid.Type, TokenUse: "id_token"}, audiences: []string{"project_management-internal"}},
		{name: "wrong client id", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, ClientID: "another-client", Type: valid.Type}, audiences: []string{"project_management-internal"}},
		{name: "missing azp", claims: serviceTokenClaims{Type: valid.Type, TokenUse: valid.TokenUse}, audiences: []string{"project_management-internal"}},
		{name: "wrong azp", claims: serviceTokenClaims{AuthorizedParty: "another-client", Type: valid.Type, TokenUse: valid.TokenUse}, audiences: []string{"project_management-internal"}},
		{name: "missing audience", claims: valid, audiences: []string{"account"}},
		{name: "id token type", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, Type: "ID", TokenUse: valid.TokenUse}, audiences: []string{"project_management-internal"}},
		{name: "wrong tenant", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, Type: valid.Type, TokenUse: valid.TokenUse, TenantID: "other", ApplicationCode: valid.ApplicationCode, EnvironmentCode: valid.EnvironmentCode}, audiences: []string{"project_management-internal"}},
		{name: "wrong application", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, Type: valid.Type, TokenUse: valid.TokenUse, TenantID: valid.TenantID, ApplicationCode: "other", EnvironmentCode: valid.EnvironmentCode}, audiences: []string{"project_management-internal"}},
		{name: "wrong environment", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, Type: valid.Type, TokenUse: valid.TokenUse, TenantID: valid.TenantID, ApplicationCode: valid.ApplicationCode, EnvironmentCode: "dev"}, audiences: []string{"project_management-internal"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifier.validateClaims(tc.claims, tc.audiences)
			if !errors.Is(err, ErrInvalidServiceToken) {
				t.Fatalf("expected ErrInvalidServiceToken, got %v", err)
			}
		})
	}
}
