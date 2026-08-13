package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func validCompactClaims() oidcClaims {
	return oidcClaims{Subject: "identity-1", IdentityID: "identity-1", TenantID: "tenant-1", PersonID: "person-1", Nonce: "nonce-1", TokenUse: "id_token", Name: "项目用户"}
}

func TestValidateCompactIDTokenClaims(t *testing.T) {
	claims := validCompactClaims()
	claims.IdentityID = ""
	if _, err := validateCompactIDTokenClaims(claims, "nonce-1", "tenant-1"); err == nil {
		t.Fatal("missing identity_id was accepted")
	}
}

func TestValidateCompactIDTokenClaimsRejectsSecurityMismatch(t *testing.T) {
	tests := map[string]func(*oidcClaims){
		"identity":  func(value *oidcClaims) { value.IdentityID = "" },
		"tenant":    func(value *oidcClaims) { value.TenantID = "tenant-2" },
		"nonce":     func(value *oidcClaims) { value.Nonce = "nonce-2" },
		"token use": func(value *oidcClaims) { value.TokenUse = "access_token" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			claims := validCompactClaims()
			mutate(&claims)
			if _, err := validateCompactIDTokenClaims(claims, "nonce-1", "tenant-1"); err == nil {
				t.Fatal("invalid compact identity was accepted")
			}
		})
	}
}

func validAuthorization(scope DataScope) AuthorizationContext {
	return AuthorizationContext{Subject: "identity-1", IdentityID: "identity-1", TenantID: "tenant-1", PersonID: "person-1", ClientID: "project_management-dev-web", ApplicationCode: "project_management", EnvironmentCode: "dev", Roles: []string{"project_manager"}, Permissions: []string{"project.read"}, DataScopes: []DataScope{scope}, AuthorizationRevision: 7}
}

