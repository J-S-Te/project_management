package application

import (
	"context"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// MetricUsageBatchStore 由仓储在一个事务内完成整批用量命令。
// 缺实现时失败关闭，不按逐条请求降级，避免出现部分成功。
type MetricUsageBatchStore interface {
	BatchMutateMetricUsages(context.Context, platform.ScopeFilter, string, domain.MetricUsageBatchInput) ([]domain.ResourceUsage, error)
}

// BatchMutateMetricUsages 原子处理一批用量提交、独立确认或取消。
// 参数：ctx/p 为真实请求与主体，in 为目标版本、动作、说明和幂等键。
// 返回值：全部生效后的真实用量；任一版本、权限、范围或状态冲突则整批回滚。
func (s *Service) BatchMutateMetricUsages(ctx context.Context, p platform.Principal, in domain.MetricUsageBatchInput) (domain.MetricUsageBatchResult, error) {
	action := strings.ToUpper(strings.TrimSpace(in.Action))
	permission := "project.resource_usage.record"
	if action == "DECISION" {
		permission = "project.resource_usage.confirm"
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return domain.MetricUsageBatchResult{}, err
	}
	if len(in.Items) == 0 || len(in.Items) > domain.MetricUsageBatchMaxItems || !metricKeyValid(in.IdempotencyKey) {
		return domain.MetricUsageBatchResult{}, ErrValidation
	}
	if action != "SUBMIT" && action != "DECISION" && action != "CANCEL" {
		return domain.MetricUsageBatchResult{}, ErrValidation
	}
	if action == "DECISION" && in.Decision != "CONFIRMED" && in.Decision != "RETURNED" || action != "DECISION" && in.Decision != "" {
		return domain.MetricUsageBatchResult{}, ErrValidation
	}
	if action != "SUBMIT" && strings.TrimSpace(in.Note) == "" || len([]rune(in.Note)) > 1000 {
		return domain.MetricUsageBatchResult{}, ValidationError("批量命令需要填写确认、退回或取消说明")
	}
	seen := make(map[string]bool, len(in.Items))
	for _, item := range in.Items {
		if item.ID == "" || len(item.ID) > 64 || item.ExpectedVersion == 0 || seen[item.ID] {
			return domain.MetricUsageBatchResult{}, ErrValidation
		}
		seen[item.ID] = true
	}
	in.Action, in.Decision, in.Note = action, strings.ToUpper(strings.TrimSpace(in.Decision)), strings.TrimSpace(in.Note)
	store, ok := s.Repo.(MetricUsageBatchStore)
	if !ok {
		return domain.MetricUsageBatchResult{}, ErrProjectSnapshotUnavailable
	}
	items, err := store.BatchMutateMetricUsages(ctx, filter, p.UserID, in)
	if err != nil {
		return domain.MetricUsageBatchResult{}, err
	}
	return domain.MetricUsageBatchResult{Items: items}, nil
}
