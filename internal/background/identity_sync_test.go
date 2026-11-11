package background

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

func identitySyncTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeIdentityStore 记录轮次汇总审计，并可按需让租户枚举失败。
type fakeIdentityStore struct {
	tenants []string
	listErr error
	audits  []application.IdentitySyncSummary
	rounds  chan application.IdentitySyncSummary
}

func (s *fakeIdentityStore) ListCapabilityTenants(context.Context) ([]string, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]string(nil), s.tenants...), nil
}

func (s *fakeIdentityStore) RecordIdentitySyncAudit(ctx context.Context, summary application.IdentitySyncSummary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.audits = append(s.audits, summary)
	if s.rounds != nil {
		s.rounds <- summary
	}
	return nil
}

// fakeIdentitySyncer 按租户计数、按需注入失败，并校验系统主体边界：
// 缺少 project.resource.manage 或全量数据范围时一律拒绝，模拟真实服务授权。
type fakeIdentitySyncer struct {
	mu        sync.Mutex
	calls     map[string]int
	fail      map[string]error
	total     int
	changed   int
	principal platform.Principal
}

func (f *fakeIdentitySyncer) SyncPersonnelIdentities(_ context.Context, p platform.Principal) (application.PersonnelIdentitySyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[p.TenantID]++
	f.principal = p
	if !p.Has("project.resource.manage") || !p.HasFullDataScope() {
		return application.PersonnelIdentitySyncResult{}, application.ErrForbidden
	}
	if err := f.fail[p.TenantID]; err != nil {
		return application.PersonnelIdentitySyncResult{}, err
	}
	return application.PersonnelIdentitySyncResult{Total: f.total, Changed: f.changed}, nil
}

func (f *fakeIdentitySyncer) callCount(tenant string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[tenant]
}

func identitySyncTestRunner(syncer identitySyncer, store application.IdentitySyncStore) *identitySyncRunner {
	return &identitySyncRunner{syncer: syncer, store: store, tenantID: "tenant-deploy", timeout: time.Minute, logger: identitySyncTestLogger()}
}

// 一轮必须逐租户各调用一次复核，并且只写一条汇总审计（不逐人写）。
func TestIdentitySyncRoundReviewsEachTenantOnceAndWritesOneSummaryAudit(t *testing.T) {
	store := &fakeIdentityStore{tenants: []string{"tenant-a", "tenant-b"}}
	syncer := &fakeIdentitySyncer{total: 4, changed: 1}
	summary, err := identitySyncTestRunner(syncer, store).runOnce(context.Background())
	if err != nil {
		t.Fatalf("runOnce failed: %v", err)
	}
	if summary.Tenants != 2 || summary.Checked != 8 || summary.Changed != 2 || summary.Failed != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		if got := syncer.callCount(tenant); got != 1 {
			t.Fatalf("tenant %s reviewed %d times, want exactly 1", tenant, got)
		}
	}
	if len(store.audits) != 1 {
		t.Fatalf("audits = %d, want exactly one summary per round", len(store.audits))
	}
	audit := store.audits[0]
	if audit.TenantID != "tenant-deploy" || audit.Tenants != 2 || audit.Checked != 8 || audit.Changed != 2 || audit.Failed != 0 {
		t.Fatalf("summary audit = %+v", audit)
	}
}

// 单租户失败只记录并继续：其余租户仍被复核，整轮本身返回成功（失败计入汇总）。
func TestIdentitySyncTenantFailureDoesNotBlockOtherTenants(t *testing.T) {
	store := &fakeIdentityStore{tenants: []string{"tenant-a", "tenant-b", "tenant-c"}}
	syncer := &fakeIdentitySyncer{total: 4, changed: 1, fail: map[string]error{"tenant-b": application.ErrForbidden}}
	summary, err := identitySyncTestRunner(syncer, store).runOnce(context.Background())
	if err != nil {
		t.Fatalf("a single tenant failure must not fail the round: %v", err)
	}
	if summary.Tenants != 3 || summary.Checked != 8 || summary.Changed != 2 || summary.Failed != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, tenant := range []string{"tenant-a", "tenant-b", "tenant-c"} {
		if got := syncer.callCount(tenant); got != 1 {
			t.Fatalf("tenant %s reviewed %d times, want exactly 1", tenant, got)
		}
	}
	if len(store.audits) != 1 || store.audits[0].Failed != 1 {
		t.Fatalf("audits = %+v, want one row with failed=1", store.audits)
	}
}

