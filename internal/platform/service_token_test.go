package platform

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// writeApplicationPublicKey 为测试生成一个只读 Ed25519 PKIX 公钥文件。
func writeApplicationPublicKey(t *testing.T, publicKey ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "application-jwt-public.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// settlementReferenceClaims 返回一条合法的结算项目引用 Application JWT 载荷。
func settlementReferenceClaims(now time.Time) platformApplicationTokenClaims {
	return platformApplicationTokenClaims{
		Issuer: "basic-platform", Audience: "basic-platform-application", TokenUse: "application",
		Subject: "settlement-prod-project-reference", OAuthClientID: "oauth-client-1", TenantID: "tenant-1",
		ApplicationCode: "settlement", EnvironmentCode: "prod", Scopes: []string{"settlement.project_reference.read"},
		IssuedAt: now.Add(-time.Second).Unix(), NotBefore: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(time.Minute).Unix(),
	}
}

// TestSettlementReferenceVerifierAllowsAnyClientSubject 验证解耦后项目按平台应用授权：
// 任意合法结算机器主体都通过，但 issuer/audience/应用/环境/scope/oauth_client_id 仍全绑定。
func TestSettlementReferenceVerifierAllowsAnyClientSubject(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewClientCredentialsTokenVerifier(context.Background(), ClientCredentialsVerifierOptions{
		Issuer: "basic-platform", Audience: "basic-platform-application", PublicKeyPath: writeApplicationPublicKey(t, publicKey),
		TenantID: "tenant-1", CallerApplicationCode: "settlement", CallerEnvironmentCode: "prod",
		RequiredScope: "settlement.project_reference.read", AllowAnyClientSubject: true,
	})
	if err != nil {
		t.Fatalf("allow-any verifier requires no client id: %v", err)
	}
	now := time.Now().UTC()
	for _, subject := range []string{"settlement-prod-project-reference", "settlement-prod-project-reference-read", "another-settlement-client"} {
		claims := settlementReferenceClaims(now)
		claims.Subject = subject
		if _, err := verifier.VerifyClientCredentials(context.Background(), signPlatformToken(t, privateKey, claims)); err != nil {
			t.Fatalf("valid settlement subject %q rejected: %v", subject, err)
		}
	}
}

// TestSettlementReferenceVerifierRejectsUntrustedClaims 验证去掉主体固定后其余信任绑定不放宽。
func TestSettlementReferenceVerifierRejectsUntrustedClaims(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewClientCredentialsTokenVerifier(context.Background(), ClientCredentialsVerifierOptions{
		Issuer: "basic-platform", Audience: "basic-platform-application", PublicKeyPath: writeApplicationPublicKey(t, publicKey),
		TenantID: "tenant-1", CallerApplicationCode: "settlement", CallerEnvironmentCode: "prod",
		RequiredScope: "settlement.project_reference.read", AllowAnyClientSubject: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, scenario := range []string{"application", "environment", "scope", "extra-scope", "audience", "issuer", "token-use", "expired", "missing-oauth-client-id", "wrong-tenant"} {
		claims := settlementReferenceClaims(now)
		switch scenario {
		case "application":
			claims.ApplicationCode = "contract_management"
		case "environment":
			claims.EnvironmentCode = "dev"
		case "scope":
			claims.Scopes = []string{"settlement.contract.read"}
		case "extra-scope":
			claims.Scopes = []string{"settlement.project_reference.read", "dashboard.project.read"}
		case "audience":
			claims.Audience = "settlement-api"
		case "issuer":
			claims.Issuer = "another-platform"
		case "token-use":
			claims.TokenUse = "access_token"
		case "expired":
			claims.IssuedAt = now.Add(-2 * time.Hour).Unix()
			claims.NotBefore = claims.IssuedAt
			claims.ExpiresAt = now.Add(-time.Hour).Unix()
		case "missing-oauth-client-id":
			claims.OAuthClientID = ""
		case "wrong-tenant":
			claims.TenantID = "tenant-2"
		}
		t.Run(scenario, func(t *testing.T) {
			if _, err := verifier.VerifyClientCredentials(context.Background(), signPlatformToken(t, privateKey, claims)); !errors.Is(err, ErrInvalidServiceToken) {
				t.Fatalf("untrusted claims accepted: %v", err)
			}
		})
	}
}

// TestSettlementReferenceVerifierKeepsPinnedClientByDefault 验证未开启解耦时仍要求固定主体。
func TestSettlementReferenceVerifierKeepsPinnedClientByDefault(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicPath := writeApplicationPublicKey(t, publicKey)
	options := ClientCredentialsVerifierOptions{
		Issuer: "basic-platform", Audience: "basic-platform-application", PublicKeyPath: publicPath,
		TenantID: "tenant-1", CallerApplicationCode: "settlement", CallerEnvironmentCode: "prod",
		RequiredScope: "settlement.project_reference.read",
	}
	if _, err := NewClientCredentialsTokenVerifier(context.Background(), options); err == nil || !strings.Contains(err.Error(), "trust binding is incomplete") {
		t.Fatalf("missing client id accepted without allow-any: %v", err)
	}
	options.ClientID = "settlement-prod-project-reference"
	verifier, err := NewClientCredentialsTokenVerifier(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := verifier.VerifyClientCredentials(context.Background(), signPlatformToken(t, privateKey, settlementReferenceClaims(now))); err != nil {
		t.Fatalf("pinned subject rejected: %v", err)
	}
	other := settlementReferenceClaims(now)
	other.Subject = "another-settlement-client"
	if _, err := verifier.VerifyClientCredentials(context.Background(), signPlatformToken(t, privateKey, other)); !errors.Is(err, ErrInvalidServiceToken) {
		t.Fatal("unbound subject accepted while subject binding is required")
	}
}
