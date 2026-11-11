package background

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
)

var (
	// errIdentitySyncNotConfigured 表示轮次依赖（复核入口或仓储能力）缺失；
	// 启动时会提前跳过，这里只作为防御性检查，不能静默成功。
	errIdentitySyncNotConfigured = errors.New("personnel identity sync is not configured")
	// errIdentitySyncTenantInvalid 表示部署租户不合法：没有明确租户归属的
	// 系统主体不允许发起任何目录查询。
	errIdentitySyncTenantInvalid = errors.New("personnel identity sync tenant is invalid")
)

const (
	// identitySyncIntervalEnv 是人员身份复核周期。它和 SLA/派发周期一样是
	// 可选维护任务，但按小时计：负责人目录只支持逐人查询，周期过短会把
	// 基础平台打成热查询。缺省 6h，硬下限 1h。
	identitySyncIntervalEnv     = "PROJECT_IDENTITY_SYNC_INTERVAL"
	defaultIdentitySyncInterval = 6 * time.Hour
	minIdentitySyncInterval     = time.Hour
	// identitySyncRoundTimeout 是单轮总预算：逐租户复核共享这一预算，
	// 超时后未处理的租户记为失败，绝不影响下一轮或 SLA/派发循环。
	identitySyncRoundTimeout = 10 * time.Minute
	// identitySyncAuditTimeout 是汇总审计写库预算：审计使用独立的短上下文，
	// 避免单轮超时/停机取消把"本轮已发生的事实"一起丢掉。
	identitySyncAuditTimeout = 30 * time.Second
)

// identitySyncer 是本轮实际调用的复核入口。生产实现是 *application.Service；
// 后台测试可以注入替身，避免连接真实负责人目录。
type identitySyncer interface {
	SyncPersonnelIdentities(context.Context, platform.Principal) (application.PersonnelIdentitySyncResult, error)
}

// identitySyncRunner 执行一轮逐租户身份复核。
type identitySyncRunner struct {
	syncer identitySyncer
	store  application.IdentitySyncStore
	// tenantID 是部署租户：它是后台进程自身的身份，也是汇总审计的归属租户。
	tenantID string
	timeout  time.Duration
	logger   *slog.Logger
}

// identitySyncInterval 解析 PROJECT_IDENTITY_SYNC_INTERVAL。
//
// 取值策略是“非法值回退默认”，不是失败关闭：身份复核是可选的维护任务，
// 配置写错不应该让同一进程里的 SLA/派发等主流程拒绝启动。非数字、空值、
// 零或负数一律回退 6h；小于 1h 的合法时长按 1h 下限截断，保证不会把负责人
// 目录打成热查询。回退/截断只会退化为更保守的周期，不会放宽权限或跳过复核。
func identitySyncInterval(lookup func(string) (string, bool)) time.Duration {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	raw, ok := lookup(identitySyncIntervalEnv)
	if !ok || strings.TrimSpace(raw) == "" {
		return defaultIdentitySyncInterval
	}
	interval, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || interval <= 0 {
		return defaultIdentitySyncInterval
	}
	if interval < minIdentitySyncInterval {
		return minIdentitySyncInterval
	}
	return interval
}

// identitySyncPrincipal 构造仅用于本轮复核的系统主体。
// 它只持有 project.resource.manage 与 APPLICATION 数据范围，刚好满足
// SyncPersonnelIdentities 的“租户全范围能力目录维护”校验；
// 该主体只在后台维护路径内构造，不得进入任何用户请求路径。
func identitySyncPrincipal(tenant string) platform.Principal {
	return platform.Principal{
		TenantID:    tenant,
		UserID:      application.IdentitySyncSystemActor,
		IdentityID:  application.IdentitySyncSystemActor,
		Permissions: map[string]bool{"project.resource.manage": true},
		DataScopes:  []platform.DataScope{{ScopeType: "APPLICATION"}},
	}
}

