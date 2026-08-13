package platform

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var ErrUnauthenticated = errors.New("unauthenticated")

const (
	loginTransactionTTL               = 10 * time.Minute
	authorizationRefreshRetryInterval = 5 * time.Second
)

type OIDCOptions struct {
	Issuer, BackchannelBaseURL, PlatformBaseURL, ClientID, ClientSecret, RedirectURI, PostLogoutRedirectURI string
	TenantID, ApplicationCode, EnvironmentCode, SessionCookieName, PathPrefix                               string
	SessionTTL, AuthorizationRefreshInterval, AuthorizationMaxStale, AuthorizationTimeout                   time.Duration
	SessionSecure                                                                                           bool
}

type oidcClaims struct {
	Subject           string `json:"sub"`
	IdentityID        string `json:"identity_id"`
	TenantID          string `json:"tenant_id"`
	PersonID          string `json:"person_id"`
	Nonce             string `json:"nonce"`
	TokenUse          string `json:"token_use"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
}

type compactIdentity struct {
	Subject, IdentityID, TenantID, PersonID, DisplayName string
}

type OIDCAuthenticator struct {
	options              OIDCOptions
	provider             *oidc.Provider
	verifier             *oidc.IDTokenVerifier
	oauth                oauth2.Config
	httpClient           *http.Client
	store                OIDCStore
	authorization        AuthorizationContextClient
	catalog              AuthorizationCatalog
	codec                *secretCodec
	endSessionEndpoint   string
	now                  func() time.Time
	refreshAuthorization func(context.Context, StoredOIDCSession, time.Time, string) (StoredOIDCSession, Principal, error)
}

func NewOIDCAuthenticator(ctx context.Context, options OIDCOptions, store OIDCStore, encryptionKey []byte) (*OIDCAuthenticator, error) {
	if store == nil {
		return nil, errors.New("OIDC persistent store is required")
	}
	codec, err := newSecretCodec(encryptionKey)
	if err != nil {
		return nil, err
	}
	catalog, err := LoadAuthorizationCatalog()
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: options.AuthorizationTimeout}
	if options.BackchannelBaseURL != "" {
		publicURL, parseErr := url.Parse(strings.TrimRight(options.Issuer, "/"))
		if parseErr != nil {
			return nil, parseErr
		}
		backchannelURL, parseErr := url.Parse(strings.TrimRight(options.BackchannelBaseURL, "/"))
		if parseErr != nil {
			return nil, parseErr
		}
		client.Transport = &backchannelTransport{base: http.DefaultTransport, public: publicURL, backchannel: backchannelURL}
	}
	oidcContext := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(oidcContext, strings.TrimRight(options.Issuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("load OIDC discovery: %w", err)
	}
	var discovery struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&discovery); err != nil {
		return nil, fmt.Errorf("decode OIDC discovery: %w", err)
	}
	return &OIDCAuthenticator{
		options: options, provider: provider, verifier: provider.Verifier(&oidc.Config{ClientID: options.ClientID}), httpClient: client,
		store: store, authorization: NewHTTPAuthorizationContextClient(options.PlatformBaseURL, client), catalog: catalog, codec: codec,
		endSessionEndpoint: strings.TrimSpace(discovery.EndSessionEndpoint), now: time.Now,
		oauth: oauth2.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, RedirectURL: options.RedirectURI, Endpoint: provider.Endpoint(), Scopes: []string{"openid", "profile"}},
	}, nil
}

func (a *OIDCAuthenticator) Authenticate(ctx context.Context, request *http.Request) (Principal, error) {
	cookie, err := request.Cookie(a.options.SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return Principal{}, ErrUnauthenticated
	}
	now := a.now().UTC()
	hash := tokenDigest(cookie.Value)
	stored, err := a.store.FindSession(ctx, hash, now)
	if err != nil || stored.TenantID != a.options.TenantID {
		return Principal{}, ErrUnauthenticated
	}
	principal, err := decodePrincipal(stored.PrincipalJSON)
	if err != nil || principal.IdentityID != stored.IdentityID || principal.TenantID != stored.TenantID {
		_ = a.store.RevokeSession(ctx, hash, now)
		return Principal{}, ErrUnauthenticated
	}
	refreshDue := !stored.TokenExpiresAt.After(now) || now.Sub(stored.AuthorizationCheckedAt) >= a.options.AuthorizationRefreshInterval
	if refreshDue {
		if stored.RefreshRetryAt.After(now) {
			if staleReadAllowed(request.Method, now, stored.AuthorizationCheckedAt, a.options.AuthorizationMaxStale, stored.TokenExpiresAt) {
				return principal, nil
			}
			return Principal{}, ErrAuthorizationUnavailable
		}
		refresh := a.refreshAuthorization
		if refresh == nil {
			refresh = a.refreshStoredSession
		}
		updated, current, refreshErr := refresh(ctx, stored, now, request.Method)
		if refreshErr != nil {
			if errors.Is(refreshErr, ErrAuthorizationUnavailable) && staleReadAllowed(request.Method, now, stored.AuthorizationCheckedAt, a.options.AuthorizationMaxStale, stored.TokenExpiresAt) {
				stored.RefreshRetryAt = now.Add(authorizationRefreshRetryInterval)
				stored.LastSeenAt = now
				_ = a.store.UpdateSession(ctx, stored, now)
				return principal, nil
			}
			if errors.Is(refreshErr, ErrAuthorizationDenied) || errors.Is(refreshErr, ErrInvalidAuthorization) {
				_ = a.store.RevokeSessionsForIdentity(ctx, stored.TenantID, stored.IdentityID, now)
			} else if errors.Is(refreshErr, ErrUnauthenticated) {
				_ = a.store.RevokeSession(ctx, hash, now)
			}
			return Principal{}, refreshErr
		}
		updated.LastSeenAt = now
		if err := a.store.UpdateSession(ctx, updated, now); err != nil {
			return Principal{}, ErrUnauthenticated
		}
		return current, nil
	}
	stored.LastSeenAt = now
	if err := a.store.UpdateSession(ctx, stored, now); err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}

func (a *OIDCAuthenticator) Login(w http.ResponseWriter, r *http.Request) {
	state, err := randomValue(32)
	if err != nil {
		http.Error(w, "cannot start login", http.StatusInternalServerError)
		return
	}
	nonce, err := randomValue(32)
	if err != nil {
		http.Error(w, "cannot start login", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	nonceCipher, err := a.codec.encrypt([]byte(nonce))
	if err != nil {
		http.Error(w, "cannot protect login", http.StatusInternalServerError)
		return
	}
	verifierCipher, err := a.codec.encrypt([]byte(verifier))
	if err != nil {
		http.Error(w, "cannot protect login", http.StatusInternalServerError)
		return
	}
	returnPath := safeReturnPath(r.URL.Query().Get("return_to"))
	now := a.now().UTC()
	transaction := LoginTransaction{StateHash: tokenDigest(state), TenantID: a.options.TenantID, NonceCipher: nonceCipher, CodeVerifierCipher: verifierCipher, ReturnPath: returnPath, ExpiresAt: now.Add(loginTransactionTTL), CreatedAt: now}
	if err := a.store.SaveLoginTransaction(r.Context(), transaction); err != nil {
		http.Error(w, "login service is unavailable", http.StatusServiceUnavailable)
		return
	}
	target := a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *OIDCAuthenticator) Callback(w http.ResponseWriter, r *http.Request) {
	state, code := strings.TrimSpace(r.URL.Query().Get("state")), strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" || r.URL.Query().Get("error") != "" {
		http.Error(w, "OIDC callback is invalid", http.StatusBadRequest)
		return
	}
	now := a.now().UTC()
	tx, err := a.store.ConsumeLoginTransaction(r.Context(), tokenDigest(state), now)
	if err != nil || tx.TenantID != a.options.TenantID {
		http.Error(w, "invalid or expired OIDC state", http.StatusUnauthorized)
		return
	}
	nonce, err := a.codec.decrypt(tx.NonceCipher)
	if err != nil {
		http.Error(w, "OIDC state is invalid", http.StatusUnauthorized)
		return
	}
	verifier, err := a.codec.decrypt(tx.CodeVerifierCipher)
	if err != nil {
		http.Error(w, "OIDC state is invalid", http.StatusUnauthorized)
		return
	}
	ctx := oidc.ClientContext(r.Context(), a.httpClient)
	token, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(string(verifier)))
	if err != nil {
		http.Error(w, "OIDC token exchange failed", http.StatusUnauthorized)
		return
	}
	rawIDToken, idToken, claims, identity, err := a.verifyIDToken(ctx, token, string(nonce))
	if err != nil {
		http.Error(w, "OIDC ID token claims are invalid", http.StatusUnauthorized)
		return
	}
	authorization, token, rawIDToken, idToken, identity, err := a.resolveAuthorization(ctx, token, rawIDToken, idToken, claims, identity, true)
	if err != nil {
		writeAuthorizationError(w, err)
		return
	}
	principal, err := a.principalFromAuthorization(identity, authorization)
	if err != nil {
		writeAuthorizationError(w, err)
		return
	}
	principalJSON, err := json.Marshal(principal)
	if err != nil {
		http.Error(w, "cannot create session", http.StatusInternalServerError)
		return
	}
	tokenJSON, err := json.Marshal(token)
	if err != nil {
		http.Error(w, "cannot create session", http.StatusInternalServerError)
		return
	}
	idTokenCipher, err := a.codec.encrypt([]byte(rawIDToken))
	if err != nil {
		http.Error(w, "cannot create session", http.StatusInternalServerError)
		return
	}
	tokenCipher, err := a.codec.encrypt(tokenJSON)
	if err != nil {
		http.Error(w, "cannot create session", http.StatusInternalServerError)
		return
	}
	rawSession, err := randomValue(48)
	if err != nil {
		http.Error(w, "cannot create session", http.StatusInternalServerError)
		return
	}
	tokenExpiry := earliestTime(idToken.Expiry, token.Expiry)
	if !tokenExpiry.After(now) {
		http.Error(w, "OIDC token is expired", http.StatusUnauthorized)
		return
	}
	expiresAt := now.Add(a.options.SessionTTL)
	stored := StoredOIDCSession{SessionIDHash: tokenDigest(rawSession), TenantID: principal.TenantID, IdentityID: principal.IdentityID, PersonID: principal.PersonID, PrincipalJSON: principalJSON, IDTokenCipher: idTokenCipher, OAuthTokenCipher: tokenCipher, AuthorizationRevision: principal.AuthorizationRevision, AuthorizationCheckedAt: now, TokenExpiresAt: tokenExpiry, SessionExpiresAt: expiresAt, CreatedAt: now, LastSeenAt: now}
	if err := a.store.CreateSession(r.Context(), stored); err != nil {
		http.Error(w, "session service is unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, a.cookie(rawSession, expiresAt))
	http.Redirect(w, r, a.PublicPath(tx.ReturnPath), http.StatusFound)
}

func (a *OIDCAuthenticator) refreshStoredSession(ctx context.Context, stored StoredOIDCSession, now time.Time, method string) (StoredOIDCSession, Principal, error) {
	tokenJSON, err := a.codec.decrypt(stored.OAuthTokenCipher)
	if err != nil {
		return stored, Principal{}, ErrUnauthenticated
	}
	var token oauth2.Token
	if err := json.Unmarshal(tokenJSON, &token); err != nil {
		return stored, Principal{}, ErrUnauthenticated
	}
	idTokenBytes, err := a.codec.decrypt(stored.IDTokenCipher)
	if err != nil {
		return stored, Principal{}, ErrUnauthenticated
	}
	identity := compactIdentity{Subject: stored.IdentityID, IdentityID: stored.IdentityID, TenantID: stored.TenantID, PersonID: stored.PersonID}
	var idToken *oidc.IDToken
	var claims oidcClaims
	if !token.Expiry.After(now) {
		token, idTokenBytes, idToken, claims, identity, err = a.refreshTokens(ctx, &token, identity)
		if err != nil {
			return stored, Principal{}, ErrUnauthenticated
		}
	}
	authorization, tokenPointer, rawIDToken, verifiedIDToken, refreshedIdentity, err := a.resolveAuthorization(ctx, &token, string(idTokenBytes), idToken, claims, identity, true)
	if err != nil {
		if errors.Is(err, ErrAuthorizationUnavailable) && staleReadAllowed(method, now, stored.AuthorizationCheckedAt, a.options.AuthorizationMaxStale, stored.TokenExpiresAt) {
			return stored, Principal{}, err
		}
		return stored, Principal{}, err
	}
	identity = refreshedIdentity
	principal, err := a.principalFromAuthorization(identity, authorization)
	if err != nil {
		return stored, Principal{}, err
	}
	oldPrincipal, decodeErr := decodePrincipal(stored.PrincipalJSON)
	if decodeErr == nil && principal.DisplayName == "" {
		principal.DisplayName = oldPrincipal.DisplayName
	}
	principalJSON, _ := json.Marshal(principal)
	tokenJSON, _ = json.Marshal(tokenPointer)
	stored.PrincipalJSON = principalJSON
	stored.AuthorizationRevision = principal.AuthorizationRevision
	stored.AuthorizationCheckedAt = now
	stored.RefreshRetryAt = time.Time{}
	stored.PersonID = principal.PersonID
	stored.TokenExpiresAt = tokenPointer.Expiry
	if verifiedIDToken != nil {
		stored.TokenExpiresAt = earliestTime(verifiedIDToken.Expiry, tokenPointer.Expiry)
	}
	stored.IDTokenCipher, err = a.codec.encrypt([]byte(rawIDToken))
	if err != nil {
		return stored, Principal{}, ErrUnauthenticated
	}
	stored.OAuthTokenCipher, err = a.codec.encrypt(tokenJSON)
	if err != nil {
		return stored, Principal{}, ErrUnauthenticated
	}
	return stored, principal, nil
}

func (a *OIDCAuthenticator) resolveAuthorization(ctx context.Context, token *oauth2.Token, rawIDToken string, idToken *oidc.IDToken, claims oidcClaims, identity compactIdentity, retry401 bool) (AuthorizationContext, *oauth2.Token, string, *oidc.IDToken, compactIdentity, error) {
	value, err := a.authorization.Resolve(ctx, token.AccessToken)
	if errors.Is(err, ErrUnauthenticated) && retry401 {
		refreshed, refreshedRaw, refreshedID, refreshedClaims, refreshedIdentity, refreshErr := a.refreshTokens(ctx, token, identity)
		if refreshErr != nil {
			return AuthorizationContext{}, token, rawIDToken, idToken, identity, ErrUnauthenticated
		}
		token, rawIDToken, idToken, claims, identity = &refreshed, string(refreshedRaw), refreshedID, refreshedClaims, refreshedIdentity
		value, err = a.authorization.Resolve(ctx, token.AccessToken)
	}
	if err != nil {
		return AuthorizationContext{}, token, rawIDToken, idToken, identity, err
	}
	return value, token, rawIDToken, idToken, identity, nil
}

func (a *OIDCAuthenticator) refreshTokens(ctx context.Context, current *oauth2.Token, expected compactIdentity) (oauth2.Token, []byte, *oidc.IDToken, oidcClaims, compactIdentity, error) {
	if current == nil || strings.TrimSpace(current.RefreshToken) == "" {
		return oauth2.Token{}, nil, nil, oidcClaims{}, compactIdentity{}, errors.New("refresh token is missing")
	}
	seed := &oauth2.Token{RefreshToken: current.RefreshToken, Expiry: a.now().UTC().Add(-time.Second)}
	token, err := a.oauth.TokenSource(oidc.ClientContext(ctx, a.httpClient), seed).Token()
	if err != nil {
		return oauth2.Token{}, nil, nil, oidcClaims{}, compactIdentity{}, err
	}
	rawIDToken, idToken, claims, identity, err := a.verifyIDToken(ctx, token, "")
	if err != nil || identity.Subject != expected.Subject || identity.TenantID != expected.TenantID {
		return oauth2.Token{}, nil, nil, oidcClaims{}, compactIdentity{}, errors.New("refreshed OIDC identity changed")
	}
	return *token, []byte(rawIDToken), idToken, claims, identity, nil
}

func (a *OIDCAuthenticator) verifyIDToken(ctx context.Context, token *oauth2.Token, nonce string) (string, *oidc.IDToken, oidcClaims, compactIdentity, error) {
	raw, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", nil, oidcClaims{}, compactIdentity{}, errors.New("ID token is missing")
	}
	idToken, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return "", nil, oidcClaims{}, compactIdentity{}, err
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return "", nil, oidcClaims{}, compactIdentity{}, err
	}
	identity, err := validateCompactIDTokenClaims(claims, nonce, a.options.TenantID)
	return raw, idToken, claims, identity, err
}

func validateCompactIDTokenClaims(claims oidcClaims, expectedNonce, expectedTenant string) (compactIdentity, error) {
	claims.Subject = strings.TrimSpace(claims.Subject)
	claims.IdentityID = strings.TrimSpace(claims.IdentityID)
	claims.TenantID = strings.TrimSpace(claims.TenantID)
	claims.PersonID = strings.TrimSpace(claims.PersonID)
	if claims.Subject == "" || claims.TenantID != expectedTenant || claims.TokenUse != "id_token" {
		return compactIdentity{}, errors.New("compact identity is incomplete")
	}
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return compactIdentity{}, errors.New("nonce does not match")
	}
	if claims.IdentityID == "" {
		claims.IdentityID = claims.Subject
	}
	if claims.IdentityID != claims.Subject {
		return compactIdentity{}, errors.New("identity_id does not match sub")
	}
	if len(claims.Subject) > 128 || len(claims.PersonID) > 64 {
		return compactIdentity{}, errors.New("identity exceeds storage boundary")
	}
	displayName := strings.TrimSpace(claims.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(claims.PreferredUsername)
	}
	if displayName == "" {
		displayName = claims.Subject
	}
	return compactIdentity{Subject: claims.Subject, IdentityID: claims.IdentityID, TenantID: claims.TenantID, PersonID: claims.PersonID, DisplayName: displayName}, nil
}

func (a *OIDCAuthenticator) principalFromAuthorization(identity compactIdentity, value AuthorizationContext) (Principal, error) {
	if identity.Subject != value.Subject || identity.IdentityID != value.IdentityID || identity.TenantID != value.TenantID || identity.PersonID != "" && value.PersonID != "" && identity.PersonID != value.PersonID {
		return Principal{}, fmt.Errorf("%w: OIDC and authorization identities differ", ErrInvalidAuthorization)
	}
	if err := a.catalog.Validate(value, a.options.ClientID, a.options.ApplicationCode, a.options.EnvironmentCode); err != nil {
		return Principal{}, err
	}
	permissions := make(map[string]bool, len(value.Permissions))
	for _, permission := range value.Permissions {
		permissions[permission] = true
	}
	personID := value.PersonID
	if personID == "" {
		personID = identity.PersonID
	}
	return Principal{TenantID: value.TenantID, IdentityID: value.IdentityID, PersonID: personID, UserID: value.IdentityID, DisplayName: identity.DisplayName, Roles: append([]string(nil), value.Roles...), Permissions: permissions, DataScopes: append([]DataScope(nil), value.DataScopes...), AuthorizationRevision: value.AuthorizationRevision, CatalogVersion: a.catalog.Version}, nil
}

func (a *OIDCAuthenticator) Logout(w http.ResponseWriter, r *http.Request) {
	idToken := a.clear(w, r)
	if a.endSessionEndpoint == "" {
		http.Redirect(w, r, a.PublicPath("/logged-out"), http.StatusFound)
		return
	}
	endpoint, err := url.Parse(a.endSessionEndpoint)
	if err != nil {
		http.Redirect(w, r, a.PublicPath("/logged-out"), http.StatusFound)
		return
	}
	query := endpoint.Query()
	query.Set("client_id", a.options.ClientID)
	if idToken != "" {
		query.Set("id_token_hint", idToken)
	}
	if a.options.PostLogoutRedirectURI != "" {
		query.Set("post_logout_redirect_uri", a.options.PostLogoutRedirectURI)
	}
	endpoint.RawQuery = query.Encode()
	http.Redirect(w, r, endpoint.String(), http.StatusFound)
}

func (a *OIDCAuthenticator) LogoutLocal(w http.ResponseWriter, r *http.Request) {
	a.clear(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *OIDCAuthenticator) clear(w http.ResponseWriter, r *http.Request) string {
	var idToken string
	if cookie, err := r.Cookie(a.options.SessionCookieName); err == nil && cookie.Value != "" {
		now := a.now().UTC()
		hash := tokenDigest(cookie.Value)
		if stored, findErr := a.store.FindSession(r.Context(), hash, now); findErr == nil {
			if plaintext, decryptErr := a.codec.decrypt(stored.IDTokenCipher); decryptErr == nil {
				idToken = string(plaintext)
			}
		}
		_ = a.store.RevokeSession(r.Context(), hash, now)
	}
	expired := a.cookie("", time.Unix(1, 0))
	expired.MaxAge = -1
	http.SetCookie(w, expired)
	return idToken
}

func (a *OIDCAuthenticator) cookie(value string, expires time.Time) *http.Cookie {
	path := a.options.PathPrefix
	if path == "" {
		path = "/"
	}
	return &http.Cookie{Name: a.options.SessionCookieName, Value: value, Path: path, Expires: expires, HttpOnly: true, Secure: a.options.SessionSecure, SameSite: http.SameSiteLaxMode}
}

func (a *OIDCAuthenticator) PublicPath(path string) string {
	prefix := strings.TrimRight(a.options.PathPrefix, "/")
	if path == "/" || path == "" {
		return prefix + "/"
	}
	return prefix + "/" + strings.TrimLeft(path, "/")
}

func safeReturnPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\r\n") {
		return "/"
	}
	return value
}

func staleReadAllowed(method string, now, checkedAt time.Time, maxStale time.Duration, tokenExpiresAt time.Time) bool {
	return (method == http.MethodGet || method == http.MethodHead) && maxStale > 0 && !checkedAt.IsZero() && now.Sub(checkedAt) <= maxStale && tokenExpiresAt.After(now)
}

func decodePrincipal(value []byte) (Principal, error) {
	var principal Principal
	if err := json.Unmarshal(value, &principal); err != nil {
		return Principal{}, err
	}
	if principal.IdentityID == "" || principal.IdentityID != principal.UserID || principal.TenantID == "" || principal.AuthorizationRevision == 0 {
		return Principal{}, errors.New("stored principal is invalid")
	}
	return principal, nil
}

func writeAuthorizationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		http.Error(w, "OIDC access token is invalid", http.StatusUnauthorized)
	case errors.Is(err, ErrAuthorizationDenied), errors.Is(err, ErrInvalidAuthorization):
		http.Error(w, "project application authorization is denied", http.StatusForbidden)
	default:
		http.Error(w, "authorization service is unavailable", http.StatusServiceUnavailable)
	}
}

func earliestTime(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if !value.IsZero() && (result.IsZero() || value.Before(result)) {
			result = value.UTC()
		}
	}
	return result
}

func randomValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

type backchannelTransport struct {
	base                http.RoundTripper
	public, backchannel *url.URL
}

func (t *backchannelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != t.public.Scheme || r.URL.Host != t.public.Host {
		return t.base.RoundTrip(r)
	}
	clone := r.Clone(r.Context())
	clone.URL.Scheme = t.backchannel.Scheme
	clone.URL.Host = t.backchannel.Host
	return t.base.RoundTrip(clone)
}