func TestAuthorizationCatalogAcceptsPlatformScopeContract(t *testing.T) {
	catalog, err := LoadAuthorizationCatalog()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		scope DataScope
	}{
		{"application empty scope id", DataScope{RoleCode: "project_manager", ScopeType: "APPLICATION"}},
		{"environment internal id", DataScope{RoleCode: "project_manager", ScopeType: "ENVIRONMENT", ScopeID: "env-internal-id", EnvironmentCode: "dev"}},
		{"tenant", DataScope{RoleCode: "project_manager", ScopeType: "TENANT", ScopeID: "tenant-1"}},
		{"organization", DataScope{RoleCode: "project_manager", ScopeType: "ORG", ScopeID: "org-1", EnvironmentCode: "dev"}},
		{"self", DataScope{RoleCode: "project_manager", ScopeType: "SELF", ScopeID: "identity-1"}},
		{"project", DataScope{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := catalog.Validate(validAuthorization(test.scope), "project_management-dev-web", "project_management", "dev"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplicationAndEnvironmentScopesAllowAll(t *testing.T) {
	for _, scope := range []DataScope{{RoleCode: "project_manager", ScopeType: "APPLICATION"}, {RoleCode: "project_manager", ScopeType: "ENVIRONMENT", ScopeID: "env-1", EnvironmentCode: "dev"}, {RoleCode: "project_manager", ScopeType: "TENANT"}} {
		principal := Principal{TenantID: "tenant-1", IdentityID: "identity-1", UserID: "identity-1", DataScopes: []DataScope{scope}}
		filter, err := principal.ProjectScopeFilter()
		if err != nil || !filter.AllowAll {
			t.Fatalf("scope=%+v filter=%+v error=%v", scope, filter, err)
		}
	}
}

func TestFineGrainedScopesBuildFilter(t *testing.T) {
	principal := Principal{TenantID: "tenant-1", IdentityID: "identity-1", UserID: "identity-1", DataScopes: []DataScope{
		{RoleCode: "project_manager", ScopeType: "SELF", ScopeID: "identity-1"},
		{RoleCode: "project_manager", ScopeType: "ORG", ScopeID: "org-1"},
		{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-1"},
	}}
	filter, err := principal.ProjectScopeFilter()
	if err != nil || filter.AllowAll || !filter.AllowSelf || len(filter.OrganizationIDs) != 1 || len(filter.ProjectIDs) != 1 {
		t.Fatalf("filter=%+v error=%v", filter, err)
	}
}

func TestAuthorizationCatalogRejectsInvalidBindingAndScopes(t *testing.T) {
	catalog, _ := LoadAuthorizationCatalog()
	tests := map[string]AuthorizationContext{
		"client":      validAuthorization(DataScope{RoleCode: "project_manager", ScopeType: "APPLICATION"}),
		"environment": validAuthorization(DataScope{RoleCode: "project_manager", ScopeType: "ENVIRONMENT", ScopeID: "env-1", EnvironmentCode: "prod"}),
		"unknown":     validAuthorization(DataScope{RoleCode: "project_manager", ScopeType: "CUSTOMER", ScopeID: "customer-1"}),
		"empty org":   validAuthorization(DataScope{RoleCode: "project_manager", ScopeType: "ORG"}),
		"wildcard":    validAuthorization(DataScope{RoleCode: "project_manager", ScopeType: "APPLICATION"}),
	}
	client := tests["client"]
	client.ClientID = "other-client"
	tests["client"] = client
	wildcard := tests["wildcard"]
	wildcard.Permissions = []string{"all"}
	tests["wildcard"] = wildcard
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if err := catalog.Validate(value, "project_management-dev-web", "project_management", "dev"); err == nil {
				t.Fatal("invalid authorization context was accepted")
			}
		})
	}
}

func TestAuthorizationContextClientClassifiesResponses(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{http.StatusUnauthorized, ErrUnauthenticated}, {http.StatusForbidden, ErrAuthorizationDenied}, {http.StatusInternalServerError, ErrAuthorizationUnavailable}} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status) }))
			defer server.Close()
			_, err := NewHTTPAuthorizationContextClient(server.URL, server.Client()).Resolve(context.Background(), "access-token")
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestAuthorizationContextClientDecodesMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("authorization header=%q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(validAuthorization(DataScope{RoleCode: "project_manager", ScopeType: "APPLICATION"}))
	}))
	defer server.Close()
	value, err := NewHTTPAuthorizationContextClient(server.URL, server.Client()).Resolve(context.Background(), "access-token")
	if err != nil || value.ClientID != "project_management-dev-web" || value.ApplicationCode != "project_management" || value.EnvironmentCode != "dev" {
		t.Fatalf("value=%+v error=%v", value, err)
	}
}

type fakeOIDCStore struct {
	login      LoginTransaction
	session    StoredOIDCSession
	revoked    bool
	revokedAll bool
	updated    bool
}

func (s *fakeOIDCStore) SaveLoginTransaction(_ context.Context, value LoginTransaction) error {
	s.login = value
	return nil
}
func (s *fakeOIDCStore) ConsumeLoginTransaction(context.Context, []byte, time.Time) (LoginTransaction, error) {
	return LoginTransaction{}, errOIDCRecordNotFound
}
func (s *fakeOIDCStore) CreateSession(context.Context, StoredOIDCSession) error { return nil }
func (s *fakeOIDCStore) FindSession(context.Context, []byte, time.Time) (StoredOIDCSession, error) {
	if s.revoked || s.revokedAll {
		return StoredOIDCSession{}, errOIDCRecordNotFound
	}
	return s.session, nil
}
func (s *fakeOIDCStore) UpdateSession(_ context.Context, value StoredOIDCSession, _ time.Time) error {
	s.updated, s.session = true, value
	return nil
}
func (s *fakeOIDCStore) RevokeSession(context.Context, []byte, time.Time) error {
	s.revoked = true
	return nil
}
func (s *fakeOIDCStore) RevokeSessionsForIdentity(context.Context, string, string, time.Time) error {
	s.revokedAll = true
	return nil
}

