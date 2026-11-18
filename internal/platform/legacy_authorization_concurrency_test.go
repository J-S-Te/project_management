package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type legacyConcurrentKey struct{}
type legacyConcurrentStore struct {
	*fakeOIDCStore
	stateMu, rowMu sync.Mutex
	secondLock     chan struct{}
	lockSignal     sync.Once
}

// FindSession 为并发认证返回独立的最新会话快照。
// 参数：ctx为请求，hash为Cookie摘要，now为有效性时钟。
// 返回值：内存存储快照或失效错误，不访问外部数据库。
func (s *legacyConcurrentStore) FindSession(ctx context.Context, hash []byte, now time.Time) (StoredOIDCSession, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.fakeOIDCStore.FindSession(ctx, hash, now)
}

// UpdateSession 原子提交真实认证器产生的会话快照。
// 参数：ctx为请求，record为已复核快照，now为提交时钟。
// 返回值：内存写回结果。
func (s *legacyConcurrentStore) UpdateSession(ctx context.Context, record StoredOIDCSession, now time.Time) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.fakeOIDCStore.UpdateSession(ctx, record, now)
}

// TouchSessionLastSeen 串行更新观测时间，不修改授权快照。
// 参数：ctx为请求，hash为Cookie摘要，now为观测时钟。
// 返回值：内存更新时间的结果。
func (s *legacyConcurrentStore) TouchSessionLastSeen(ctx context.Context, hash []byte, now time.Time) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.fakeOIDCStore.TouchSessionLastSeen(ctx, hash, now)
}

// WithSessionLock 在会话行锁内重读、复核并提交，所有授权写回共享同一边界。
// 参数：ctx为请求，hash和now限定会话，update为生产认证器的授权复核操作。
// 返回值：最新提交快照；错误不提交回调结果。
func (s *legacyConcurrentStore) WithSessionLock(ctx context.Context, hash []byte, now time.Time, update sessionUpdateFunc) (StoredOIDCSession, error) {
	if ctx.Value(legacyConcurrentKey{}) == "fresh" {
		s.lockSignal.Do(func() { close(s.secondLock) })
	}
	s.rowMu.Lock()
	defer s.rowMu.Unlock()
	current, err := s.FindSession(ctx, hash, now)
	if err != nil {
		return current, err
	}
	updated, changed, err := update(current)
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

// TestLegacyConcurrentAuthorizationNeverRestoresWideScope 验证普通旧读不能迟到覆盖同revision的新窄授权。
// 参数：t为真实认证器、并发内存行锁与同一Cookie测试上下文。
// 返回值：无；最终普通请求恢复宽权限、增加远程读取或延长期限时失败。
func TestLegacyConcurrentAuthorizationNeverRestoresWideScope(t *testing.T) {
	for _, outage := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful ordinary refresh", true: "ordinary outage backoff"}[outage], func(t *testing.T) {

			now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
			record, wide := storedPrincipal(t, now)
			wide.AuthorizationRevision = 9
			wide.Permissions = map[string]bool{"project.read": true}
			narrow := wide
			narrow.Permissions = map[string]bool{"project.task.read": true}
			narrow.DataScopes = []DataScope{{RoleCode: "limited", ScopeType: "SELF", ScopeID: "identity-1"}}
			record.AuthorizationRevision = 9
			record.PrincipalJSON, _ = json.Marshal(wide)
			record.SessionIDHash = tokenDigest("same-cookie")
			store := &legacyConcurrentStore{fakeOIDCStore: &fakeOIDCStore{session: record}, secondLock: make(chan struct{})}
			oldStarted, releaseOld, freshStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			auth := &OIDCAuthenticator{options: OIDCOptions{TenantID: "tenant-1", SessionCookieName: "legacy_session", SessionIdleTTL: 30 * time.Minute, AuthorizationRefreshInterval: time.Minute, AuthorizationMaxStale: 5 * time.Minute}, store: store, now: func() time.Time { return now }}
			auth.refreshAuthorization = func(ctx context.Context, current StoredOIDCSession, checked time.Time, _ string) (StoredOIDCSession, Principal, error) {
				calls.Add(1)
				value := wide
				if ctx.Value(legacyConcurrentKey{}) == "fresh" {
					close(freshStarted)
					value = narrow
				} else {
					close(oldStarted)
					<-releaseOld
				}
				if outage && ctx.Value(legacyConcurrentKey{}) != "fresh" {
					return current, Principal{}, ErrAuthorizationUnavailable
				}
				current.PrincipalJSON, _ = json.Marshal(value)
				current.AuthorizationRevision, current.AuthorizationCheckedAt = 9, checked
				return current, value, nil
			}
			request := func(fresh bool) *http.Request {
				path := "/api/v1/projects"
				if fresh {
					path = "/api/v1/auth/me"
				}
				r := httptest.NewRequest("GET", path, nil)
				r.AddCookie(&http.Cookie{Name: "legacy_session", Value: "same-cookie"})
				if fresh {
					r.Header.Set("X-Authorization-Refresh", "1")
				}
				return r
			}
			oldDone, freshDone := make(chan error, 1), make(chan error, 1)
			go func() { _, err := auth.Authenticate(context.Background(), request(false)); oldDone <- err }()
			select {
			case <-oldStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("ordinary authorization read did not start")
			}
			go func() {
				ctx := context.WithValue(context.Background(), legacyConcurrentKey{}, "fresh")
				_, err := auth.Authenticate(ctx, request(true))
				freshDone <- err
			}()
			freshFinished := false
			select {
			case <-freshStarted:
				select {
				case err := <-freshDone:
					if err != nil {
						t.Fatal(err)
					}
					freshFinished = true
				case <-time.After(5 * time.Second):
					t.Fatal("fresh read did not finish")
				}
			case <-store.secondLock:
			case <-time.After(5 * time.Second):
				t.Fatal("fresh request reached neither source nor row lock")
			}
			close(releaseOld)
			select {
			case err := <-oldDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ordinary read did not finish")
			}
			if !freshFinished {
				select {
				case err := <-freshDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("fresh read did not finish after lock")
				}
			}
			current, err := auth.Authenticate(context.Background(), request(false))
			if err != nil || current.Has("project.read") || !current.Has("project.task.read") || len(current.DataScopes) != 1 || current.DataScopes[0].ScopeType != "SELF" || calls.Load() != 2 {
				t.Fatalf("late ordinary read restored authorization: principal=%+v calls=%d error=%v", current, calls.Load(), err)
			}
			persisted, err := store.FindSession(context.Background(), record.SessionIDHash, now)
			if err != nil || !persisted.SessionExpiresAt.Equal(record.SessionExpiresAt) || !persisted.LastInteractiveAt.Equal(record.LastInteractiveAt) {
				t.Fatal("authorization serialization changed original deadlines")
			}
		})
	}
}
