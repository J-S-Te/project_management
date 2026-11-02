package platform

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"golang.org/x/oauth2"
)

// centralAuthenticationError 保留确定失效与暂时故障的边界。
// 参数：err为中央校验、令牌或权限查询返回的错误。
// 返回值：明确拒绝为未认证，其他失败为依赖暂不可用。
func centralAuthenticationError(err error) error {
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrAuthorizationDenied) || errors.Is(err, ErrInvalidAuthorization) {
		return ErrUnauthenticated
	}
	return ErrAuthorizationUnavailable
}

// rotateCentralTokens 在第一事务持锁重读，保存验签且身份不变的新凭据。
// 参数：ctx为请求，stored为锁内最新项目会话，now为服务器时钟。
// 返回值：待提交记录和变更标记；权限查询放在提交后的第二事务。
func (a *OIDCAuthenticator) rotateCentralTokens(ctx context.Context, stored StoredOIDCSession, now time.Time) (StoredOIDCSession, bool, error) {
	if _, err := a.centralState(ctx, stored, false, false); err != nil {
		return stored, false, err
	}
	if stored.TokenExpiresAt.After(now.Add(time.Minute)) {
		return stored, false, nil
	}
	data, err := a.codec.decrypt(stored.OAuthTokenCipher)
	if err != nil {
		return stored, false, ErrUnauthenticated
	}
	var old oauth2.Token
	if json.Unmarshal(data, &old) != nil {
		return stored, false, ErrUnauthenticated
	}
	principal, err := decodePrincipal(stored.PrincipalJSON)
	if err != nil {
		return stored, false, ErrUnauthenticated
	}
	expected := compactIdentity{Subject: principal.Subject, IdentityID: stored.IdentityID, TenantID: stored.TenantID, PersonID: stored.PersonID}
	token, rawID, id, claims, _, err := a.refreshTokens(ctx, &old, expected)
	if err != nil {
		return stored, false, classifyRefreshError(err)
	}
	if stored.OIDCSessionID == "" || claims.SessionID != stored.OIDCSessionID {
		return stored, false, ErrUnauthenticated
	}
	tokenData, err := json.Marshal(&token)
	if err != nil {
		return stored, false, ErrAuthorizationUnavailable
	}
	tokenCipher, err := a.codec.encrypt(tokenData)
	if err != nil {
		return stored, false, ErrAuthorizationUnavailable
	}
	idCipher, err := a.codec.encrypt(rawID)
	if err != nil {
		return stored, false, ErrAuthorizationUnavailable
	}
	stored.OAuthTokenCipher, stored.IDTokenCipher = tokenCipher, idCipher
	stored.TokenExpiresAt = earliestTime(id.Expiry, token.Expiry)
	stored.AuthorizationCheckedAt = time.Unix(1, 0).UTC()
	stored.RefreshRetryAt = time.Time{}
	return stored, true, nil
}
