package application

import (
	"context"
	"time"
)

// IdentitySyncSystemActor 是后台人员身份复核使用的系统主体标识。
// 它只用于“回基础平台负责人目录复核人员档案”这一条维护路径，
// 不得进入任何用户请求路径；审计与日志都按系统操作者记录。
const IdentitySyncSystemActor = "system:identity-sync"

// IdentitySyncSummary 是后台单轮人员身份复核的汇总。
// 一轮只写一条汇总审计与一条运行日志，不逐人刷审计/日志；
// 审计归属租户用部署租户（后台进程身份），逐租户结果只体现在计数里。
type IdentitySyncSummary struct {
	// TenantID 是部署租户，作为汇总审计行的归属租户。
	TenantID string
	// Tenants 是本轮枚举到的租户数（来自人员资质档案）。
	Tenants int
	// Checked 是本轮回目录复核过的人员档案数（各租户 Total 之和）。
	Checked int
	// Changed 是本轮身份状态真正发生变化的档案数。
	Changed int
	// Failed 是本轮复核失败（含目录不可用、授权拒绝、超时未处理）的租户数。
	Failed int
	// StartedAt 与 Duration 描述本轮耗时。
	StartedAt time.Time
	Duration  time.Duration
}

// IdentitySyncStore 是后台人员身份复核轮次需要的最小仓储能力。
// 单独定义而不是扩大 Repository：系统级维护动作不应迫使所有仓储实现
// 支持它，也让后台可以用窄接口做测试替身。
type IdentitySyncStore interface {
	// ListCapabilityTenants 枚举拥有人员资质档案的租户；空表返回空列表。
	ListCapabilityTenants(ctx context.Context) ([]string, error)
	// RecordIdentitySyncAudit 写一条系统汇总审计（审计 outbox），不逐人写。
	RecordIdentitySyncAudit(ctx context.Context, summary IdentitySyncSummary) error
}