// 租户枚举失败是整轮唯一会返回错误的情况：此时没有可信计数，不写汇总审计，
// 但错误必须交给调用方记录，由下一轮重试。
func TestIdentitySyncRoundReportsTenantEnumerationFailure(t *testing.T) {
	store := &fakeIdentityStore{listErr: errors.New("database unavailable")}
	syncer := &fakeIdentitySyncer{}
	if _, err := identitySyncTestRunner(syncer, store).runOnce(context.Background()); err == nil {
		t.Fatal("tenant enumeration failure must be reported")
	}
	if len(store.audits) != 0 {
		t.Fatalf("audits = %+v, want none without trustworthy counts", store.audits)
	}
	if syncer.callCount("tenant-a") != 0 {
		t.Fatal("no tenant may be reviewed when enumeration failed")
	}
}

// 启动即跑一轮，之后按 ticker 重复；每轮租户调用次数一致。
func TestIdentitySyncLoopRunsImmediatelyThenOnTicker(t *testing.T) {
	store := &fakeIdentityStore{tenants: []string{"tenant-a", "tenant-b"}, rounds: make(chan application.IdentitySyncSummary, 8)}
	syncer := &fakeIdentitySyncer{total: 1}
	r := &Runtime{logger: identitySyncTestLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.runIdentitySyncLoop(ctx, 20*time.Millisecond, identitySyncTestRunner(syncer, store))
	}()
	for round := 1; round <= 2; round++ {
		select {
		case <-store.rounds:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatalf("round %d did not run", round)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("identity sync loop did not stop after cancellation")
	}
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		if got := syncer.callCount(tenant); got != 2 {
			t.Fatalf("tenant %s reviewed %d times, want 2 (immediate + one tick)", tenant, got)
		}
	}
}

// env 取值必须失败关闭到保守周期：非法值回退 6h，过小值截断到 1h 下限。
func TestIdentitySyncIntervalFallbackAndFloor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		set    bool
		expect time.Duration
	}{
		{"unset", "", false, 6 * time.Hour},
		{"blank", "  ", true, 6 * time.Hour},
		{"non numeric", "six hours", true, 6 * time.Hour},
		{"zero", "0", true, 6 * time.Hour},
		{"negative", "-2h", true, 6 * time.Hour},
		{"below floor", "30m", true, time.Hour},
		{"valid", "3h", true, 3 * time.Hour},
		{"valid minutes", "90m", true, 90 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(string) (string, bool) { return "", false }
			if tc.set {
				lookup = func(string) (string, bool) { return tc.raw, true }
			}
			if got := identitySyncInterval(lookup); got != tc.expect {
				t.Fatalf("interval(%q) = %s, want %s", tc.raw, got, tc.expect)
			}
		})
	}
}

// 系统主体边界：只带一条维护权限 + 应用级数据范围，且标识固定为系统账号。
// 生产路径只能用这个主体，不能复用或扩大到其他请求。
func TestIdentitySyncPrincipalIsMaintenanceOnly(t *testing.T) {
	p := identitySyncPrincipal("tenant-a")
	if p.TenantID != "tenant-a" || p.UserID != application.IdentitySyncSystemActor || p.IdentityID != application.IdentitySyncSystemActor {
		t.Fatalf("principal identity = %+v", p)
	}
	if len(p.Permissions) != 1 || !p.Has("project.resource.manage") {
		t.Fatalf("principal permissions = %+v, want only project.resource.manage", p.Permissions)
	}
	if len(p.DataScopes) != 1 || p.DataScopes[0].ScopeType != "APPLICATION" || !p.HasFullDataScope() {
		t.Fatalf("principal scopes = %+v, want APPLICATION only", p.DataScopes)
	}
	// 没有维护权限的主体不能被同一个入口放行（防止将来把系统主体替换成空主体）。
	syncer := &fakeIdentitySyncer{}
	if _, err := syncer.SyncPersonnelIdentities(context.Background(), platform.Principal{TenantID: "tenant-a"}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("empty principal err = %v, want ErrForbidden", err)
	}
}

// identityRuntimeRepo 是后台启动集成用例的最小仓储：只实现复核轮次与人员档案
// 读写，其余契约用 nil 接口占位，确保意外路径直接 panic 而不是静默写库。
type identityRuntimeRepo struct {
	application.Repository
	application.DeliveryRepository
	mu        sync.Mutex
	tenants   []string
	caps      []domain.Capability
	updates   int
	auditChan chan application.IdentitySyncSummary
}

func (r *identityRuntimeRepo) ListCapabilityTenants(context.Context) ([]string, error) {
	return append([]string(nil), r.tenants...), nil
}

