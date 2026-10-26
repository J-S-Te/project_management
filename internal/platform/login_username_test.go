package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginUsernameIsSeparateFromDisplayNameAndSurvivesSessionJSON(t *testing.T) {
	claims := validCompactClaims()
	claims.PreferredUsername = " alice "
	identity, err := validateCompactIDTokenClaims(claims, "nonce-1", "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Username != "alice" || identity.DisplayName != "项目用户" {
		t.Fatalf("identity=%#v", identity)
	}
	data, err := json.Marshal(Principal{IdentityID: "identity-1", UserID: "identity-1", TenantID: "tenant-1", AuthorizationRevision: 1, Username: identity.Username, DisplayName: identity.DisplayName})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := decodePrincipal(data)
	if err != nil || principal.Username != "alice" || principal.DisplayName != "项目用户" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}

// This unit-test KeySet isolates profile binding from cryptographic verification,
// and does not assert end-to-end cryptographic verification.
type usernameTestKeySet struct{ payload []byte }

func (k usernameTestKeySet) VerifySignature(context.Context, string) ([]byte, error) {
	return k.payload, nil
}

func TestCompactIDTokenUsernameFallsBackOnlyToMatchingUserInfo(t *testing.T) {
	for _, tc := range []struct {
		name, subject, username string
		status                  int
		wantError               bool
	}{
		{"matching", "identity-1", " alice ", 200, false},
		{"different subject", "someone-else", "alice", 200, true},
		{"unavailable", "identity-1", "alice", 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var issuer string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/.well-known/openid-configuration" {
					_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "userinfo_endpoint": issuer + "/userinfo"})
					return
				}
				if r.Header.Get("Authorization") != "Bearer test-access" {
					t.Error("missing access-token binding")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"sub": tc.subject, "preferred_username": tc.username})
			}))
			defer server.Close()
			issuer = server.URL
			provider, err := oidc.NewProvider(context.Background(), issuer)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"iss": issuer, "aud": "client", "exp": time.Now().Add(time.Hour).Unix(), "sub": "identity-1", "identity_id": "identity-1", "tenant_id": "tenant-1", "nonce": "nonce-1", "token_use": "id_token"})
			raw := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln"
			verifier := oidc.NewVerifier(issuer, usernameTestKeySet{payload}, &oidc.Config{ClientID: "client"})
			authenticator := &OIDCAuthenticator{provider: provider, verifier: verifier, httpClient: server.Client(), options: OIDCOptions{TenantID: "tenant-1"}}
			token := (&oauth2.Token{AccessToken: "test-access"}).WithExtra(map[string]any{"id_token": raw})
			_, _, _, identity, err := authenticator.verifyIDToken(context.Background(), token, "nonce-1")
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if err == nil && identity.Username != "alice" {
				t.Fatalf("username=%q", identity.Username)
			}
		})
	}
}
