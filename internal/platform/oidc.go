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
	central "github.com/j-s-te/project-management/internal/sessiongovernance"
	"golang.org/x/oauth2"
)

var ErrUnauthenticated = errors.New("unauthenticated")

// SessionIdleStatus 是会话空闲期限的公开视图。IdleRemaining 取空闲剩余与绝对
// 剩余的最小值（毫秒精度，非负），对应 API 契约中的 idle_timeout_seconds /
// idle_remaining_ms。
type SessionIdleStatus struct {
	IdleTimeout   time.Duration
	IdleRemaining time.Duration
}

const (
	loginTransactionTTL               = 10 * time.Minute
	authorizationRefreshRetryInterval = 5 * time.Second
)

type OIDCOptions struct {
	CentralClient                                                                                                                 central.Client
	CentralSessionEnabled                                                                                                         bool
	CentralSessionPublicBaseURL                                                                                                   string
	Issuer, BackchannelBaseURL, PlatformBaseURL, ClientID, ClientSecret, IdentityProviderHint, RedirectURI, PostLogoutRedirectURI string
	TenantID, ApplicationCode, EnvironmentCode, SessionCookieName, PathPrefix                                                     string
	SessionIdleTTL, SessionAbsoluteTTL, AuthorizationRefreshInterval, AuthorizationMaxStale, AuthorizationTimeout                 time.Duration
	SessionSecure                                                                                                                 bool
	// Audit 可选；非空时在登录/登出成功后向平台审计上报 auth.login / auth.logout。
	Audit AuditReporter
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
	SessionID         string `json:"sid"`
}

type compactIdentity struct {
	Subject, IdentityID, TenantID, PersonID, DisplayName, Username string
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
	audit                AuditReporter
	now                  func() time.Time
	refreshAuthorization func(context.Context, StoredOIDCSession, time.Time, string) (StoredOIDCSession, Principal, error)
	backchannelVerifier  backchannelLogoutVerifier
	centralReads         centralReadGroup
}

