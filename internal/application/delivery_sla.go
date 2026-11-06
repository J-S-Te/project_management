package application

import (
	"context"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
	"time"
)

// ListSlaOverdue 返回两类超期/临近超期口径的合并列表：
//  1. 计划完成时间已过且尚未终结的服务项（与配置无关的固定口径）；
//  2. 启用的 pm_sla 规则判定：服务项停留在规则状态超过 deadline_hours（超期），
//     或剩余时间不足 remind_hours（临近提醒）。状态维度以 status_changed_at 计时，
//     报告阶段维度以 report_updated_at 计时（每次报告推进都会刷新）。
func (s *Service) ListSlaOverdue(ctx context.Context, p platform.Principal) ([]domain.SlaOverdueItem, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return nil, err
	}
	candidates, err := repo.ListSlaOverdue(ctx, filter)
	if err != nil {
		return nil, err
	}
	rules, err := s.Repo.ListRules(ctx, p.TenantID, "sla")
	if err != nil {
		return nil, err
	}
	computed, unparsablePlannedEnd := computeSlaItems(candidates, rules, time.Now().UTC())
	if unparsablePlannedEnd > 0 && s.Logger != nil {
		// 计划完成时间无法解析的服务项会被静默排除在 SLA 口径之外，必须可诊断。
		s.Logger.Warn("sla skipped items with unparsable planned_end",
			"tenant_id", p.TenantID, "skipped", unparsablePlannedEnd)
	}
	// 与列表/详情同一脱敏口径：该响应会带出 site/category。
	return s.applyFieldPermissionsToSlaItems(ctx, p, computed)
}

// computeSlaItems 把候选服务项展开成两类 SLA 口径：
//  1. 计划完成时间已过（与配置无关的固定口径），每项一条；
//  2. 命中启用规则的状态停留超期 / 临近提醒，各一条。
//
// 候选集由仓储给出（仅排除终态），两类口径的过滤在这里分别判定：
// 计划完成口径排除已交付（现场实施完成）的项，否则它们只要计划时间已过就会永久留在超期列表；
// 状态口径不按计划时间过滤，否则「计划时间未到但已临近超时」的项永远不会被提醒。
//
// 规则维度由 domain report phase 表决定：规则状态是报告阶段码时按服务项的
// report_status 匹配、用 report_updated_at 计时；否则保持原有的
// status/status_changed_at 行为。monitoring.go 的 SQL 谓词与通知文案必须与
// 这里同源（码清单来自 domain.ReportPhaseSLACodes），由一致性测试守护。
func computeSlaItems(candidates []domain.SlaOverdueItem, rules []domain.Rule, now time.Time) ([]domain.SlaOverdueItem, int) {
	items := make([]domain.SlaOverdueItem, 0, len(candidates))
	// 计划完成时间无法解析的服务项会被跳过：它们既进不了超期列表，也不会有任何提示，
	// 会静默消失在 SLA 口径之外。把数量返回给调用方，由调用方告警。
	unparsablePlannedEnd := 0
	for _, candidate := range candidates {
		if candidate.Status != domain.ProjectStatusFieldCompleted && candidate.PlannedEnd != "" {
			plannedEnd, err := time.Parse(time.RFC3339, candidate.PlannedEnd)
			if err != nil {
				unparsablePlannedEnd++
			} else if plannedEnd.Before(now) {
				overdue := candidate
				overdue.Kind = domain.SlaKindPlanEndOverdue
				overdue.OverdueHours = int64(now.Sub(plannedEnd).Hours())
				items = append(items, overdue)
			}
		}
		for _, rule := range rules {
			if !rule.Enabled {
				continue
			}
			if item, ok := evaluateSlaRule(candidate, rule, now); ok {
				items = append(items, item)
			}
		}
	}
	return items, unparsablePlannedEnd
}

// evaluateSlaRule 判定一条启用规则是否让服务项进入超期/临近口径。
// 参数：candidate 为候选服务项投影，rule 为启用规则，now 为判定时刻。
// 返回值：命中的 SLA 条目（已填 Kind/RuleDimension/计时结果）与是否命中。
func evaluateSlaRule(candidate domain.SlaOverdueItem, rule domain.Rule, now time.Time) (domain.SlaOverdueItem, bool) {
	if domain.IsReportPhase(rule.Status) {
		// 报告阶段维度只在服务项已到「现场实施完成」时成立，与
		// domain.DeriveProjectReportStage 的进入条件、监控 SQL 谓词一致；
		// 返工/异常数据留下的旧报告码不得产生 SLA。
		if strings.TrimSpace(candidate.Status) != domain.ProjectStatusFieldCompleted {
			return domain.SlaOverdueItem{}, false
		}
		// 按服务项当前报告状态匹配规则，用报告推进时间计时。
		// 两侧都做归一化（大写+去空白），脏数据不能让规则静默失效。
		reportStage := normalizeReportStatus(candidate.ReportStatus)
		if reportStage != normalizeReportStatus(rule.Status) || candidate.ReportChangedAt.IsZero() {
			return domain.SlaOverdueItem{}, false
		}
		return slaBreach(candidate, rule, candidate.ReportChangedAt, domain.SlaDimensionReport, reportStage, now)
	}
	// 状态维度：保持历史行为——按 status 匹配、按 status_changed_at 计时。
	// 两侧都做 Trim：脏数据（状态带空格）不能让规则静默失效。
	if strings.TrimSpace(rule.Status) != strings.TrimSpace(candidate.Status) || candidate.StatusChangedAt.IsZero() {
		return domain.SlaOverdueItem{}, false
	}
	return slaBreach(candidate, rule, candidate.StatusChangedAt, domain.SlaDimensionStatus, "", now)
}

// slaBreach 按停留时长与时限/提醒阈值生成超期或临近条目。
// 参数：candidate 为候选服务项，rule 为规则，since 为进入当前维度状态的时刻，
// dimension/reportStage 为本次命中的规则维度与报告阶段，now 为判定时刻。
// 返回值：SLA 条目与是否命中；未命中时返回零值。
func slaBreach(candidate domain.SlaOverdueItem, rule domain.Rule, since time.Time, dimension, reportStage string, now time.Time) (domain.SlaOverdueItem, bool) {
	elapsed := int64(now.Sub(since).Hours())
	fill := func(kind string, overdueHours int64) domain.SlaOverdueItem {
		breach := candidate
		breach.Kind = kind
		breach.OverdueHours = overdueHours
		breach.RuleName = rule.Name
		breach.RuleStatus = rule.Status
		breach.DeadlineHours = rule.DeadlineHours
		breach.RuleDimension = dimension
		breach.ReportStage = reportStage
		return breach
	}
	switch {
	case elapsed > int64(rule.DeadlineHours):
		return fill(domain.SlaKindStatusOverdue, elapsed-int64(rule.DeadlineHours)), true
	case rule.RemindHours > 0 && int64(rule.DeadlineHours)-elapsed <= int64(rule.RemindHours):
		return fill(domain.SlaKindStatusApproaching, int64(rule.DeadlineHours)-elapsed), true
	}
	return domain.SlaOverdueItem{}, false
}

// normalizeReportStatus 归一化报告状态码：报告状态列存大写码，
// 历史或手工数据可能带空格/小写，判定前必须与 domain 报告阶段表一致。
func normalizeReportStatus(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}
