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
//     或剩余时间不足 remind_hours（临近提醒）。停留时长以服务项 updated_at 为准，
//     每次状态推进都会刷新该时间。
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

// computeSlaItems 把候选服务项展开成两类 SLA 口径：
//  1. 计划完成时间已过（与配置无关的固定口径），每项一条；
//  2. 命中启用规则的状态停留超期 / 临近提醒，各一条。
//
// 候选集由仓储给出（仅排除终态），两类口径的过滤在这里分别判定：
// 计划完成口径排除已交付（现场实施完成）的项，否则它们只要计划时间已过就会永久留在超期列表；
// 状态口径不按计划时间过滤，否则「计划时间未到但已临近超时」的项永远不会被提醒。
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
			// 两侧都做 Trim：脏数据（状态带空格）不能让规则静默失效。
			if !rule.Enabled || strings.TrimSpace(rule.Status) != strings.TrimSpace(candidate.Status) {
				continue
			}
			// 计时基准是「进入当前状态的时刻」。没有基准时不判超期，
			// 避免把零值时间当成极早的起点而误报天文数字的停留时长。
			if candidate.StatusChangedAt.IsZero() {
				continue
			}
			elapsed := int64(now.Sub(candidate.StatusChangedAt).Hours())
			switch {
			case elapsed > int64(rule.DeadlineHours):
				breach := candidate
				breach.Kind = domain.SlaKindStatusOverdue
				breach.OverdueHours = elapsed - int64(rule.DeadlineHours)
				breach.RuleName = rule.Name
				breach.RuleStatus = rule.Status
				breach.DeadlineHours = rule.DeadlineHours
				items = append(items, breach)
			case rule.RemindHours > 0 && int64(rule.DeadlineHours)-elapsed <= int64(rule.RemindHours):
				breach := candidate
				breach.Kind = domain.SlaKindStatusApproaching
				breach.OverdueHours = int64(rule.DeadlineHours) - elapsed
				breach.RuleName = rule.Name
				breach.RuleStatus = rule.Status
				breach.DeadlineHours = rule.DeadlineHours
				items = append(items, breach)
			}
		}
	}
	return items, unparsablePlannedEnd
}
