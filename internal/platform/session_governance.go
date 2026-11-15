package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	central "github.com/j-s-te/project-management/internal/sessiongovernance"
)

type sessionUpdateFunc func(StoredOIDCSession) (StoredOIDCSession, bool, error)
type centralOIDCStore interface {
	AttachSessionLink(context.Context, []byte, []byte, time.Time) (LoginTransaction, error)
	WithSessionLock(context.Context, []byte, time.Time, sessionUpdateFunc) (StoredOIDCSession, error)
}

// centralExpectation 只使用运行配置中的租户、客户端、应用和环境信任边界。
// 参数：无。
// 返回值：中央响应必须匹配的精确范围。
func (a *OIDCAuthenticator) centralExpectation() central.Expectation {
	return central.Expectation{TenantID: a.options.TenantID, ClientID: a.options.ClientID, ApplicationCode: a.options.ApplicationCode, EnvironmentCode: a.options.EnvironmentCode}
}

// centralState 验证本地Cookie精确绑定的中央周期，不从本地请求时间续期。
// 参数：ctx为请求，record为本地密文会话，interact为同源真实活动，revoke为是否可在锁外撤销。
// 返回值：非敏感权威窗口；明确失效401，网络或数据库故障503。
func (a *OIDCAuthenticator) centralState(ctx context.Context, record StoredOIDCSession, interact, revoke bool) (central.Context, error) {
	if len(record.CentralBindingCipher) == 0 {
		return central.Context{}, ErrUnauthenticated
	}
	secret, err := a.codec.decrypt(record.CentralBindingCipher)
	if err != nil || len(secret) == 0 {
		return central.Context{}, ErrUnauthenticated
	}
	var state central.Context
	if interact {
		state, err = a.options.CentralClient.Activity(ctx, string(secret))
	} else {
		state, err = a.readCentralState(ctx, string(secret))
	}
	if err == nil {
		err = central.Validate(state, a.centralExpectation(), record.IdentityID)
	}
	if errors.Is(err, central.ErrInactive) || errors.Is(err, central.ErrBindingConflict) {
		if revoke {
			if err := a.store.RevokeSession(ctx, record.SessionIDHash, a.now().UTC()); err != nil {
				return central.Context{}, ErrAuthorizationUnavailable
			}
		}
		return central.Context{}, ErrUnauthenticated
	}
	if err != nil {
		return central.Context{}, ErrAuthorizationUnavailable
	}
	if state.SessionID != record.CentralSessionID {
		return central.Context{}, ErrUnauthenticated
	}
	return state, nil
}

// authenticateCentral 在中央有效的前提下使用数据库锁串行刷新。
// 参数：ctx为请求，record为本地会话，now为时钟，method为当前业务方法。
// 返回值：有效项目主体；明确失效与依赖故障分别返回401和503语义。
func (a *OIDCAuthenticator) authenticateCentral(ctx context.Context, record StoredOIDCSession, now time.Time, method string) (Principal, error) {
	if _, err := a.centralState(ctx, record, false, true); err != nil {
		return Principal{}, err
	}
	principal, err := decodePrincipal(record.PrincipalJSON)
	if err != nil || principal.IdentityID != record.IdentityID || principal.TenantID != record.TenantID {
		return Principal{}, ErrUnauthenticated
	}
	if record.TokenExpiresAt.After(now.Add(time.Minute)) && now.Sub(record.AuthorizationCheckedAt) < a.options.AuthorizationRefreshInterval {
		return principal, nil
	}
	store, ok := a.store.(centralOIDCStore)
	if !ok {
		return Principal{}, ErrAuthorizationUnavailable
	}
	if !record.TokenExpiresAt.After(a.now().UTC().Add(time.Minute)) {
		rotationCtx, cancel := central.RotationContext(ctx)
		record, err = store.WithSessionLock(rotationCtx, record.SessionIDHash, a.now().UTC(), func(current StoredOIDCSession) (StoredOIDCSession, bool, error) {
			return a.rotateCentralTokens(rotationCtx, current, a.now().UTC())
		})
		cancel()
		if err != nil {
			return Principal{}, centralAuthenticationError(err)
		}
	}
	updated, err := store.WithSessionLock(ctx, record.SessionIDHash, now, func(current StoredOIDCSession) (StoredOIDCSession, bool, error) {
		lockedNow := a.now().UTC()
		if _, err := a.centralState(ctx, current, false, false); err != nil {
			return current, false, err
		}
		if current.TokenExpiresAt.After(lockedNow.Add(time.Minute)) && lockedNow.Sub(current.AuthorizationCheckedAt) < a.options.AuthorizationRefreshInterval {
			return current, false, nil
		}
		refreshed, _, err := a.refreshStoredSessionWithTokenPolicy(ctx, current, lockedNow, method, false)
		return refreshed, err == nil, err
	})
	if err != nil {
		return Principal{}, centralAuthenticationError(err)
	}
	return decodePrincipal(updated.PrincipalJSON)
}

