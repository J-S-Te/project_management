package temporalworker

import (
	"context"
	"log/slog"
	"time"
)

// 审计 outbox 积压指标（C2 中间态）：只暴露聚合行数，不带租户、事件或载荷标签。
const (
	// OutboxPendingGauge 是仍需投递的审计事件行数。
	OutboxPendingGauge = "project_audit_outbox_pending"
	// OutboxDeadGauge 是达到重试上限、停在终态失败的审计事件行数。
	OutboxDeadGauge = "project_audit_outbox_dead"
	// OutboxQueryErrorsCounter 累计积压查询失败次数，区分"没有积压"与"读不到积压"。
	OutboxQueryErrorsCounter = "project_audit_outbox_query_errors"

	// defaultOutboxBacklogInterval 是积压采集周期：够快发现积压，又不把数据库变成轮询热点。
	defaultOutboxBacklogInterval = 30 * time.Second
	// defaultOutboxBacklogTimeout 是单次积压查询的上限，避免慢查询挂住采集循环。
	defaultOutboxBacklogTimeout = 5 * time.Second
)

// OutboxBacklog 是审计 outbox 的积压快照。
type OutboxBacklog struct {
	Pending int64
	Dead    int64
}

// OutboxBacklogSource 读取审计 outbox 积压；实现必须返回聚合值，不暴露业务载荷。
type OutboxBacklogSource interface {
	CountBacklog(context.Context) (OutboxBacklog, error)
}

// OutboxBacklogCollector 周期性把审计 outbox 积压写入 Temporal 私有指标端口。
//
// 三态口径：读到积压输出当前值（含 0）；查询失败移除 gauge 并累加错误计数，
// 让"读不到"与"积压为 0"在监控上可区分，而不是把陈旧值当成事实。
type OutboxBacklogCollector struct {
	Source   OutboxBacklogSource
	Registry *MetricsRegistry
	Interval time.Duration
	Timeout  time.Duration
	Logger   *slog.Logger
}

// Run 持续采集积压，直到 ctx 取消；启动后立即采集一次，不等第一个周期。
func (collector *OutboxBacklogCollector) Run(ctx context.Context) {
	if collector == nil || collector.Source == nil || collector.Registry == nil {
		return
	}
	interval := collector.Interval
	if interval <= 0 {
		interval = defaultOutboxBacklogInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	collector.collect(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collector.collect(ctx)
		}
	}
}

// collect 执行一次积压查询并更新指标。
// 参数：ctx 为进程生命周期上下文；查询本身另有独立超时。
func (collector *OutboxBacklogCollector) collect(ctx context.Context) {
	if collector == nil || collector.Source == nil || collector.Registry == nil {
		return
	}
	timeout := collector.Timeout
	if timeout <= 0 {
		timeout = defaultOutboxBacklogTimeout
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	backlog, err := collector.Source.CountBacklog(queryCtx)
	if err != nil {
		collector.logger().WarnContext(ctx, "read audit outbox backlog", "error_code", "OUTBOX_BACKLOG_UNAVAILABLE", "error", err)
		collector.Registry.ClearGauges(OutboxPendingGauge, OutboxDeadGauge)
		collector.Registry.Counter(OutboxQueryErrorsCounter).Inc(1)
		return
	}
	collector.Registry.Gauge(OutboxPendingGauge).Update(float64(backlog.Pending))
	collector.Registry.Gauge(OutboxDeadGauge).Update(float64(backlog.Dead))
}

func (collector *OutboxBacklogCollector) logger() *slog.Logger {
	if collector.Logger != nil {
		return collector.Logger
	}
	return slog.Default()
}
