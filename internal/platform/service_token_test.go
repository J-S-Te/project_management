package platform

import (
	"errors"
	"testing"
)

func TestClientCredentialsClaimsRequireAccessTokenContract(t *testing.T) {
	verifier := &keycloakClientCredentialsVerifier{
		clientID: "contract_management-integration",
		audience: "project_management-internal",
	}

	valid := serviceTokenClaims{
		AuthorizedParty: "contract_management-integration",
		Type:            "Bearer",
		TokenUse:        "access_token",
	}
	if err := verifier.validateClaims(valid, []string{"account", "project_management-internal"}); err != nil {
		t.Fatalf("valid Keycloak client_credentials claims rejected: %v", err)
	}

	cases := []struct {
		name      string
		claims    serviceTokenClaims
		audiences []string
	}{
		{name: "missing token use", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, Type: valid.Type}, audiences: []string{"project_management-internal"}},
		{name: "wrong token use", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, Type: valid.Type, TokenUse: "id_token"}, audiences: []string{"project_management-internal"}},
		{name: "missing azp", claims: serviceTokenClaims{Type: valid.Type, TokenUse: valid.TokenUse}, audiences: []string{"project_management-internal"}},
		{name: "wrong azp", claims: serviceTokenClaims{AuthorizedParty: "another-client", Type: valid.Type, TokenUse: valid.TokenUse}, audiences: []string{"project_management-internal"}},
		{name: "missing audience", claims: valid, audiences: []string{"account"}},
		{name: "id token type", claims: serviceTokenClaims{AuthorizedParty: valid.AuthorizedParty, Type: "ID", TokenUse: valid.TokenUse}, audiences: []string{"project_management-internal"}},
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