// NewOIDCAuthenticator 装配原OIDC认证、持久存储及可选中央会话依赖。
// 参数：ctx为调用上下文与取消边界。
//
//	options为本应用认证及运行选项。
//	store为本应用OIDC持久化边界。
//	encryptionKey为服务器端令牌材料加密密钥。
//
// 返回值：装配后的应用或认证实例；配置与依赖装配失败时返回错误。
func NewOIDCAuthenticator(ctx context.Context, options OIDCOptions, store OIDCStore, encryptionKey []byte) (*OIDCAuthenticator, error) {
	if options.CentralSessionEnabled && options.CentralClient == nil {
		public := options.CentralSessionPublicBaseURL
		if public == "" {
			target, err := url.Parse(options.RedirectURI)
			if err != nil {
				return nil, err
			}
			public = target.Scheme + "://" + target.Host
		}
		client, err := central.NewHTTPClient(options.PlatformBaseURL, public, options.ClientID, options.RedirectURI)
		if err != nil {
			return nil, err
		}
		options.CentralClient = client
	}
	if options.CentralClient != nil {
		if _, ok := store.(centralOIDCStore); !ok {
			return nil, errors.New("central sessions require transactional storage")
		}
	}
	if store == nil {
		return nil, errors.New("OIDC persistent store is required")
	}
	// 失败关闭：没有同时具备"空闲期限 < 绝对期限"的会话策略时拒绝启动，
	// 不允许退化成单一绝对期限或永不过期。
	if options.CentralClient == nil && (options.SessionIdleTTL <= 0 || options.SessionAbsoluteTTL <= options.SessionIdleTTL) {
		return nil, errors.New("OIDC session idle TTL must be positive and shorter than the absolute TTL")
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
	authenticator := &OIDCAuthenticator{
		options: options, provider: provider, verifier: provider.Verifier(&oidc.Config{ClientID: options.ClientID}), httpClient: client,
		store: store, authorization: NewHTTPAuthorizationContextClient(options.PlatformBaseURL, client), catalog: catalog, codec: codec,
		endSessionEndpoint: strings.TrimSpace(discovery.EndSessionEndpoint), audit: options.Audit, now: time.Now,
		oauth: oauth2.Config{ClientID: options.ClientID, ClientSecret: options.ClientSecret, RedirectURL: options.RedirectURI, Endpoint: provider.Endpoint(), Scopes: []string{"openid", "profile"}},
	}
	authenticator.backchannelVerifier = oidcProviderLogoutVerifier{authenticator: authenticator, maxTTL: 2 * time.Minute}
	return authenticator, nil
}

// Authenticate 校验当前浏览器会话、中央期限与业务授权。
// 参数：ctx为调用上下文与取消边界。
//
//	request为经过调用方校验的请求内容。
//
// 返回值：经过核验的主体或会话；明确失效与暂时依赖错误分别返回。
func (a *OIDCAuthenticator) Authenticate(ctx context.Context, request *http.Request) (Principal, error) {
	cookie, err := request.Cookie(a.options.SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return Principal{}, ErrUnauthenticated
	}
	now := a.now().UTC()
	hash := tokenDigest(cookie.Value)
	stored, err := a.store.FindSession(ctx, hash, now)
	if err != nil {
		if errors.Is(err, errOIDCRecordNotFound) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, ErrAuthorizationUnavailable
	}
	if stored.TenantID != a.options.TenantID {
		return Principal{}, ErrUnauthenticated
	}
	if a.options.CentralClient != nil {
		return a.authenticateCentral(ctx, stored, now, request.Method)
	}
	principal, err := decodePrincipal(stored.PrincipalJSON)
	if err != nil || principal.IdentityID != stored.IdentityID || principal.TenantID != stored.TenantID {
		_ = a.store.RevokeSession(ctx, hash, now)
		return Principal{}, ErrUnauthenticated
	}
	// 空闲期限只在显式活动接口推进；普通业务请求只更新 last_seen_at（见下方
	// UpdateSession），所以长时间无操作后会在这里被拒绝，而绝对期限不会延长。
	if !a.sessionWithinDeadlines(stored, now) {
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
	if err := a.store.TouchSessionLastSeen(ctx, hash, now); err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}

// sessionIdleDeadline 计算旧模式会话的真实空闲截止。
// 参数：stored 为持久会话，缺显式交互时间时回退创建时间。
// 返回值：截止时间与策略有效标记；缺配置或时间基准时失败关闭。
func (a *OIDCAuthenticator) sessionIdleDeadline(stored StoredOIDCSession) (time.Time, bool) {
	if a.options.SessionIdleTTL <= 0 {
		return time.Time{}, false
	}
	lastInteractive := stored.LastInteractiveAt
	if lastInteractive.IsZero() {
		lastInteractive = stored.CreatedAt
	}
	if lastInteractive.IsZero() {
		return time.Time{}, false
	}
	return lastInteractive.Add(a.options.SessionIdleTTL), true
}

// sessionWithinDeadlines 同时核验旧模式的绝对期限与真实空闲期限。
// 参数：stored 为持久会话，now 为服务端校验时钟。
// 返回值：两个期限均有效时为真，失效会话不得复活。
func (a *OIDCAuthenticator) sessionWithinDeadlines(stored StoredOIDCSession, now time.Time) bool {
	if !stored.SessionExpiresAt.After(now) {
		return false
	}
	idleDeadline, ok := a.sessionIdleDeadline(stored)
	return ok && now.Before(idleDeadline)
}

// sessionIdleStatus 计算旧模式空闲窗口的公开视图。
// 参数：stored 为持久会话，now 为服务端校验时钟。
// 返回值：空闲策略与距空闲或绝对截止较早者的非负时长。
func (a *OIDCAuthenticator) sessionIdleStatus(stored StoredOIDCSession, now time.Time) SessionIdleStatus {
	idleDeadline, _ := a.sessionIdleDeadline(stored)
	remaining := earliestTime(idleDeadline, stored.SessionExpiresAt).Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	return SessionIdleStatus{IdleTimeout: a.options.SessionIdleTTL, IdleRemaining: remaining}
}

// activeSessionForRequest 核验活动与状态接口的本地会话和主体。
// 参数：ctx 为请求上下文，request 为携带 Cookie 的浏览器请求。
// 返回值：会话、摘要和时钟；旧模式额外核验本地空闲，依赖故障保留服务异常。
func (a *OIDCAuthenticator) activeSessionForRequest(ctx context.Context, request *http.Request) (StoredOIDCSession, []byte, time.Time, error) {
	cookie, err := request.Cookie(a.options.SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return StoredOIDCSession{}, nil, time.Time{}, ErrUnauthenticated
	}
	now := a.now().UTC()
	hash := tokenDigest(cookie.Value)
	stored, err := a.store.FindSession(ctx, hash, now)
	if err != nil {
		if errors.Is(err, errOIDCRecordNotFound) {
			return StoredOIDCSession{}, nil, time.Time{}, ErrUnauthenticated
		}
		return StoredOIDCSession{}, nil, time.Time{}, ErrAuthorizationUnavailable
	}
	if stored.TenantID != a.options.TenantID {
		return StoredOIDCSession{}, nil, time.Time{}, ErrUnauthenticated
	}
	principal, err := decodePrincipal(stored.PrincipalJSON)
	if err != nil || principal.IdentityID != stored.IdentityID || principal.TenantID != stored.TenantID {
		return StoredOIDCSession{}, nil, time.Time{}, ErrUnauthenticated
	}
	if a.options.CentralClient == nil && !a.sessionWithinDeadlines(stored, now) {
		return StoredOIDCSession{}, nil, time.Time{}, ErrUnauthenticated
	}
	return stored, hash, now, nil
}

// SessionIdleStatus 读取会话空闲状态，中央模式使用平台权威窗口。
// 参数：ctx 为请求上下文，request 为携带 Cookie 的浏览器请求。
// 返回值：只读窗口；未认证或依赖错误不推进活动时间。
func (a *OIDCAuthenticator) SessionIdleStatus(ctx context.Context, request *http.Request) (SessionIdleStatus, error) {
	stored, _, now, err := a.activeSessionForRequest(ctx, request)
	if err != nil {
		return SessionIdleStatus{}, err
	}
	if a.options.CentralClient != nil {
		state, err := a.centralState(ctx, stored, false, true)
		if err != nil {
			return SessionIdleStatus{}, err
		}
		return SessionIdleStatus{
			IdleTimeout:   time.Duration(state.IdleTimeoutSeconds) * time.Second,
			IdleRemaining: time.Duration(state.IdleRemainingMS) * time.Millisecond,
		}, nil
	}
	return a.sessionIdleStatus(stored, now), nil
}

// TouchSessionActivity 记录显式活动，中央模式代理平台，旧模式条件更新本地时间。
// 参数：ctx 为请求上下文，request 为已通过同源写保护的浏览器请求。
// 返回值：最新空闲窗口；失效不复活，依赖故障返回服务异常。
func (a *OIDCAuthenticator) TouchSessionActivity(ctx context.Context, request *http.Request) (SessionIdleStatus, error) {
	stored, hash, now, err := a.activeSessionForRequest(ctx, request)
	if err != nil {
		return SessionIdleStatus{}, err
	}
	if a.options.CentralClient != nil {
		state, err := a.centralState(ctx, stored, true, true)
		if err != nil {
			return SessionIdleStatus{}, err
		}
		return SessionIdleStatus{
			IdleTimeout:   time.Duration(state.IdleTimeoutSeconds) * time.Second,
			IdleRemaining: time.Duration(state.IdleRemainingMS) * time.Millisecond,
		}, nil
	}
	if err := a.store.TouchSessionActivity(ctx, hash, now, now.Add(-a.options.SessionIdleTTL)); err != nil {
		if errors.Is(err, errOIDCRecordNotFound) {
			return SessionIdleStatus{}, ErrUnauthenticated
		}
		return SessionIdleStatus{}, err
	}
	stored.LastInteractiveAt = now
	return a.sessionIdleStatus(stored, now), nil
}

// Login 建立原state、nonce和PKCE事务并进入可信登录流程。
// 参数：w为HTTP响应写入器。
//
//	r为原始HTTP请求。
//
// 返回值：无。
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
	transaction.ForceLogin = strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("prompt")), "login")
	if err := a.store.SaveLoginTransaction(r.Context(), transaction); err != nil {
		http.Error(w, "login service is unavailable", http.StatusServiceUnavailable)
		return
	}
	authOptions := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("prompt")), "login") {
		authOptions = append(authOptions, oauth2.SetAuthURLParam("prompt", "login"))
	}
	if hint := strings.TrimSpace(a.options.IdentityProviderHint); hint != "" {
		authOptions = append(authOptions, oauth2.SetAuthURLParam("kc_idp_hint", hint))
	}
	target := a.oauth.AuthCodeURL(state, authOptions...)
	if a.options.CentralClient != nil {
		http.SetCookie(w, a.stateCookie(state, 600))
		target = a.options.CentralClient.BeginURL(state, nonce)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// Callback 消费原登录事务，验证身份并创建受中央期限约束的本地会话。
// 参数：w为HTTP响应写入器，r为携带同浏览器state及授权码的回调。
// 返回值：无；旧周期最多重登一次，故障、身份冲突及再次失效保持失败关闭。
func (a *OIDCAuthenticator) Callback(w http.ResponseWriter, r *http.Request) {
	if a.options.CentralClient != nil {
		cookie, err := r.Cookie(a.options.SessionCookieName + "_oidc_state")
		if err != nil || cookie.Value == "" || cookie.Value != r.URL.Query().Get("state") {
			http.Error(w, "invalid browser state", 401)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if ticket := r.URL.Query().Get("session_link_ticket"); ticket != "" {
			a.acceptSessionLink(w, r, cookie.Value, ticket)
			return
		}
		http.SetCookie(w, a.stateCookie("", -1))
	}
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
	var binding central.Binding
	if a.options.CentralClient != nil {
		ticket, unwrapErr := a.codec.decrypt(tx.CentralTicketCipher)
		if unwrapErr != nil || len(ticket) == 0 {
			http.Error(w, "invalid central ticket", 401)
			return
		}
		binding, err = a.options.CentralClient.Complete(ctx, string(ticket), token.AccessToken, rawIDToken)
		if err == nil {
			err = central.Validate(binding.Context, a.centralExpectation(), identity.IdentityID)
		}
		if err != nil {
			a.writeCentralLoginFailure(w, r, tx, err)
			return
		}
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
	expiresAt := now.Add(a.options.SessionAbsoluteTTL)
	stored := StoredOIDCSession{SessionIDHash: tokenDigest(rawSession), TenantID: principal.TenantID, IdentityID: principal.IdentityID, OIDCSubject: strings.TrimSpace(claims.Subject), OIDCSessionID: strings.TrimSpace(claims.SessionID), PersonID: principal.PersonID, PrincipalJSON: principalJSON, IDTokenCipher: idTokenCipher, OAuthTokenCipher: tokenCipher, AuthorizationRevision: principal.AuthorizationRevision, AuthorizationCheckedAt: now, TokenExpiresAt: tokenExpiry, SessionExpiresAt: expiresAt, CreatedAt: now, LastSeenAt: now, LastInteractiveAt: now}
	if a.options.CentralClient != nil {
		if stored.OIDCSessionID == "" {
			http.Error(w, "missing upstream session", 401)
			return
		}
		stored.CentralBindingCipher, err = a.codec.encrypt([]byte(binding.Token))
		if err != nil {
			http.Error(w, "session encryption failed", 503)
			return
		}
		stored.CentralSessionID = binding.Context.SessionID
		expiresAt = binding.Context.AbsoluteExpiresAt
		stored.SessionExpiresAt = expiresAt
	}
	expiresAt, err = a.createSession(r.Context(), now, stored)
	if err != nil {
		http.Error(w, "session service is unavailable", http.StatusServiceUnavailable)
		return
	}
	a.reportAuth(r, AuditEvent{ActorID: principal.UserID, ActorName: principal.DisplayName, Action: "auth.login", ResourceType: "auth_session", Result: "SUCCESS", RiskLevel: "LOW"})
	http.SetCookie(w, a.cookie(rawSession, expiresAt))
	http.Redirect(w, r, a.PublicPath(tx.ReturnPath), http.StatusFound)
}

// createSession 持久化新会话，中央模式保留平台原始认证的绝对截止。
// 参数：ctx 为请求上下文，now 为登录时钟，stored 为令牌与主体快照。
// 返回值：固定的绝对截止；持久化失败返回错误。
func (a *OIDCAuthenticator) createSession(ctx context.Context, now time.Time, stored StoredOIDCSession) (time.Time, error) {
	if a.options.CentralClient == nil {
		stored.SessionExpiresAt = now.Add(a.options.SessionAbsoluteTTL)
	}
	stored.CreatedAt = now
	stored.LastSeenAt = now
	stored.LastInteractiveAt = now
	if err := a.store.CreateSession(ctx, stored); err != nil {
		return time.Time{}, err
	}
	return stored.SessionExpiresAt, nil
}

// refreshStoredSession 保持旧模式刷新钩子的固定签名与行为。
// 参数：ctx为请求，stored为当前记录，now为时钟，method为业务方法。
// 返回值：新记录及主体，或认证和依赖错误。
func (a *OIDCAuthenticator) refreshStoredSession(ctx context.Context, stored StoredOIDCSession, now time.Time, method string) (StoredOIDCSession, Principal, error) {
	return a.refreshStoredSessionWithTokenPolicy(ctx, stored, now, method, true)
}

// refreshStoredSessionWithTokenPolicy 复核项目权限，中央模式禁用第二事务再次刷新。
// 参数：ctx为请求，stored为当前记录，now为时钟，method为业务方法，canRefresh为是否允许轮换。
// 返回值：新记录与主体；明确拒绝和暂时故障保留各自错误语义。
func (a *OIDCAuthenticator) refreshStoredSessionWithTokenPolicy(ctx context.Context, stored StoredOIDCSession, now time.Time, method string, canRefresh bool) (StoredOIDCSession, Principal, error) {
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
	storedPrincipal, _ := decodePrincipal(stored.PrincipalJSON)
	subject := storedPrincipal.Subject
	if subject == "" {
		subject = stored.IdentityID
	}
	identity := compactIdentity{Subject: subject, IdentityID: stored.IdentityID, TenantID: stored.TenantID, PersonID: stored.PersonID}
	var idToken *oidc.IDToken
	var claims oidcClaims
	if !token.Expiry.After(now) || a.options.CentralClient != nil && !token.Expiry.After(now.Add(time.Minute)) {
		if !canRefresh {
			return stored, Principal{}, ErrAuthorizationUnavailable
		}
		token, idTokenBytes, idToken, claims, identity, err = a.refreshTokens(ctx, &token, identity)
		if err != nil {
			return stored, Principal{}, classifyRefreshError(err)
		}
	}
	authorization, tokenPointer, rawIDToken, verifiedIDToken, refreshedIdentity, err := a.resolveAuthorization(ctx, &token, string(idTokenBytes), idToken, claims, identity, canRefresh)
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
	if decodeErr == nil && principal.Username == "" {
		principal.Username = oldPrincipal.Username
	}
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
	if claims.Subject != "" {
		if a.options.CentralClient != nil && claims.SessionID != stored.OIDCSessionID {
			return stored, Principal{}, ErrUnauthenticated
		}
		stored.OIDCSubject = strings.TrimSpace(claims.Subject)
		stored.OIDCSessionID = strings.TrimSpace(claims.SessionID)
	}
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

// resolveAuthorization 从基础平台读取并校验当前应用的授权快照。
// 参数：ctx为调用上下文与取消边界。
//
//	token为待验证的服务器端令牌。
//	rawIDToken为服务器取得的原始身份令牌。
//	idToken为已经完成提供方校验的身份令牌。
//	claims为已经完成签名及用途校验的声明。
//	identity为经过验签及租户校验的平台身份。
//	retry401为是否允许既有分支在明确拒绝后刷新重试。
//
// 返回值：与该调用匹配的核验或持久化结果；失败保留原有错误语义。
func (a *OIDCAuthenticator) resolveAuthorization(ctx context.Context, token *oauth2.Token, rawIDToken string, idToken *oidc.IDToken, claims oidcClaims, identity compactIdentity, retry401 bool) (AuthorizationContext, *oauth2.Token, string, *oidc.IDToken, compactIdentity, error) {
	value, err := a.authorization.Resolve(ctx, token.AccessToken)
	if errors.Is(err, ErrUnauthenticated) && retry401 {
		refreshed, refreshedRaw, refreshedID, refreshedClaims, refreshedIdentity, refreshErr := a.refreshTokens(ctx, token, identity)
		if refreshErr != nil {
			return AuthorizationContext{}, token, rawIDToken, idToken, identity, classifyRefreshError(refreshErr)
		}
		token, rawIDToken, idToken, claims, identity = &refreshed, string(refreshedRaw), refreshedID, refreshedClaims, refreshedIdentity
		value, err = a.authorization.Resolve(ctx, token.AccessToken)
	}
	if err != nil {
		return AuthorizationContext{}, token, rawIDToken, idToken, identity, err
	}
	return value, token, rawIDToken, idToken, identity, nil
}

// refreshTokens 在服务器端使用原刷新材料取得轮换令牌。
// 参数：ctx为调用上下文与取消边界。
//
//	current为当前已核验的持久投影。
//	expected为当前应用的身份及用途约束。
//
// 返回值：已经校验的身份或轮换材料；协议、签名或依赖失败返回错误。
func (a *OIDCAuthenticator) refreshTokens(ctx context.Context, current *oauth2.Token, expected compactIdentity) (oauth2.Token, []byte, *oidc.IDToken, oidcClaims, compactIdentity, error) {
	if current == nil || strings.TrimSpace(current.RefreshToken) == "" {
		return oauth2.Token{}, nil, nil, oidcClaims{}, compactIdentity{}, ErrUnauthenticated
	}
	seed := &oauth2.Token{RefreshToken: current.RefreshToken, Expiry: a.now().UTC().Add(-time.Second)}
	token, err := a.oauth.TokenSource(oidc.ClientContext(ctx, a.httpClient), seed).Token()
	if err != nil {
		return oauth2.Token{}, nil, nil, oidcClaims{}, compactIdentity{}, err
	}
	rawIDToken, idToken, claims, identity, err := a.verifyIDToken(ctx, token, "")
	if err != nil || identity.Subject != expected.Subject || identity.TenantID != expected.TenantID || identity.IdentityID != expected.IdentityID {
		return oauth2.Token{}, nil, nil, oidcClaims{}, compactIdentity{}, ErrUnauthenticated
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
	if err != nil {
		return raw, idToken, claims, identity, err
	}
	if strings.TrimSpace(claims.PreferredUsername) == "" {
		info, profileErr := a.provider.UserInfo(oidc.ClientContext(ctx, a.httpClient), oauth2.StaticTokenSource(token))
		if profileErr != nil {
			return "", nil, oidcClaims{}, compactIdentity{}, profileErr
		}
		var profile struct {
			Subject  string `json:"sub"`
			Username string `json:"preferred_username"`
		}
		if profileErr = info.Claims(&profile); profileErr != nil || profile.Subject != claims.Subject {
			return "", nil, oidcClaims{}, compactIdentity{}, errors.New("OIDC UserInfo subject mismatch")
		}
		claims.PreferredUsername = strings.TrimSpace(profile.Username)
	}

	identity.Username = strings.TrimSpace(claims.PreferredUsername)
	return raw, idToken, claims, identity, nil
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
	if claims.IdentityID == "" || claims.IdentityID != strings.TrimSpace(claims.IdentityID) {
		return compactIdentity{}, errors.New("identity_id is required")
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
	return compactIdentity{Subject: claims.Subject, IdentityID: claims.IdentityID, TenantID: claims.TenantID, PersonID: claims.PersonID, DisplayName: displayName, Username: strings.TrimSpace(claims.PreferredUsername)}, nil
}

func (a *OIDCAuthenticator) principalFromAuthorization(identity compactIdentity, value AuthorizationContext) (Principal, error) {
	subjectID, err := canonicalSubjectID(value.SubjectID, value.IdentityID)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrInvalidAuthorization, err)
	}
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
	return Principal{Subject: identity.Subject, TenantID: value.TenantID, IdentityID: subjectID, PersonID: personID, UserID: subjectID, DisplayName: identity.DisplayName, Username: identity.Username, Roles: append([]string(nil), value.Roles...), Permissions: permissions, DataScopes: append([]DataScope(nil), value.DataScopes...), AuthorizationRevision: value.AuthorizationRevision, CatalogVersion: a.catalog.Version}, nil
}

func canonicalSubjectID(subjectID, identityID string) (string, error) {
	subjectID = strings.TrimSpace(subjectID)
	identityID = strings.TrimSpace(identityID)
	if subjectID == "" {
		subjectID = identityID
	}
	if subjectID == "" || identityID == "" || subjectID != identityID {
		return "", errors.New("subject_id and identity_id must identify the same platform subject")
	}
	return subjectID, nil
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
		identityID := ""
		if stored, findErr := a.store.FindSession(r.Context(), hash, now); findErr == nil {
			if plaintext, decryptErr := a.codec.decrypt(stored.IDTokenCipher); decryptErr == nil {
				idToken = string(plaintext)
			}
			identityID = stored.IdentityID
		}
		_ = a.store.RevokeSession(r.Context(), hash, now)
		if identityID != "" {
			a.reportAuth(r, AuditEvent{ActorID: identityID, Action: "auth.logout", ResourceType: "auth_session", Result: "SUCCESS", RiskLevel: "LOW"})
		}
	}
	expired := a.cookie("", time.Unix(1, 0))
	expired.MaxAge = -1
	http.SetCookie(w, expired)
	return idToken
}

// reportAuth 向平台审计上报登录/登出事件；审计未配置或上报失败均不影响认证流程。
func (a *OIDCAuthenticator) reportAuth(request *http.Request, event AuditEvent) {
	if a.audit == nil {
		return
	}
	_ = a.audit.Report(request.Context(), event)
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
