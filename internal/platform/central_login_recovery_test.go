package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	central "github.com/j-s-te/project-management/internal/sessiongovernance"
	"golang.org/x/oauth2"
)

type recoveryLoginStore struct{ fakeOIDCStore }

// ConsumeLoginTransaction 模拟真实的一次性事务消费，不允许重放旧state。
// 参数：上下文未使用，hash为回调state摘要，now为当前时钟。
// 返回值：未消费事务；未知、重放或过期时返回不存在。
func (s *recoveryLoginStore) ConsumeLoginTransaction(_ context.Context, hash []byte, now time.Time) (LoginTransaction, error) {
	if !bytes.Equal(hash, s.login.StateHash) || s.login.ConsumedAt != nil || !s.login.ExpiresAt.After(now) {
		return LoginTransaction{}, errOIDCRecordNotFound
	}
	tx := s.login
	s.login.ConsumedAt = &now
	return tx, nil
}

type recoveryCentralClient struct {
	central.Client
	err error
}

// Complete 返回已分类的中央完成结果，测试不会创建任何真实会话。
// 参数：上下文、票据和令牌为本地受控测试材料。
// 返回值：配置的失效、冲突或暂时故障。
func (c recoveryCentralClient) Complete(context.Context, string, string, string) (central.Binding, error) {
	return central.Binding{}, c.err
}

// BeginURL 为新state生成受控中央入口，保持真实Login路径。
// 参数：state为新的随机状态，nonce由真实Login生成但本替身不消费。
// 返回值：仅用于单测的中央入口。
func (c recoveryCentralClient) BeginURL(state, _ string) string {
	return "https://central.test/begin?state=" + url.QueryEscape(state)
}

type recoveryKeySet struct{}

// VerifySignature 仅在单测中解出受控JWT负载，正式验签器不受影响。
// 参数：上下文未使用，raw为本用例生成的紧凑JWT。
// 返回值：测试声明；格式不合法时返回错误。
func (recoveryKeySet) VerifySignature(_ context.Context, raw string) ([]byte, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid fixture token")
	}
	return base64.RawURLEncoding.DecodeString(parts[1])
}

// newRecoveryAuthenticator 使用真实Callback及Login流程配合本地令牌端点。
// 参数：t为测试上下文，failure为中央已分类错误，forced表示已强制重登过。
// 返回值：认证器及一次性事务夹具，不访问真实OIDC或数据库。
func newRecoveryAuthenticator(t *testing.T, failure error, forced bool) (*OIDCAuthenticator, *recoveryLoginStore) {
	t.Helper()
	codec, err := newSecretCodec(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	nonce, _ := codec.encrypt([]byte("fixture-nonce"))
	verifier, _ := codec.encrypt([]byte("fixture-verifier"))
	ticket, _ := codec.encrypt([]byte("fixture-ticket"))
	store := &recoveryLoginStore{fakeOIDCStore{login: LoginTransaction{
		TenantID: "tenant-1", StateHash: tokenDigest("fixture-state"),
		NonceCipher: nonce, CodeVerifierCipher: verifier, CentralTicketCipher: ticket,
		ForceLogin: forced, ReturnPath: "/implementation?filter=mine", ExpiresAt: now.Add(time.Minute),
	}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		currentNonce, _ := codec.decrypt(store.login.NonceCipher)
		claims, _ := json.Marshal(map[string]any{
			"iss": "https://issuer.test", "aud": "project-client", "sub": "identity-1",
			"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": string(currentNonce),
			"identity_id": "identity-1", "tenant_id": "tenant-1", "token_use": "id_token",
			"preferred_username": "fixture-user", "sid": "fixture-upstream",
		})
		raw := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
			base64.RawURLEncoding.EncodeToString(claims) + ".c2ln"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access",
			"token_type": "Bearer", "expires_in": 3600, "id_token": raw})
	}))
	t.Cleanup(server.Close)
	auth := &OIDCAuthenticator{store: store, codec: codec, now: func() time.Time { return now },
		httpClient: server.Client(), verifier: oidc.NewVerifier("https://issuer.test", recoveryKeySet{},
			&oidc.Config{ClientID: "project-client"}),
		options: OIDCOptions{TenantID: "tenant-1", PathPrefix: "/project_management",
			SessionCookieName: "project-session", CentralClient: recoveryCentralClient{err: failure}},
		oauth: oauth2.Config{ClientID: "project-client", Endpoint: oauth2.Endpoint{
			TokenURL: server.URL, AuthURL: "https://issuer.test/auth",
			AuthStyle: oauth2.AuthStyleInParams}}}
	return auth, store
}

