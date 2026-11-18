package platform

import (
	"context"
	"errors"
	"time"

	"github.com/j-s-te/project-management/internal/authorizationrefresh"
)

type legacySessionLocker interface {
	WithSessionLock(context.Context, []byte, time.Time, sessionUpdateFunc) (StoredOIDCSession, error)
}

// authenticateLegacyRefresh 将所有旧模式授权复核与写回放入同一会话行锁。
// 参数：ctx为请求，stored定位原Cookie会话，now为初始时钟，method决定陈旧只读策略。
// 返回值：锁内重读后的有效主体；认证401、授权403、依赖503语义保持不变。
func (a *OIDCAuthenticator) authenticateLegacyRefresh(ctx context.Context, stored StoredOIDCSession,
	now time.Time, method string) (Principal, error) {
	locker, ok := a.store.(legacySessionLocker)
	if !ok {
		return Principal{}, ErrAuthorizationUnavailable
	}
	var principal Principal
	_, err := locker.WithSessionLock(ctx, stored.SessionIDHash, now,
		func(current StoredOIDCSession) (StoredOIDCSession, bool, error) {
			lockedNow := a.now().UTC()
			var err error
			principal, err = decodePrincipal(current.PrincipalJSON)
			if err != nil || current.TenantID != a.options.TenantID ||
				principal.IdentityID != current.IdentityID || principal.TenantID != current.TenantID ||
				!a.sessionWithinDeadlines(current, lockedNow) {
				return current, false, ErrUnauthenticated
			}
			forced := authorizationrefresh.Forced(ctx)
			if !forced && current.TokenExpiresAt.After(lockedNow) &&
				lockedNow.Sub(current.AuthorizationCheckedAt) < a.options.AuthorizationRefreshInterval {
				return current, false, nil
			}
			if !forced && current.RefreshRetryAt.After(lockedNow) {
				if staleReadAllowed(method, lockedNow, current.AuthorizationCheckedAt, a.options.AuthorizationMaxStale, current.TokenExpiresAt) {
					return current, false, nil
				}
				return current, false, ErrAuthorizationUnavailable
			}
			refresh := a.refreshAuthorization
			if refresh == nil {
				refresh = a.refreshStoredSession
			}
			updated, refreshed, refreshErr := refresh(ctx, current, lockedNow, method)
			if refreshErr != nil {
				if !forced && errors.Is(refreshErr, ErrAuthorizationUnavailable) &&
					staleReadAllowed(method, lockedNow, current.AuthorizationCheckedAt, a.options.AuthorizationMaxStale, current.TokenExpiresAt) {
					current.RefreshRetryAt = lockedNow.Add(authorizationRefreshRetryInterval)
					current.LastSeenAt = lockedNow
					return current, true, nil
				}
				return current, false, refreshErr
			}
			principal = refreshed
			updated.LastSeenAt = lockedNow
			updated.RefreshRetryAt = time.Time{}
			return updated, true, nil
		})
	if errors.Is(err, ErrAuthorizationDenied) || errors.Is(err, ErrInvalidAuthorization) {
		_ = a.store.RevokeSessionsForIdentity(ctx, stored.TenantID, stored.IdentityID, a.now().UTC())
		return Principal{}, err
	}
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, errOIDCRecordNotFound) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, ErrAuthorizationUnavailable
	}
	return principal, nil
}
