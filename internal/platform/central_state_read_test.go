package platform

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	central "github.com/j-s-te/project-management/internal/sessiongovernance"
)

type controlledCentralReader struct {
	central.Client
	state      central.Context
	err        error
	failFirst  bool
	gate       <-chan struct{}
	reads      atomic.Int32
	activities atomic.Int32
	panicRead  bool
}

// State 提供可阻塞且尊重取消的中央只读替身，记录真实调用次数。
// 参数：ctx为调用截止，绑定秘密未用于替身的业务判断。
// 返回值：权威测试窗口或分类错误，不创建真实会话。
func (c *controlledCentralReader) State(ctx context.Context, _ string) (central.Context, error) {
	count := c.reads.Add(1)
	if c.panicRead {
		panic("private fixture material must not be logged")
	}
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return central.Context{}, central.ErrUnavailable
		}
	}
	if c.err != nil || c.failFirst && count == 1 {
		if c.err != nil {
			return central.Context{}, c.err
		}
		return central.Context{}, central.ErrUnavailable
	}
	return c.state, nil
}

// Activity 记录每次真实活动，验证写入不被重试或合并。
// 参数：上下文与绑定为本地测试材料，不访问外部服务。
// 返回值：预设窗口或依赖错误。
func (c *controlledCentralReader) Activity(context.Context, string) (central.Context, error) {
	c.activities.Add(1)
	return c.state, c.err
}

// newCentralReadFixture 创建可直接调用正式centralState门禁的加密会话夹具。
// 参数：t为测试上下文，client控制中央读写结果。
// 返回值：认证器、本地绑定和撤销记录；初始化失败时终止测试。
func newCentralReadFixture(t *testing.T, client *controlledCentralReader) (*OIDCAuthenticator, StoredOIDCSession, *centralContractStore) {
	t.Helper()
	now := time.Now().UTC()
	codec, err := newSecretCodec(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := codec.encrypt([]byte("fixture-binding"))
	if err != nil {
		t.Fatal(err)
	}
	client.state = central.Context{SessionID: "central-cycle", TenantID: "tenant",
		SubjectID: "identity", ClientID: "project-client", ApplicationCode: "project",
		EnvironmentCode: "test", PolicyVersion: 1, IdleTimeoutSeconds: 1800,
		IdleRemainingMS:     (30 * time.Minute).Milliseconds(),
		AbsoluteRemainingMS: (8 * time.Hour).Milliseconds(), AbsoluteExpiresAt: now.Add(8 * time.Hour)}
	record := StoredOIDCSession{TenantID: "tenant", IdentityID: "identity",
		CentralBindingCipher: cipher, CentralSessionID: "central-cycle"}
	store := &centralContractStore{record: record}
	auth := &OIDCAuthenticator{codec: codec, store: store, now: time.Now,
		options: OIDCOptions{TenantID: "tenant", ClientID: "project-client",
			ApplicationCode: "project", EnvironmentCode: "test", CentralClient: client}}
	return auth, record, store
}

// TestCentralReadRetriesOnlyTransientFailure 验证纯只读临时故障最多重试一次。
// 参数：t为无数据库的正式中央门禁测试上下文。
// 返回值：无；错误放行、误撤销或重试已失效周期时失败。
func TestCentralReadRetriesOnlyTransientFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		first bool
		calls int32
		want  error
	}{{"transient-then-live", nil, true, 2, nil},
		{"still-unavailable", central.ErrUnavailable, false, 2, ErrAuthorizationUnavailable},
		{"inactive", central.ErrInactive, false, 1, ErrUnauthenticated},
		{"conflict", central.ErrBindingConflict, false, 1, ErrUnauthenticated}} {
		t.Run(tc.name, func(t *testing.T) {
			client := &controlledCentralReader{err: tc.err, failFirst: tc.first}
			auth, record, store := newCentralReadFixture(t, client)
			state, err := auth.centralState(context.Background(), record, false, true)
			if !errors.Is(err, tc.want) || client.reads.Load() != tc.calls {
				t.Fatalf("err=%v want=%v reads=%d want=%d", err, tc.want,
					client.reads.Load(), tc.calls)
			}
			if tc.want == nil && !state.AbsoluteExpiresAt.Equal(client.state.AbsoluteExpiresAt) {
				t.Fatal("read retry must preserve the original absolute deadline")
			}
			if tc.want == ErrAuthorizationUnavailable && store.revoked {
				t.Fatal("temporary failure must not revoke the local session")
			}
		})
	}
}

// TestCentralReadCoalescesOnlyInFlight 验证并发同绑定读合并且完成后不新增结果缓存。
// 参数：t为使用虚拟调度等待的并发测试上下文，不依赖真实sleep。
// 返回值：无；并发放大、跨次复用旧窗口或正确请求失败时失败。
func TestCentralReadCoalescesOnlyInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		client := &controlledCentralReader{gate: gate}
		auth, record, _ := newCentralReadFixture(t, client)
		results := make(chan error, 8)
		for range 8 {
			go func() {
				_, err := auth.centralState(context.Background(), record, false, true)
				results <- err
			}()
		}
		synctest.Wait()
		if client.reads.Load() != 1 {
			t.Errorf("same binding must share one in-flight read, got %d", client.reads.Load())
		}
		close(gate)
		for range 8 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if _, err := auth.centralState(context.Background(), record, false, true); err != nil || client.reads.Load() != 2 {
			t.Fatalf("completed flight must not cache its result: reads=%d err=%v", client.reads.Load(), err)
		}
	})
}