// recoveryCallbackRequest 构造与当前事务同浏览器的真实回调请求。
// 参数：state为当前随机状态。
// 返回值：带同值HttpOnly状态Cookie的请求，不保存任何正式令牌。
func recoveryCallbackRequest(state string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+url.QueryEscape(state)+"&code=fixture", nil)
	r.AddCookie(&http.Cookie{Name: "project-session_oidc_state", Value: state})
	return r
}

// TestCentralLoginRecoveryIsBounded 验证旧周期只允许一次完整重登且临时故障不跳转。
// 参数：t为本地HTTP单测上下文，覆盖明确失效、冲突及依赖失败。
// 返回值：无；重复跳转、绕过新state/PKCE或错误创建会话时失败。
func TestCentralLoginRecoveryIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		forced bool
		status int
	}{{"inactive", central.ErrInactive, false, http.StatusSeeOther},
		{"already-forced", central.ErrInactive, true, http.StatusUnauthorized},
		{"conflict", central.ErrBindingConflict, false, http.StatusUnauthorized},
		{"outage", central.ErrUnavailable, false, http.StatusServiceUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			auth, store := newRecoveryAuthenticator(t, tc.err, tc.forced)
			response := httptest.NewRecorder()
			auth.Callback(response, recoveryCallbackRequest("fixture-state"))
			if response.Code != tc.status || store.calls != 0 {
				t.Fatalf("status=%d want=%d sessions=%d body=%s", response.Code,
					tc.status, store.calls, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("recovery must retain the callback cache and referrer protections")
			}
			if tc.status != http.StatusSeeOther {
				if response.Header().Get("Location") != "" {
					t.Fatal("conflict, forced retry or outage must not redirect")
				}
				return
			}
			target, err := url.Parse(response.Header().Get("Location"))
			if err != nil || target.Path != "/project_management/auth/login" ||
				target.Query().Get("prompt") != "login" ||
				target.Query().Get("return_to") != "/implementation?filter=mine" {
				t.Fatal("recovery must preserve the local destination and require fresh login")
			}
			replayed := httptest.NewRecorder()
			auth.Callback(replayed, recoveryCallbackRequest("fixture-state"))
			if replayed.Code != http.StatusUnauthorized || replayed.Header().Get("Location") != "" {
				t.Fatal("recovery must not reuse the consumed callback state")
			}
			auth.Login(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target.String(), nil))
			if !store.login.ForceLogin || bytes.Equal(store.login.StateHash, tokenDigest("fixture-state")) {
				t.Fatal("recovery must persist its retry limit and create a fresh state")
			}
			nonce, _ := auth.codec.decrypt(store.login.NonceCipher)
			verifier, _ := auth.codec.decrypt(store.login.CodeVerifierCipher)
			upstream, _ := url.Parse(auth.authorizationURL("new-state", string(nonce),
				string(verifier), store.login.ForceLogin))
			if upstream.Query().Get("prompt") != "login" || upstream.Query().Get("max_age") != "0" ||
				upstream.Query().Get("code_challenge") == "" {
				t.Fatal("central continuation must require fresh upstream authentication and PKCE")
			}
		})
	}
}
