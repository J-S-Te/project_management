package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"
)

const oidcBackchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// BackchannelLogout 是通过验签和协议校验后交给持久层的注销命令。
type BackchannelLogout struct {
	TenantID, Issuer, Audience, Subject, SessionID, JTI string
	ExpiresAt                                           time.Time
}

type backchannelLogoutStore interface {
	ConsumeBackchannelLogout(context.Context, BackchannelLogout, time.Time) error
}

type backchannelLogoutVerifier interface {
	VerifyBackchannelLogout(context.Context, string, time.Time) (BackchannelLogout, error)
}

type oidcProviderLogoutVerifier struct {
	authenticator *OIDCAuthenticator
	maxTTL        time.Duration
}

type logoutTokenClaims struct {
	Issuer    string                     `json:"iss"`
	Subject   string                     `json:"sub"`
	Audience  json.RawMessage            `json:"aud"`
	IssuedAt  int64                      `json:"iat"`
	ExpiresAt int64                      `json:"exp"`
	JTI       string                     `json:"jti"`
	SessionID string                     `json:"sid"`
	Events    map[string]json.RawMessage `json:"events"`
	Nonce     json.RawMessage            `json:"nonce"`
}

// VerifyBackchannelLogout 使用浏览器 OIDC Provider 的同一 JWKS、issuer 和 client_id
// 验证 logout_token，再补充 Back-Channel Logout 专属声明约束。
func (v oidcProviderLogoutVerifier) VerifyBackchannelLogout(ctx context.Context, raw string, now time.Time) (BackchannelLogout, error) {
	if v.authenticator == nil || v.authenticator.verifier == nil || v.maxTTL <= 0 {
		return BackchannelLogout{}, errors.New("back-channel logout verifier is not configured")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return BackchannelLogout{}, errors.New("logout token serialization is invalid")
	}
	var header struct {
		Type string `json:"typ"`
	}
	encodedHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(encodedHeader, &header) != nil || header.Type != "logout+jwt" {
		return BackchannelLogout{}, errors.New("logout token type is invalid")
	}
	verified, err := v.authenticator.verifier.Verify(ctx, raw)
	if err != nil {
		return BackchannelLogout{}, fmt.Errorf("verify logout token signature: %w", err)
	}
	var claims logoutTokenClaims
	if err = verified.Claims(&claims); err != nil {
		return BackchannelLogout{}, errors.New("logout token claims are invalid")
	}
	claims.Subject = strings.TrimSpace(claims.Subject)
	claims.SessionID = strings.TrimSpace(claims.SessionID)
	claims.JTI = strings.TrimSpace(claims.JTI)
	if err = validateLogoutProtocolClaims(claims, now, v.maxTTL); err != nil {
		return BackchannelLogout{}, err
	}
	return BackchannelLogout{
		TenantID: v.authenticator.options.TenantID, Issuer: strings.TrimRight(v.authenticator.options.Issuer, "/"),
		Audience: v.authenticator.options.ClientID, Subject: claims.Subject, SessionID: claims.SessionID,
		JTI: claims.JTI, ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(),
	}, nil
}

func validateLogoutProtocolClaims(claims logoutTokenClaims, now time.Time, maxTTL time.Duration) error {
	claims.Subject = strings.TrimSpace(claims.Subject)
	claims.SessionID = strings.TrimSpace(claims.SessionID)
	claims.JTI = strings.TrimSpace(claims.JTI)
	issuedAt := time.Unix(claims.IssuedAt, 0).UTC()
	expiresAt := time.Unix(claims.ExpiresAt, 0).UTC()
	event, hasEvent := claims.Events[oidcBackchannelLogoutEvent]
	var eventObject map[string]any
	validEvent := hasEvent && json.Unmarshal(event, &eventObject) == nil && eventObject != nil
	if claims.JTI == "" || len(claims.JTI) > 128 || claims.Subject == "" && claims.SessionID == "" ||
		len(claims.Subject) > 128 || len(claims.SessionID) > 128 || claims.Nonce != nil || !validEvent || claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt ||
		maxTTL <= 0 || expiresAt.Sub(issuedAt) > maxTTL || !expiresAt.After(now.UTC()) || issuedAt.After(now.UTC().Add(time.Minute)) {
		return errors.New("logout token protocol claims are invalid")
	}
	return nil
}

// BackchannelLogout 接收 OP 的标准 logout_token。该端点不读取浏览器 Cookie，也不执行重定向。
func (a *OIDCAuthenticator) BackchannelLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaTypeErr != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") {
		http.Error(w, "invalid content type", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid logout request", http.StatusBadRequest)
		return
	}
	logoutTokens := r.PostForm["logout_token"]
	raw := ""
	if len(logoutTokens) == 1 {
		raw = strings.TrimSpace(logoutTokens[0])
	}
	verifier := a.backchannelVerifier
	store, ok := a.store.(backchannelLogoutStore)
	if raw == "" || verifier == nil || !ok {
		http.Error(w, "invalid logout request", http.StatusBadRequest)
		return
	}
	now := a.now().UTC()
	command, err := verifier.VerifyBackchannelLogout(r.Context(), raw, now)
	if err != nil {
		http.Error(w, "invalid logout token", http.StatusBadRequest)
		return
	}
	if err = store.ConsumeBackchannelLogout(r.Context(), command, now); err != nil {
		if errors.Is(err, errOIDCBackchannelReplay) {
			// OP 在首次响应丢失后会使用同一 Outbox/JTI 重试。首次事务已经撤销会话，
			// 因此重复投递必须幂等成功，避免平台把已完成注销误判为永久失败。
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "session service is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