// runOnce 执行一轮：枚举租户 → 逐租户复核 → 写一条汇总审计。
//
// 单租户失败只计数并继续下一个；只有"租户枚举失败"会让整轮返回错误。
// 返回的汇总无论成功失败都可用于日志/审计；返回 error 时调用方只需记录，
// 不能把后台主循环带停。
func (r *identitySyncRunner) runOnce(ctx context.Context) (application.IdentitySyncSummary, error) {
	summary := application.IdentitySyncSummary{TenantID: r.tenantID, StartedAt: time.Now().UTC()}
	if r.syncer == nil || r.store == nil {
		return summary, errIdentitySyncNotConfigured
	}
	if !validTenant(r.tenantID) {
		return summary, errIdentitySyncTenantInvalid
	}
	timeout := r.timeout
	if timeout <= 0 {
		timeout = identitySyncRoundTimeout
	}
	roundCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tenants, err := r.store.ListCapabilityTenants(roundCtx)
	if err != nil {
		summary.Duration = time.Since(summary.StartedAt)
		return summary, err
	}
	summary.Tenants = len(tenants)
	for index, tenant := range tenants {
		if roundCtx.Err() != nil {
			// 预算耗尽：剩余租户按失败记录，绝不假装已经复核过。
			summary.Failed += len(tenants) - index
			break
		}
		tenant = strings.TrimSpace(tenant)
		if !validTenant(tenant) {
			summary.Failed++
			r.log().Warn("personnel identity sync skipped invalid tenant", "error_code", "IDENTITY_SYNC_TENANT_INVALID")
			continue
		}
		result, syncErr := r.syncer.SyncPersonnelIdentities(roundCtx, identitySyncPrincipal(tenant))
		if syncErr != nil {
			// 只记录失败租户与错误码，不记录人员标识、姓名或目录响应内容。
			summary.Failed++
			r.log().Warn("personnel identity sync tenant failed", "tenant", tenant, "error_code", "IDENTITY_SYNC_TENANT_FAILED")
			continue
		}
		summary.Checked += result.Total
		summary.Changed += result.Changed
	}
	summary.Duration = time.Since(summary.StartedAt)
	r.recordAudit(roundCtx, summary)
	r.log().Info("personnel identity sync round completed",
		"tenants", summary.Tenants, "checked", summary.Checked, "changed", summary.Changed,
		"failed", summary.Failed, "duration_ms", summary.Duration.Milliseconds())
	return summary, nil
}

// recordAudit 写每轮唯一的一条汇总审计。写失败只留错误码并继续：
// 审计投递本身由 outbox 重试，不能反过来阻断下一轮复核。
func (r *identitySyncRunner) recordAudit(roundCtx context.Context, summary application.IdentitySyncSummary) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(roundCtx), identitySyncAuditTimeout)
	defer cancel()
	if err := r.store.RecordIdentitySyncAudit(auditCtx, summary); err != nil {
		r.log().Error("personnel identity sync audit write failed", "error_code", "IDENTITY_SYNC_AUDIT_FAILED")
	}
}

func (r *identitySyncRunner) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// startIdentitySync 在后台启动时接入身份复核循环。
// 负责人目录未开通或仓储不支持轮次能力时安全跳过（只记录一次），
// 不能阻断 SLA/派发等主流程启动。
func (r *Runtime) startIdentitySync() {
	if r.service == nil || r.service.Personnel == nil || r.identitySync == nil {
		r.logger.Info("personnel identity sync disabled", "error_code", "IDENTITY_SYNC_NOT_CONFIGURED")
		return
	}
	store, supported := r.service.Repo.(application.IdentitySyncStore)
	if !supported {
		r.logger.Warn("personnel identity sync disabled", "error_code", "IDENTITY_SYNC_STORE_UNSUPPORTED")
		return
	}
	interval := r.identitySyncInterval
	if interval <= 0 {
		interval = identitySyncInterval(os.LookupEnv)
	}
	runner := &identitySyncRunner{syncer: r.identitySync, store: store, tenantID: r.options.TenantID, timeout: identitySyncRoundTimeout, logger: r.logger}
	done := make(chan struct{})
	r.mu.Lock()
	r.identitySyncDone = done
	r.mu.Unlock()
	go func() {
		defer close(done)
		r.runIdentitySyncLoop(r.activityCtx, interval, runner)
	}()
	r.logger.Info("personnel identity sync scheduled", "interval", interval.String())
}

// runIdentitySyncLoop 先立即跑一轮，之后按间隔重复；周期任务与 SLA/派发
// 循环相互独立，都只由 activityCtx 取消。
func (r *Runtime) runIdentitySyncLoop(ctx context.Context, interval time.Duration, runner *identitySyncRunner) {
	// 启动即跑一轮：不等一个完整周期，部署后人员档案立刻能出现在指派下拉里。
	r.runIdentitySyncRound(ctx, runner)
	if interval <= 0 {
		interval = defaultIdentitySyncInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runIdentitySyncRound(ctx, runner)
		}
	}
}

// runIdentitySyncRound 是单轮隔离点：任何一轮的错误只记录错误码，
// 不返回给 Start/Stop，也不影响 SLA/派发与主流程。维护任务里的意外 panic
// 同样只记错误码后继续下一轮，不能让后台进程整体退出。
func (r *Runtime) runIdentitySyncRound(ctx context.Context, runner *identitySyncRunner) {
	if ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	blocked := r.blocked
	r.mu.Unlock()
	if blocked {
		// 运维暂停/停机排空期间不产生新的业务写入，与 SLA/派发循环保持一致。
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			r.logger.Error("personnel identity sync round panicked", "error_code", "IDENTITY_SYNC_ROUND_PANIC")
		}
	}()
	if _, err := runner.runOnce(ctx); err != nil {
		r.logger.Warn("personnel identity sync round failed", "error_code", "IDENTITY_SYNC_ROUND_FAILED")
	}
}
