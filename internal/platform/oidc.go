package platform

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var ErrUnauthenticated = errors.New("unauthenticated")

const authorizationRefreshRetryInterval = 5 * time.Second

type OIDCOptions struct {
	Issuer, BackchannelBaseURL, ClientID, ClientSecret, RedirectURI, PostLogoutRedirectURI string
	TenantID, SessionCookieName, PathPrefix                                                string
	SessionTTL, AuthorizationRefreshInterval                                               time.Duration
	SessionSecure                                                                          bool
}

type oidcClaims struct {
	Subject        string   `json:"sub"`
	Nonce          string   `json:"nonce"`
	TenantID       string   `json:"tenant_id"`
	Roles          []string `json:"roles"`
	Permissions    []string `json:"permissions"`
	RoleConfigHash string   `json:"role_config_hash"`
	AuthzRevision  uint64   `json:"authz_revision"`
}

type transaction struct {
	Nonce, Verifier string
	ExpiresAt       time.Time
}
type session struct {
	mu                          sync.Mutex
	Principal                   Principal
	IDToken                     string
	Token                       *oauth2.Token
	RefreshedAt, RefreshRetryAt time.Time
	TokenExpiresAt, ExpiresAt   time.Time
}

type OIDCAuthenticator struct {
	options      OIDCOptions
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	oauth        oauth2.Config
	httpClient   *http.Client
	mu           sync.Mutex
	transactions map[string]transaction
	sessions     map[string]*session
	now          func() time.Time
	refresh      func(context.Context, *session, time.Time) error
}

func NewOIDCAuthenticator(ctx context.Context, options OIDCOptions) (*OIDCAuthenticator, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if options.BackchannelBaseURL != "" {
		publicURL, err := url.Parse(strings.TrimRight(options.Issuer, "/"))
		if err != nil {
			return nil, err
		}
		backchannelURL, err := url.Parse(strings.TrimRight(options.BackchannelBaseURL, "/"))
		if err != nil {
			return nil, err
		}
		client.Transport = &backchannelTransport{base: http.DefaultTransport, public: publicURL, backchannel: backchannelURL}
	}
	oidcContext := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(oidcContext, strings.TrimRight(options.Issuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("load OIDC discovery: %w", err)
	}
	return &OIDCAuthenticator{
		options: options, provider: provider, verifier: provider.Verifier(&oidc.Config{ClientID: options.ClientID}), httpClient: client, now: time.Now,
		oauth:        oauth2.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, RedirectURL: options.RedirectURI, Endpoint: provider.Endpoint(), Scopes: []string{"openid", "profile"}},
		transactions: map[string]transaction{}, sessions: map[string]*session{},
	}, nil
}

func (a *OIDCAuthenticator) Authenticate(ctx context.Context, request *http.Request) (Principal, error) {
	cookie, err := request.Cookie(a.options.SessionCookieName)
	if err != nil || cookie.Value == "" {
		return Principal{}, ErrUnauthenticated
	}
	now := a.now().UTC()
	a.mu.Lock()
	a.cleanup(now)
	current := a.sessions[cookie.Value]
	a.mu.Unlock()
	if current == nil {
		return Principal{}, ErrUnauthenticated
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if !current.ExpiresAt.After(now) {
		a.deleteSession(cookie.Value, current)
		return Principal{}, ErrUnauthenticated
	}
	tokenExpired := !current.TokenExpiresAt.After(now)
	refreshDue := tokenExpired || now.Sub(current.RefreshedAt) >= a.options.AuthorizationRefreshInterval
	refreshAllowed := tokenExpired || !current.RefreshRetryAt.After(now)
	if refreshDue && refreshAllowed {
		refresh := a.refresh
		if refresh == nil {
			refresh = a.refreshSession
		}
		if err := refresh(ctx, current, now); err != nil {
			if tokenExpired || refreshWasRejected(err) {
				a.deleteSession(cookie.Value, current)
				return Principal{}, ErrUnauthenticated
			}
			// 平台短暂不可用时保留尚未过期的已验证主体；令牌过期或被撤销仍失败关闭。
			current.RefreshRetryAt = now.Add(authorizationRefreshRetryInterval)
			return current.Principal, nil
		}
		current.RefreshRetryAt = time.Time{}
	}
	return current.Principal, nil
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
	now := a.now().UTC()
	a.mu.Lock()
	a.cleanup(now)
	a.transactions[state] = transaction{Nonce: nonce, Verifier: verifier, ExpiresAt: now.Add(10 * time.Minute)}
	a.mu.Unlock()
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
	a.mu.Lock()
	a.cleanup(now)
	tx, ok := a.transactions[state]
	delete(a.transactions, state)
	a.mu.Unlock()
	if !ok || !tx.ExpiresAt.After(now) {
		http.Error(w, "invalid or expired OIDC state", http.StatusUnauthorized)
		return
	}
	ctx := oidc.ClientContext(r.Context(), a.httpClient)
	token, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(tx.Verifier))
	if err != nil {
		http.Error(w, "OIDC token exchange failed", http.StatusUnauthorized)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		http.Error(w, "OIDC ID token is missing", http.StatusUnauthorized)
		return
	}
	idToken, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		http.Error(w, "OIDC ID token verification failed", http.StatusUnauthorized)
		return
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil || claims.Nonce != tx.Nonce {
		http.Error(w, "OIDC claims are invalid", http.StatusUnauthorized)
		return
	}
	principal, err := principalFromClaims(claims, a.options.TenantID)
	if err != nil {
		http.Error(w, "OIDC authorization claims are invalid", http.StatusUnauthorized)
		return
	}
	info, err := a.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		http.Error(w, "OIDC user information failed", http.StatusUnauthorized)
		return
	}
	var userInfo struct {
		Name string `json:"name"`
	}
	if info.Subject != principal.UserID || info.Claims(&userInfo) != nil || strings.TrimSpace(userInfo.Name) == "" {
		http.Error(w, "OIDC user information is invalid", http.StatusUnauthorized)
		return
	}
	principal.DisplayName = strings.TrimSpace(userInfo.Name)
	if token.RefreshToken == "" {
		http.Error(w, "OIDC refresh token is missing", http.StatusUnauthorized)
		return
	}
	sessionID, err := randomValue(32)
	if err != nil {
		http.Error(w, "cannot create session", http.StatusInternalServerError)
		return
	}
	current := &session{Principal: principal, IDToken: raw, Token: token, RefreshedAt: now, TokenExpiresAt: idToken.Expiry, ExpiresAt: now.Add(a.options.SessionTTL)}
	a.mu.Lock()
	a.sessions[sessionID] = current
	a.mu.Unlock()
	http.SetCookie(w, a.cookie(sessionID, current.ExpiresAt))
	http.Redirect(w, r, a.PublicPath("/"), http.StatusFound)
}