func (r *identityRuntimeRepo) ListCapabilities(_ context.Context, tenant, typ string) ([]domain.Capability, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Capability, 0, len(r.caps))
	for _, item := range r.caps {
		if tenant != "tenant-a" || (typ != "" && item.ResourceType != typ) {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (r *identityRuntimeRepo) UpdateCapabilityIdentities(_ context.Context, tenant string, statuses map[string]string, checkedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tenant != "tenant-a" {
		return errors.New("unexpected tenant")
	}
	r.updates++
	for index := range r.caps {
		status, ok := statuses[r.caps[index].UserID]
		if !ok {
			continue
		}
		r.caps[index].IdentityStatus = status
		r.caps[index].IdentityCheckedAt = checkedAt
	}
	return nil
}

func (r *identityRuntimeRepo) RecordIdentitySyncAudit(_ context.Context, summary application.IdentitySyncSummary) error {
	r.auditChan <- summary
	return nil
}

func (r *identityRuntimeRepo) updateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updates
}

// ownerDirectoryStub 只按 user_id 精确应答；查无此人返回空页，不返回错误。
type ownerDirectoryStub struct{ names map[string]string }

func (d ownerDirectoryStub) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	name, ok := d.names[query.UserID]
	if !ok {
		return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{}}, nil
	}
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: query.UserID, DisplayName: name}}}, nil
}

// 通过真实 Runtime.Start 验证接线：启动即跑、逐租户调用真实
// SyncPersonnelIdentities、第二轮同结果零写入、Stop 会 join 复核循环。
func TestRuntimeStartRunsIdentitySyncAndStops(t *testing.T) {
	r, _, _ := testRuntime(t)
	repo := &identityRuntimeRepo{
		tenants:   []string{"tenant-a"},
		caps:      []domain.Capability{{ResourceType: "PERSON", ResourceID: "P-1", ResourceName: "张三", UserID: "u-1", Status: "ACTIVE", IdentityStatus: domain.IdentityStatusUnlinked}},
		auditChan: make(chan application.IdentitySyncSummary, 8),
	}
	r.service.Repo = repo
	r.service.Personnel = ownerDirectoryStub{names: map[string]string{"u-1": "张三"}}
	r.identitySyncInterval = 25 * time.Millisecond
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	for round := 1; round <= 2; round++ {
		select {
		case summary := <-repo.auditChan:
			if summary.Tenants != 1 || summary.Checked != 1 || summary.Failed != 0 {
				t.Fatalf("round %d summary = %+v", round, summary)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("identity sync round %d did not run after Start", round)
		}
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Stop(stopCtx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	// 第一轮把 UNLINKED 改成 ACTIVE，第二轮目录结论一致，不得再写。
	if got := repo.updateCount(); got != 1 {
		t.Fatalf("identity updates = %d, want 1 (second identical round must be a no-op)", got)
	}
	if repo.caps[0].IdentityStatus != domain.IdentityStatusActive {
		t.Fatalf("identity status = %q, want ACTIVE", repo.caps[0].IdentityStatus)
	}
}

// panicIdentitySyncer 在第一次复核时 panic，用于验证单轮隔离：意外 panic
// 只能结束当前轮，不能让后台进程与后续轮次一起退出。
type panicIdentitySyncer struct {
	mu    sync.Mutex
	calls int
}

func (p *panicIdentitySyncer) SyncPersonnelIdentities(context.Context, platform.Principal) (application.PersonnelIdentitySyncResult, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		panic("owner directory client bug")
	}
	return application.PersonnelIdentitySyncResult{Total: 1}, nil
}

func (p *panicIdentitySyncer) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestIdentitySyncRoundPanicIsContainedAndLoopContinues(t *testing.T) {
	store := &fakeIdentityStore{tenants: []string{"tenant-a"}, rounds: make(chan application.IdentitySyncSummary, 8)}
	syncer := &panicIdentitySyncer{}
	r := &Runtime{logger: identitySyncTestLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.runIdentitySyncLoop(ctx, 10*time.Millisecond, identitySyncTestRunner(syncer, store))
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("identity sync loop did not stop after cancellation")
		}
	}()
	select {
	case <-store.rounds:
		// 第二轮已经恢复并写出汇总审计。
	case <-time.After(3 * time.Second):
		t.Fatal("panic in one round stopped the identity sync loop")
	}
	if got := syncer.callCount(); got < 2 {
		t.Fatalf("syncer calls = %d, want the loop to continue after a panic", got)
	}
}

// 运维暂停/停机排空期间不产生新的身份写回，与既有 SLA/派发循环的 blocked 语义一致。
func TestIdentitySyncPausedRuntimeSkipsNewRounds(t *testing.T) {
	store := &fakeIdentityStore{tenants: []string{"tenant-a"}}
	syncer := &fakeIdentitySyncer{total: 1}
	r := &Runtime{logger: identitySyncTestLogger(), blocked: true}
	r.runIdentitySyncRound(context.Background(), identitySyncTestRunner(syncer, store))
	if syncer.callCount("tenant-a") != 0 || len(store.audits) != 0 {
		t.Fatalf("paused runtime must not review tenants or write audits: calls=%d audits=%d", syncer.callCount("tenant-a"), len(store.audits))
	}
}
