package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type logoutVerifierStub struct {
	command BackchannelLogout
	err     error
	raw     string
}

func (stub *logoutVerifierStub) VerifyBackchannelLogout(_ context.Context, raw string, _ time.Time) (BackchannelLogout, error) {
	stub.raw = raw
	return stub.command, stub.err
}

type backchannelStoreStub struct {
	*fakeOIDCStore
	command BackchannelLogout
	err     error
}

func (stub *backchannelStoreStub) ConsumeBackchannelLogout(_ context.Context, command BackchannelLogout, _ time.Time) error {
	stub.command = command
	return stub.err
}

func TestBackchannelLogoutConsumesVerifiedToken(t *testing.T) {
	now := time.Date(2026, time.August, 28, 8, 0, 0, 0, time.UTC)
	command := BackchannelLogout{TenantID: "tenant-1", SessionID: "sid-1", JTI: "jti-1", ExpiresAt: now.Add(time.Minute)}
	verifier := &logoutVerifierStub{command: command}
	store := &backchannelStoreStub{fakeOIDCStore: &fakeOIDCStore{}}
	authenticator := &OIDCAuthenticator{store: store, backchannelVerifier: verifier, now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodPost, "/auth/backchannel-logout", strings.NewReader(url.Values{"logout_token": {"signed-token"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	authenticator.BackchannelLogout(response, request)

	if response.Code != http.StatusNoContent || verifier.raw != "signed-token" || store.command.JTI != command.JTI {
		t.Fatalf("status=%d raw=%q command=%#v", response.Code, verifier.raw, store.command)
	}
}

func TestBackchannelLogoutTreatsReplayAsIdempotentSuccess(t *testing.T) {
	now := time.Now().UTC()
	store := &backchannelStoreStub{fakeOIDCStore: &fakeOIDCStore{}, err: errOIDCBackchannelReplay}
	authenticator := &OIDCAuthenticator{
		store: store, now: func() time.Time { return now },
		backchannelVerifier: &logoutVerifierStub{command: BackchannelLogout{JTI: "replayed"}},
	}
	request := httptest.NewRequest(http.MethodPost, "/auth/backchannel-logout", strings.NewReader("logout_token=token"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	authenticator.BackchannelLogout(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestValidateLogoutProtocolClaims(t *testing.T) {
	now := time.Date(2026, time.August, 28, 8, 0, 0, 0, time.UTC)
	valid := logoutTokenClaims{
		Subject: "subject-1", JTI: "jti-1", IssuedAt: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(time.Minute).Unix(),
		Events: map[string]json.RawMessage{oidcBackchannelLogoutEvent: json.RawMessage(`{}`)},
	}
	if err := validateLogoutProtocolClaims(valid, now, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*logoutTokenClaims){
		"missing subject and sid": func(value *logoutTokenClaims) { value.Subject = "" },
		"nonce":                   func(value *logoutTokenClaims) { value.Nonce = json.RawMessage(`"nonce"`) },
		"missing event":           func(value *logoutTokenClaims) { value.Events = nil },
		"invalid event": func(value *logoutTokenClaims) {
			value.Events = map[string]json.RawMessage{oidcBackchannelLogoutEvent: json.RawMessage(`[]`)}
		},
		"long ttl":    func(value *logoutTokenClaims) { value.ExpiresAt = now.Add(10 * time.Minute).Unix() },
		"expired":     func(value *logoutTokenClaims) { value.ExpiresAt = now.Add(-time.Second).Unix() },
		"missing jti": func(value *logoutTokenClaims) { value.JTI = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if err := validateLogoutProtocolClaims(value, now, 2*time.Minute); err == nil {
				t.Fatal("invalid logout claims were accepted")
			}
		})
	}
}

func TestBackchannelLogoutMapsVerifierAndStoreFailures(t *testing.T) {
	for name, testCase := range map[string]struct {
		verifierErr error
		storeErr    error
		want        int
	}{
		"invalid token": {errors.New("bad signature"), nil, http.StatusBadRequest},
		"database":      {nil, errors.New("database unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			authenticator := &OIDCAuthenticator{
				store: &backchannelStoreStub{fakeOIDCStore: &fakeOIDCStore{}, err: testCase.storeErr}, now: time.Now,
				backchannelVerifier: &logoutVerifierStub{command: BackchannelLogout{JTI: "jti"}, err: testCase.verifierErr},
			}
			request := httptest.NewRequest(http.MethodPost, "/auth/backchannel-logout", strings.NewReader("logout_token=token"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			authenticator.BackchannelLogout(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