// TestCentralReadCancellationDoesNotCancelOtherWaiters 验证首请求取消不伤及同绑定请求。
// 参数：t为虚拟调度测试上下文，两个请求共用一个只读检查。
// 返回值：无；取消被当失效、取消传播给其他请求或重复RPC时失败。
func TestCentralReadCancellationDoesNotCancelOtherWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		client := &controlledCentralReader{gate: gate}
		auth, record, store := newCentralReadFixture(t, client)
		ctx, cancel := context.WithCancel(context.Background())
		first, second := make(chan error, 1), make(chan error, 1)
		go func() { _, err := auth.centralState(ctx, record, false, true); first <- err }()
		synctest.Wait()
		go func() { _, err := auth.centralState(context.Background(), record, false, true); second <- err }()
		synctest.Wait()
		cancel()
		if err := <-first; !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatalf("canceled waiter classification: %v", err)
		}
		close(gate)
		if err := <-second; err != nil || store.revoked || client.reads.Load() != 1 {
			t.Fatalf("other waiter must remain valid: err=%v revoked=%v calls=%d",
				err, store.revoked, client.reads.Load())
		}
	})
}

// TestCentralActivityIsNotRetried 验证活动写入仍逐次发送且失败不重放。
// 参数：t为正式中央门禁单测上下文。
// 返回值：无；活动被合并、重试、放行或临时失败误撤销时失败。
func TestCentralActivityIsNotRetried(t *testing.T) {
	client := &controlledCentralReader{err: central.ErrUnavailable}
	auth, record, store := newCentralReadFixture(t, client)
	for range 2 {
		if _, err := auth.centralState(context.Background(), record, true, true); !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatal(err)
		}
	}
	if client.activities.Load() != 2 || client.reads.Load() != 0 || store.revoked {
		t.Fatal("activity must remain separate from read retries and coalescing")
	}
}

// TestCentralReadHasOverallDeadline 验证不响应的依赖有整体截止且不撤销会话。
// 参数：t为虚拟时钟测试上下文，依赖等待取消而不返回成功。
// 返回值：无；截止被延长、无限等待或误判失效时失败。
func TestCentralReadHasOverallDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &controlledCentralReader{gate: make(chan struct{})}
		auth, record, store := newCentralReadFixture(t, client)
		started := time.Now()
		_, err := auth.centralState(context.Background(), record, false, true)
		if !errors.Is(err, ErrAuthorizationUnavailable) ||
			time.Since(started) != 2*time.Second || store.revoked || client.reads.Load() != 1 {
			t.Fatal("read must stop at its overall deadline without revoking the session")
		}
	})
}

// TestCentralReadFlightCapacityAndIsolation 验证不同绑定不混用且在途数量有上限。
// 参数：t为虚拟调度上下文，使用不同绑定模拟达到容量上限的并发用户。
// 返回值：无；跨绑定合并、容量失控或已有绑定无法继续等待时失败。
func TestCentralReadFlightCapacityAndIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		client := &controlledCentralReader{gate: gate}
		auth, record, _ := newCentralReadFixture(t, client)
		results := make(chan error, maximumCentralReadFlights+1)
		for i := range maximumCentralReadFlights {
			current := record
			current.CentralBindingCipher, _ = auth.codec.encrypt([]byte(fmt.Sprintf("binding-%d", i)))
			go func() {
				_, err := auth.centralState(context.Background(), current, false, true)
				results <- err
			}()
		}
		synctest.Wait()
		if client.reads.Load() != maximumCentralReadFlights {
			t.Errorf("distinct bindings must not share results: %d", client.reads.Load())
		}
		if _, err := auth.centralState(context.Background(), record, false, true); !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Fatal("new binding must fail closed when the in-flight limit is reached")
		}
		record.CentralBindingCipher, _ = auth.codec.encrypt([]byte("binding-0"))
		go func() {
			_, err := auth.centralState(context.Background(), record, false, true)
			results <- err
		}()
		synctest.Wait()
		close(gate)
		for range maximumCentralReadFlights + 1 {
			if err := <-results; err != nil {
				t.Fatal("existing flight should remain available:", err)
			}
		}
	})
}

// TestCentralReadPanicFailsClosedAndReleasesFlight 验证异常依赖不崩溃或残留槽位。
// 参数：t为本地故障注入上下文，不访问真实中央服务。
// 返回值：无；异常被放行或后续正常请求被旧槽位毒化时失败。
func TestCentralReadPanicFailsClosedAndReleasesFlight(t *testing.T) {
	client := &controlledCentralReader{panicRead: true}
	auth, record, store := newCentralReadFixture(t, client)
	if _, err := auth.centralState(context.Background(), record, false, true); !errors.Is(err, ErrAuthorizationUnavailable) || store.revoked {
		t.Fatal("reader panic must fail closed without revoking the session")
	}
	client.panicRead = false
	if _, err := auth.centralState(context.Background(), record, false, true); err != nil {
		t.Fatal("completed failed flight must release its slot:", err)
	}
}