func (a *OIDCAuthenticator) refreshSession(ctx context.Context, current *session, now time.Time) error {
	if current.Token == nil || current.Token.RefreshToken == "" {
		return errors.New("refresh token missing")
	}
	seed := &oauth2.Token{RefreshToken: current.Token.RefreshToken, Expiry: now.Add(-time.Second)}
	token, err := a.oauth.TokenSource(oidc.ClientContext(ctx, a.httpClient), seed).Token()
	if err != nil {
		return err
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return errors.New("refreshed ID token missing")
	}
	idToken, err := a.verifier.Verify(oidc.ClientContext(ctx, a.httpClient), raw)
	if err != nil {
		return err
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return err
	}
	principal, err := principalFromClaims(claims, a.options.TenantID)
	if err != nil {
		return err
	}
	if principal.UserID != current.Principal.UserID {
		return errors.New("refreshed subject changed")
	}
	principal.DisplayName = current.Principal.DisplayName
	current.Principal, current.IDToken, current.Token, current.RefreshedAt, current.TokenExpiresAt = principal, raw, token, now, idToken.Expiry
	return nil
}

func refreshWasRejected(err error) bool {
	var retrieveError *oauth2.RetrieveError
	return errors.As(err, &retrieveError) && retrieveError.ErrorCode == "invalid_grant"
}

func principalFromClaims(claims oidcClaims, tenantID string) (Principal, error) {
	if strings.TrimSpace(claims.Subject) == "" || claims.TenantID != tenantID || len(claims.Roles) == 0 || len(claims.Permissions) == 0 || claims.RoleConfigHash == "" || claims.AuthzRevision == 0 {
		return Principal{}, errors.New("authorization metadata incomplete")
	}
	for _, role := range claims.Roles {
		if role == "" || role != strings.TrimSpace(role) {
			return Principal{}, errors.New("roles malformed")
		}
	}
	roles := normalize(claims.Roles)
	if len(roles) != len(claims.Roles) {
		return Principal{}, errors.New("roles malformed")
	}
	permissions := map[string]bool{}
	for _, value := range claims.Permissions {
		if strings.TrimSpace(value) == "" || value == "all" {
			return Principal{}, errors.New("permissions malformed")
		}
		permissions[value] = true
	}
	return Principal{TenantID: claims.TenantID, UserID: claims.Subject, Roles: roles, Permissions: permissions, RoleConfigHash: claims.RoleConfigHash, AuthzRevision: claims.AuthzRevision}, nil
}

func normalize(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (a *OIDCAuthenticator) Logout(w http.ResponseWriter, r *http.Request) {
	idToken := a.clear(w, r)
	endpoint, _ := url.Parse(strings.TrimRight(a.options.Issuer, "/") + "/oauth2/logout")
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
	var token string
	if cookie, err := r.Cookie(a.options.SessionCookieName); err == nil {
		a.mu.Lock()
		current := a.sessions[cookie.Value]
		delete(a.sessions, cookie.Value)
		a.mu.Unlock()
		if current != nil {
			current.mu.Lock()
			token = current.IDToken
			current.mu.Unlock()
		}
	}
	expired := a.cookie("", time.Unix(1, 0))
	expired.MaxAge = -1
	http.SetCookie(w, expired)
	return token
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
	if path == "/" {
		return prefix + "/"
	}
	return prefix + "/" + strings.TrimLeft(path, "/")
}
func (a *OIDCAuthenticator) cleanup(now time.Time) {
	for key, value := range a.transactions {
		if !value.ExpiresAt.After(now) {
			delete(a.transactions, key)
		}
	}
	for key, value := range a.sessions {
		if !value.ExpiresAt.After(now) {
			delete(a.sessions, key)
		}
	}
}
func (a *OIDCAuthenticator) deleteSession(id string, expected *session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[id] == expected {
		delete(a.sessions, id)
	}
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
