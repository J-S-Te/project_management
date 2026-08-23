package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ErrInvalidServiceToken is deliberately distinct from a browser session
// error: callers must never fall back to routing headers when a bearer token
// was supplied but cannot be verified.
var ErrInvalidServiceToken = errors.New("invalid service token")

// ClientCredentialsTokenVerifier verifies the Keycloak access token used by a
// trusted service-to-service caller. It verifies signature, issuer, expiry,
// token type, authorized party and audience; it does not accept an ID token.
type ClientCredentialsTokenVerifier interface {
	VerifyClientCredentials(context.Context, string) (ServiceTokenIdentity, error)
}

type ClientCredentialsVerifierOptions struct {
	Issuer, BackchannelBaseURL, ClientID, Audience         string
	TenantID, CallerApplicationCode, CallerEnvironmentCode string
	Timeout                                                time.Duration
}

// ServiceTokenIdentity 是经过签名、issuer、audience 及调用方绑定校验后的机器身份。
// 路由层必须使用这里的租户，不能再从可由请求方任意填写的请求头推导租户边界。
type ServiceTokenIdentity struct {
	TenantID        string
	ApplicationCode string
	EnvironmentCode string
}

type keycloakClientCredentialsVerifier struct {
	verifier              *oidc.IDTokenVerifier
	clientID              string
	audience              string
	tenantID              string
	callerApplicationCode string
	callerEnvironmentCode string
}

type serviceTokenClaims struct {
	AuthorizedParty string `json:"azp"`
	ClientID        string `json:"client_id"`
	Type            string `json:"typ"`
	TokenUse        string `json:"token_use"`
	TenantID        string `json:"tenant_id"`
	ApplicationCode string `json:"application_code"`
	EnvironmentCode string `json:"environment_code"`
}

func NewClientCredentialsTokenVerifier(ctx context.Context, options ClientCredentialsVerifierOptions) (ClientCredentialsTokenVerifier, error) {
	issuer := strings.TrimRight(strings.TrimSpace(options.Issuer), "/")
	backchannel := strings.TrimRight(strings.TrimSpace(options.BackchannelBaseURL), "/")
	clientID := strings.TrimSpace(options.ClientID)
	audience := strings.TrimSpace(options.Audience)
	tenantID := strings.TrimSpace(options.TenantID)
	callerApplicationCode := strings.TrimSpace(options.CallerApplicationCode)
	callerEnvironmentCode := strings.TrimSpace(options.CallerEnvironmentCode)
	if issuer == "" || clientID == "" || audience == "" || tenantID == "" || callerApplicationCode == "" || callerEnvironmentCode == "" {
		return nil, fmt.Errorf("%w: issuer, client ID, audience, tenant, caller application and caller environment are required", ErrInvalidServiceToken)
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Second
	}
	client := &http.Client{Timeout: options.Timeout}
	if backchannel != "" {
		publicURL, err := url.Parse(issuer)
		if err != nil {
			return nil, fmt.Errorf("%w: issuer: %v", ErrInvalidServiceToken, err)
		}
		backchannelURL, err := url.Parse(backchannel)
		if err != nil {
			return nil, fmt.Errorf("%w: backchannel: %v", ErrInvalidServiceToken, err)
		}
		client.Transport = &backchannelTransport{base: http.DefaultTransport, public: publicURL, backchannel: backchannelURL}
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: load discovery: %v", ErrInvalidServiceToken, err)
	}
	// Access tokens are issued for the machine-client audience, not the
	// browser RP Client ID. The audience is validated explicitly below.
	return &keycloakClientCredentialsVerifier{
		verifier:              provider.Verifier(&oidc.Config{SkipClientIDCheck: true}),
		clientID:              clientID,
		audience:              audience,
		tenantID:              tenantID,
		callerApplicationCode: callerApplicationCode,
		callerEnvironmentCode: callerEnvironmentCode,
	}, nil
}

func (v *keycloakClientCredentialsVerifier) VerifyClientCredentials(ctx context.Context, rawToken string) (ServiceTokenIdentity, error) {
	if v == nil || v.verifier == nil || strings.TrimSpace(rawToken) == "" {
		return ServiceTokenIdentity{}, ErrInvalidServiceToken
	}
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return ServiceTokenIdentity{}, fmt.Errorf("%w: signature, issuer or expiry: %v", ErrInvalidServiceToken, err)
	}
	claims := serviceTokenClaims{}
	if err := token.Claims(&claims); err != nil {
		return ServiceTokenIdentity{}, fmt.Errorf("%w: claims: %v", ErrInvalidServiceToken, err)
	}
	if err := v.validateClaims(claims, token.Audience); err != nil {
		return ServiceTokenIdentity{}, err
	}
	return ServiceTokenIdentity{TenantID: v.tenantID, ApplicationCode: v.callerApplicationCode, EnvironmentCode: v.callerEnvironmentCode}, nil
}

func (v *keycloakClientCredentialsVerifier) validateClaims(claims serviceTokenClaims, audiences []string) error {
	if !strings.EqualFold(strings.TrimSpace(claims.Type), "bearer") {
		return fmt.Errorf("%w: token typ is not bearer", ErrInvalidServiceToken)
	}
	// Keycloak service-account access tokens omit token_use unless a custom
	// mapper is installed. If present it must still identify an access token.
	if tokenUse := strings.TrimSpace(claims.TokenUse); tokenUse != "" && tokenUse != "access_token" {
		return fmt.Errorf("%w: token_use is not access_token", ErrInvalidServiceToken)
	}
	// azp is the authenticated Keycloak caller.  Do not fall back to client_id:
	// the latter is an optional mapper and is not the authorized-party contract.
	if strings.TrimSpace(claims.AuthorizedParty) != v.clientID || !containsAudience(audiences, v.audience) {
		return fmt.Errorf("%w: authorized party or audience", ErrInvalidServiceToken)
	}
	if clientID := strings.TrimSpace(claims.ClientID); clientID != "" && clientID != v.clientID {
		return fmt.Errorf("%w: client_id", ErrInvalidServiceToken)
	}
	// tenant/application/environment 由服务端固定配置绑定；若令牌安装了对应
	// mapper，则 claim 必须与绑定一致。默认 Keycloak service-account token 不含
	// 这些 claim，不能因此拒绝一个已通过签名、issuer、azp、audience 校验的调用方。
	if tenantID := strings.TrimSpace(claims.TenantID); tenantID != "" && tenantID != v.tenantID {
		return fmt.Errorf("%w: tenant", ErrInvalidServiceToken)
	}
	if applicationCode := strings.TrimSpace(claims.ApplicationCode); applicationCode != "" && applicationCode != v.callerApplicationCode {
		return fmt.Errorf("%w: caller application", ErrInvalidServiceToken)
	}
	if environmentCode := strings.TrimSpace(claims.EnvironmentCode); environmentCode != "" && environmentCode != v.callerEnvironmentCode {
		return fmt.Errorf("%w: tenant, caller application or caller environment", ErrInvalidServiceToken)
	}
	return nil
}

func containsAudience(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