func storedPrincipal(t *testing.T, now time.Time) (StoredOIDCSession, Principal) {
	t.Helper()
	principal := Principal{TenantID: "tenant-1", IdentityID: "identity-1", UserID: "identity-1", Roles: []string{"project_manager"}, Permissions: map[string]bool{"project.read": true}, DataScopes: []DataScope{{RoleCode: "project_manager", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}
	encoded, err := json.Marshal(principal)
	if err != nil {
		t.Fatal(err)
	}
	return StoredOIDCSession{TenantID: "tenant-1", IdentityID: "identity-1", PrincipalJSON: encoded, AuthorizationCheckedAt: now.Add(-2 * time.Minute), TokenExpiresAt: now.Add(time.Hour), SessionExpiresAt: now.Add(time.Hour)}, principal
}

func TestAuthorizationDenialRevokesAllIdentitySessions(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	stored, _ := storedPrincipal(t, now)
	store := &fakeOIDCStore{session: stored}
	auth := &OIDCAuthenticator{options: OIDCOptions{TenantID: "tenant-1", SessionCookieName: "project_session", AuthorizationRefreshInterval: time.Minute, AuthorizationMaxStale: 5 * time.Minute}, store: store, now: func() time.Time { return now }, refreshAuthorization: func(context.Context, StoredOIDCSession, time.Time, string) (StoredOIDCSession, Principal, error) {
		return StoredOIDCSession{}, Principal{}, ErrAuthorizationDenied
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	request.AddCookie(auth.cookie("session-1", now.Add(time.Hour)))
	if _, err := auth.Authenticate(context.Background(), request); !errors.Is(err, ErrAuthorizationDenied) || !store.revokedAll || store.revoked {
		t.Fatalf("error=%v revokedAll=%v revoked=%v", err, store.revokedAll, store.revoked)
	}
}

func TestAuthorizationRefreshPersistsNewRevisionAcrossAuthenticatorRestart(t *testing.T) {
	now := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	stored, _ := storedPrincipal(t, now)
	stored.SessionIDHash = tokenDigest("shared-browser-session")
	store := &fakeOIDCStore{session: stored}
	updatedPrincipal := Principal{TenantID: "tenant-1", IdentityID: "identity-1", UserID: "identity-1", Roles: []string{"project_manager"}, Permissions: map[string]bool{"project.create": true}, DataScopes: []DataScope{{RoleCode: "project_manager", ScopeType: "PROJECT", ScopeID: "PJ-2"}}, AuthorizationRevision: 8, CatalogVersion: "2"}
	updatedJSON, err := json.Marshal(updatedPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	first := &OIDCAuthenticator{
		options: OIDCOptions{TenantID: "tenant-1", SessionCookieName: "project_session", AuthorizationRefreshInterval: time.Minute, AuthorizationMaxStale: 5 * time.Minute},
		store:   store, now: func() time.Time { return now },
		refreshAuthorization: func(_ context.Context, value StoredOIDCSession, _ time.Time, _ string) (StoredOIDCSession, Principal, error) {
			value.PrincipalJSON = updatedJSON
			value.AuthorizationRevision = updatedPrincipal.AuthorizationRevision
			value.AuthorizationCheckedAt = now
			return value, updatedPrincipal, nil
		},
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	request.AddCookie(first.cookie("shared-browser-session", now.Add(time.Hour)))
	principal, err := first.Authenticate(context.Background(), request)
	if err != nil || principal.AuthorizationRevision != 8 || !principal.Has("project.create") || principal.Has("project.read") || !store.updated || store.session.AuthorizationRevision != 8 {
		t.Fatalf("principal=%+v error=%v stored=%+v updated=%v", principal, err, store.session, store.updated)
	}

	// A new authenticator represents a restarted or different API instance. It
	// reads the same persistent store rather than an in-process session cache.
	second := &OIDCAuthenticator{options: first.options, store: store, now: func() time.Time { return now.Add(30 * time.Second) }}
	principal, err = second.Authenticate(context.Background(), request)
	if err != nil || principal.AuthorizationRevision != 8 || !principal.Has("project.create") || principal.Has("project.read") {
		t.Fatalf("restarted instance principal=%+v error=%v", principal, err)
	}
}

func TestRevocationIsVisibleToAnotherAuthenticatorInstance(t *testing.T) {
	now := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	stored, _ := storedPrincipal(t, now)
	store := &fakeOIDCStore{session: stored}
	first := &OIDCAuthenticator{options: OIDCOptions{TenantID: "tenant-1", SessionCookieName: "project_session"}, store: store, now: func() time.Time { return now }}
	second := &OIDCAuthenticator{options: first.options, store: store, now: first.now}
	if err := first.store.RevokeSessionsForIdentity(context.Background(), "tenant-1", "identity-1", now); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	request.AddCookie(second.cookie("shared-browser-session", now.Add(time.Hour)))
	if _, err := second.Authenticate(context.Background(), request); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked session error=%v", err)
	}
}

func TestAuthorizationOutageAllowsOnlyFreshReadSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	stored, expected := storedPrincipal(t, now)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			store := &fakeOIDCStore{session: stored}
			auth := &OIDCAuthenticator{options: OIDCOptions{TenantID: "tenant-1", SessionCookieName: "project_session", AuthorizationRefreshInterval: time.Minute, AuthorizationMaxStale: 5 * time.Minute}, store: store, now: func() time.Time { return now }, refreshAuthorization: func(context.Context, StoredOIDCSession, time.Time, string) (StoredOIDCSession, Principal, error) {
				return StoredOIDCSession{}, Principal{}, ErrAuthorizationUnavailable
			}}
			request := httptest.NewRequest(method, "/api/v1/projects", nil)
			request.AddCookie(auth.cookie("session-1", now.Add(time.Hour)))
			principal, err := auth.Authenticate(context.Background(), request)
			if method == http.MethodGet {
				if err != nil || principal.IdentityID != expected.IdentityID || !store.updated {
					t.Fatalf("principal=%+v error=%v updated=%v", principal, err, store.updated)
				}
			} else if !errors.Is(err, ErrAuthorizationUnavailable) {
				t.Fatalf("write error=%v", err)
			}
		})
	}
}

func TestLoginPersistsOnlyStateDigestAndEncryptedSecrets(t *testing.T) {
	codec, err := newSecretCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeOIDCStore{}
	auth := &OIDCAuthenticator{options: OIDCOptions{TenantID: "tenant-1"}, store: store, codec: codec, now: time.Now, oauth: oauth2.Config{ClientID: "project_management-dev-web", RedirectURL: "http://localhost/callback", Endpoint: oauth2.Endpoint{AuthURL: "http://keycloak/authorize"}}}
	request := httptest.NewRequest(http.MethodGet, "/auth/login?return_to=/projects/PJ-1", nil)
	response := httptest.NewRecorder()
	auth.Login(response, request)
	if response.Code != http.StatusFound || len(store.login.StateHash) != 32 || string(store.login.StateHash) == response.Header().Get("Location") || store.login.ReturnPath != "/projects/PJ-1" {
		t.Fatalf("status=%d login=%+v location=%s", response.Code, store.login, response.Header().Get("Location"))
	}
	if len(store.login.NonceCipher) == 0 || len(store.login.CodeVerifierCipher) == 0 {
		t.Fatal("nonce or verifier ciphertext is empty")
	}
}

func TestLogoutUsesDiscoveredEndSessionEndpoint(t *testing.T) {
	codec, err := newSecretCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	idCipher, err := codec.encrypt([]byte("id-token"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := &fakeOIDCStore{session: StoredOIDCSession{TenantID: "tenant-1", IdentityID: "identity-1", IDTokenCipher: idCipher, SessionExpiresAt: now.Add(time.Hour)}}
	auth := &OIDCAuthenticator{options: OIDCOptions{TenantID: "tenant-1", ClientID: "project_management-dev-web", SessionCookieName: "project_session", PostLogoutRedirectURI: "http://localhost/logged-out"}, store: store, codec: codec, endSessionEndpoint: "http://keycloak/realms/basic-platform/protocol/openid-connect/logout", now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	request.AddCookie(auth.cookie("session-1", now.Add(time.Hour)))
	response := httptest.NewRecorder()
	auth.Logout(response, request)
	location := response.Header().Get("Location")
	for _, expected := range []string{"/protocol/openid-connect/logout", "client_id=project_management-dev-web", "id_token_hint=id-token", "post_logout_redirect_uri="} {
		if !strings.Contains(location, expected) {
			t.Fatalf("logout location missing %q: %s", expected, location)
		}
	}
	if !store.revoked {
		t.Fatal("local session was not revoked")
	}
}