// stateCookie 将原始登录state绑定到发起登录的浏览器，不接受仅有数据库事务的回调。
// 参数：state为原始随机状态，maxAge为有效秒数或删除标志。
// 返回值：与项目Cookie相同安全属性和路径的HttpOnly状态Cookie。
func (a *OIDCAuthenticator) stateCookie(state string, maxAge int) *http.Cookie {
	cookie := a.cookie(state, a.now().UTC().Add(time.Duration(maxAge)*time.Second))
	cookie.Name = a.options.SessionCookieName + "_oidc_state"
	cookie.MaxAge = maxAge
	return cookie
}

// acceptSessionLink 将桥接票据原子写入同浏览器的未消费事务，再继续原始OIDC。
// 参数：writer/request为HTTP边界，state为Cookie验证的原始状态，ticket为中央一次性票据。
// 返回值：无；过期或重放不跳过原state、nonce及PKCE。
func (a *OIDCAuthenticator) acceptSessionLink(writer http.ResponseWriter, request *http.Request, state, ticket string) {
	if request.URL.Query().Get("code") != "" || len(ticket) > 128 {
		http.Error(writer, "invalid central ticket", 401)
		return
	}
	cipher, err := a.codec.encrypt([]byte(ticket))
	if err != nil {
		http.Error(writer, "cannot prepare session", 503)
		return
	}
	store, ok := a.store.(centralOIDCStore)
	if !ok {
		http.Error(writer, "session storage unavailable", 503)
		return
	}
	transaction, err := store.AttachSessionLink(request.Context(), tokenDigest(state), cipher, a.now().UTC())
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, ErrUnauthenticated) {
			status = http.StatusUnauthorized
		}
		http.Error(writer, "session ticket unavailable", status)
		return
	}
	nonce, err := a.codec.decrypt(transaction.NonceCipher)
	if err != nil {
		http.Error(writer, "invalid login state", 401)
		return
	}
	verifier, err := a.codec.decrypt(transaction.CodeVerifierCipher)
	if err != nil {
		http.Error(writer, "invalid login state", 401)
		return
	}
	http.Redirect(writer, request, a.authorizationURL(state, string(nonce), string(verifier), transaction.ForceLogin), http.StatusFound)
}

// SessionState 返回同一中央生命周期，不触碰业务权限快照或活动时间。
// 参数：writer为响应，request携带当前项目Cookie。
// 返回值：无；失效返回401，依赖故障503。
func (a *OIDCAuthenticator) SessionState(writer http.ResponseWriter, request *http.Request) {
	a.lifecycle(writer, request, false)
}

// Activity 接受精确同源和CSRF保护的显式真实交互。
// 参数：writer为响应，request携带项目Cookie及X-CSRF-Token:1。
// 返回值：无；后台读取或跨源请求不会推进中央期限。
func (a *OIDCAuthenticator) Activity(writer http.ResponseWriter, request *http.Request) {
	a.lifecycle(writer, request, true)
}

// lifecycle 统一项目状态和活动代理的认证、同源及错误分类。
// 参数：writer/request为HTTP边界，interact区分只读与真实活动。
// 返回值：无；成功只输出非敏感生命周期JSON。
func (a *OIDCAuthenticator) lifecycle(writer http.ResponseWriter, request *http.Request, interact bool) {
	writer.Header().Set("Cache-Control", "no-store")
	if a.options.CentralClient == nil {
		http.Error(writer, "central sessions not enabled", 503)
		return
	}
	if interact {
		origin, err := url.Parse(request.Header.Get("Origin"))
		expected, expectedErr := url.Parse(a.options.RedirectURI)
		if err != nil || expectedErr != nil || origin.Scheme != expected.Scheme || origin.Host != expected.Host || request.Header.Get("X-CSRF-Token") != "1" {
			http.Error(writer, "forbidden", 403)
			return
		}
	}
	if _, err := a.Authenticate(request.Context(), request); err != nil {
		status := 503
		if errors.Is(err, ErrUnauthenticated) {
			status = 401
		}
		http.Error(writer, "session unavailable", status)
		return
	}
	cookie, err := request.Cookie(a.options.SessionCookieName)
	if err != nil {
		http.Error(writer, "unauthenticated", 401)
		return
	}
	record, err := a.store.FindSession(request.Context(), tokenDigest(cookie.Value), a.now().UTC())
	if err != nil {
		http.Error(writer, "session unavailable", 503)
		return
	}
	state, err := a.centralState(request.Context(), record, interact, true)
	if err != nil {
		status := 503
		if errors.Is(err, ErrUnauthenticated) {
			status = 401
		}
		http.Error(writer, "session unavailable", status)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"code": "OK", "message": "操作成功", "data": state})
}
