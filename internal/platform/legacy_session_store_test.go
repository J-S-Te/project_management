package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// WithSessionLock 在内存替身中提供原子会话重读、复核和提交能力。
// 参数：ctx为请求，hash为Cookie摘要，now为时钟，update为生产授权复核逻辑。
// 返回值：最新会话，明确失效提交撤销，依赖错误不提交回调结果。
func (s *fakeOIDCStore) WithSessionLock(ctx context.Context, hash []byte, now time.Time,
	update sessionUpdateFunc) (StoredOIDCSession, error) {
	s.legacyRowMu.Lock()
	defer s.legacyRowMu.Unlock()
	current, err := s.FindSession(ctx, hash, now)
	if err != nil {
		return current, err
	}
	updated, changed, err := update(current)
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrAuthorizationDenied) || errors.Is(err, ErrInvalidAuthorization) {
		s.legacyStateMu.Lock()
		s.session.RevokedAt = &now
		s.legacyStateMu.Unlock()
		return current, err
	}
	if err != nil {
		return current, err
	}
	if changed {
		if err := s.UpdateSession(ctx, updated, now); err != nil {
			return current, err
		}
	}
	return s.FindSession(ctx, hash, now)
}

// TestLegacyRefreshFailsClosedWithoutAtomicStore 验证旧存储缺少原子复核能力时不回退无锁写入。
// 参数：t为隐藏扩展锁接口的真实认证器测试上下文。
// 返回值：无；执行了无锁刷新、返回旧成功或修改存储时失败。
func TestLegacyRefreshFailsClosedWithoutAtomicStore(t *testing.T) {
	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	record, _ := storedPrincipal(t, now)
	source := &fakeOIDCStore{session: record}
	store := &struct{ OIDCStore }{OIDCStore: source}
	calls := 0
	auth := &OIDCAuthenticator{options: OIDCOptions{TenantID: "tenant-1", SessionCookieName: "legacy_session", SessionIdleTTL: 30 * time.Minute, AuthorizationRefreshInterval: time.Minute, AuthorizationMaxStale: 5 * time.Minute}, store: store, now: func() time.Time { return now }, refreshAuthorization: func(context.Context, StoredOIDCSession, time.Time, string) (StoredOIDCSession, Principal, error) {
		calls++
		return record, Principal{}, nil
	}}
	request := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	request.Header.Set("X-Authorization-Refresh", "1")
	request.AddCookie(&http.Cookie{Name: "legacy_session", Value: "same-cookie"})
	if principal, err := auth.Authenticate(context.Background(), request); !errors.Is(err, ErrAuthorizationUnavailable) || len(principal.Permissions) != 0 || calls != 0 || source.updated {
		t.Fatalf("unsafe fallback: principal=%+v calls=%d updated=%t error=%v", principal, calls, source.updated, err)
	}
}
