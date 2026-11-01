package platform

import (
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// classifyRefreshError 区分确定失效与网络、提供方及签名材料的暂不可用。
// 参数：err为刷新返回的协议或依赖错误。
// 返回值：仅明确拒绝为未认证，其他错误维持依赖不可用且不泄漏响应正文。
func classifyRefreshError(err error) error {
	if errors.Is(err, ErrUnauthenticated) {
		return ErrUnauthenticated
	}
	var protocol *oauth2.RetrieveError
	if errors.As(err, &protocol) && (protocol.ErrorCode == "invalid_grant" || protocol.ErrorCode == "invalid_token") {
		return ErrUnauthenticated
	}
	return ErrAuthorizationUnavailable
}

// authorizationURL 在桥接后复用原始state、nonce与PKCE，不重建认证事务。
// 参数：state/nonce/verifier为原始事务秘密，forceLogin要求提供方新鲜认证。
// 返回值：受固定提供方和登记回调约束的OIDC地址。
func (a *OIDCAuthenticator) authorizationURL(state, nonce, verifier string, forceLogin bool) string {
	options := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("claims", `{"id_token":{"auth_time":{"essential":true}}}`)}
	if forceLogin {
		options = append(options, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	if a.options.IdentityProviderHint != "" {
		options = append(options, oauth2.SetAuthURLParam("kc_idp_hint", a.options.IdentityProviderHint))
	}
	return a.oauth.AuthCodeURL(state, options...)
}
