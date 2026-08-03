package platform

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func validClaims() oidcClaims {
	return oidcClaims{Subject: "user-1", TenantID: "tenant-1", Roles: []string{"project_manager"}, Permissions: []string{"project.read", "service_item.confirm"}, RoleConfigHash: "hash-1", AuthzRevision: 1}
}

func TestPrincipalFromClaims(t *testing.T) {
	principal, err := principalFromClaims(validClaims(), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != "user-1" || !principal.Has("project.read") {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestPrincipalRejectsWrongTenantAndWildcard(t *testing.T) {
	if _, err := principalFromClaims(validClaims(), "tenant-2"); err == nil {
		t.Fatal("wrong tenant was accepted")
	}
	claims := validClaims()
	claims.Permissions = []string{"all"}
	if _, err := principalFromClaims(claims, "tenant-1"); err == nil {
		t.Fatal("wildcard permission was accepted")
	}
}

func TestPrincipalRejectsMalformedAuthorizationMetadata(t *testing.T) {
	claims := validClaims()
	claims.Roles = []string{" project_manager "}
	if _, err := principalFromClaims(claims, "tenant-1"); err == nil {
		t.Fatal("malformed role was accepted")
	}
	claims = validClaims()
	claims.AuthzRevision = 0
	if _, err := principalFromClaims(claims, "tenant-1"); err == nil {
		t.Fatal("missing revision was accepted")
	}
}

func TestAuthenticationBacksOffAfterTransientRefreshFailure(t *testing.T) {
	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	refreshCalls := 0
	authenticator := &OIDCAuthenticator{
		options: OIDCOptions{
			SessionCookieName: "project_session", SessionTTL: time.Hour,
			AuthorizationRefreshInterval: time.Minute,
		},
		now:          func() time.Time { return now },
		transactions: map[string]transaction{},
		sessions: map[string]*session{
			"session-1": {
				Principal:   Principal{TenantID: "tenant-1", UserID: "user-1"},
				RefreshedAt: now.Add(-2 * time.Minute), TokenExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(time.Hour),
			},
		},
		refresh: func(context.Context, *session, time.Time) error {
			refreshCalls++
			return errors.New("platform temporarily unavailable")
		},
	}
	request := httptest.NewRequest("GET", "/api/v1/projects", nil)
	request.AddCookie(authenticator.cookie("session-1", now.Add(time.Hour)))

	if _, err := authenticator.Authenticate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := authenticator.Authenticate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1 during retry backoff", refreshCalls)
	}
}
