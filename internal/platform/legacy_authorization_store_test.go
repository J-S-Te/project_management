package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestLegacyGORMBackoffCheckpointDoesNotOverrideFreshAuthorization 验证真实存储保存退避并由强制新授权清除。
// 参数：t为独立内存SQLite与生产GORM会话存储测试上下文，不使用外部DSN。
// 返回值：无；退避未保存、强制新权限未持久化或原期限改变时失败。
func TestLegacyGORMBackoffCheckpointDoesNotOverrideFreshAuthorization(t *testing.T) {
	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if err := db.AutoMigrate(&oidcSessionRecord{}); err != nil {
		t.Fatal(err)
	}
	store := NewGORMOIDCStore(db)
	record, wide := storedPrincipal(t, now)
	record.SessionIDHash = tokenDigest("same-cookie")
	record.AuthorizationRevision = wide.AuthorizationRevision
	record.IDTokenCipher = []byte("synthetic-id-cipher")
	record.OAuthTokenCipher = []byte("synthetic-token-cipher")
	if err := store.CreateSession(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	failed, calls := true, 0
	auth := &OIDCAuthenticator{options: OIDCOptions{TenantID: "tenant-1", SessionCookieName: "legacy_session", SessionIdleTTL: 30 * time.Minute, AuthorizationRefreshInterval: time.Minute, AuthorizationMaxStale: 5 * time.Minute}, store: store, now: func() time.Time { return now }}
	auth.refreshAuthorization = func(_ context.Context, current StoredOIDCSession, checked time.Time, _ string) (StoredOIDCSession, Principal, error) {
		calls++
		if failed {
			return current, Principal{}, ErrAuthorizationUnavailable
		}
		limited := wide
		limited.Permissions = map[string]bool{"project.task.read": true}
		limited.DataScopes = []DataScope{{RoleCode: "limited", ScopeType: "SELF", ScopeID: "identity-1"}}
		current.PrincipalJSON, _ = json.Marshal(limited)
		current.AuthorizationCheckedAt = checked
		return current, limited, nil
	}
	request := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: "legacy_session", Value: "same-cookie"})
	if _, err := auth.Authenticate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	stored, err := store.FindSession(context.Background(), record.SessionIDHash, now)
	if err != nil || !stored.RefreshRetryAt.Equal(now.Add(authorizationRefreshRetryInterval)) {
		t.Fatalf("retry checkpoint missing: retry=%v error=%v", stored.RefreshRetryAt, err)
	}
	failed = false
	request.Header.Set("X-Authorization-Refresh", "1")
	if _, err := auth.Authenticate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	stored, err = store.FindSession(context.Background(), record.SessionIDHash, now)
	principal, decodeErr := decodePrincipal(stored.PrincipalJSON)
	if err != nil || decodeErr != nil || !stored.RefreshRetryAt.IsZero() || !principal.Has("project.task.read") || len(principal.DataScopes) != 1 || principal.DataScopes[0].ScopeType != "SELF" || calls != 2 {
		t.Fatalf("fresh checkpoint not saved: principal=%+v retry=%v calls=%d error=%v decode=%v", principal, stored.RefreshRetryAt, calls, err, decodeErr)
	}
	if !stored.SessionExpiresAt.Equal(record.SessionExpiresAt) || !stored.LastInteractiveAt.Equal(record.LastInteractiveAt) {
		t.Fatal("legacy transaction changed original lifecycle")
	}
}
