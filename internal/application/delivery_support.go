package application

import (
	"context"
	"errors"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"strconv"
	"strings"
	"time"
)

func (s *Service) deliveryRepo() (DeliveryRepository, error) {
	repo, ok := s.Repo.(DeliveryRepository)
	if !ok {
		return nil, errors.New("delivery repository unavailable")
	}
	return repo, nil
}

// ListSlaOverdue 返回两类超期/临近超期口径的合并列表：
//  1. 计划完成时间已过且尚未终结的服务项（与配置无关的固定口径）；
//  2. 启用的 pm_sla 规则判定：服务项停留在规则状态超过 deadline_hours（超期），
//     或剩余时间不足 remind_hours（临近提醒）。停留时长以服务项 updated_at 为准，
//     每次状态推进都会刷新该时间。

// verifyExpectedVersion 在写入前校验客户端声明的服务项版本。
//
// 这是"先能检测、能提示"的那一半：实际写入已在事务内加行锁并做条件更新，不会产生脏写；
// 但客户端此前拿不到任何版本信号，两人同时改同一对象时后者会静默覆盖前者的意图。
// 期望版本为 0 表示调用方未声明版本（内部调用或旧客户端），跳过校验以保持兼容。
func (s *Service) verifyExpectedVersion(ctx context.Context, p platform.Principal, permission, itemID string, expected uint64) error {
	if expected == 0 {
		return nil
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Version != expected {
		return ErrConflict
	}
	return nil
}

// conflictKind 把能力校验产生的冲突描述归类，供预警规则的 check_type 匹配。
func conflictKind(conflict string) string {
	switch {
	case strings.Contains(conflict, "资质"):
		return warningCheckQualification
	case strings.Contains(conflict, "缺少能力"):
		return warningCheckCapability
	default:
		return warningCheckOther
	}
}

// warningRuleMatches 判断预警规则是否覆盖某条冲突：check_type 为空表示不限类型，
// 否则按冲突类别名或冲突原文做子串匹配（配置里写"资质能力冲突"或"资质"都能命中）。

// warningRuleMatches 判断预警规则是否覆盖某条冲突：check_type 为空表示不限类型，
// 否则按冲突类别名或冲突原文做子串匹配（配置里写"资质能力冲突"或"资质"都能命中）。
func warningRuleMatches(rule domain.Rule, conflict string) bool {
	checkType := strings.TrimSpace(rule.CheckType)
	if checkType == "" {
		return true
	}
	return strings.Contains(conflictKind(conflict), checkType) || strings.Contains(conflict, checkType)
}

// warningThreshold 解析规则的冲突数量阈值，允许 "3" 或 "连续 3 项冲突" 这类写法；
// 空值/非法值按 1 处理，保持"配了规则至少一条冲突即告警"的直觉语义。

// warningThreshold 解析规则的冲突数量阈值，允许 "3" 或 "连续 3 项冲突" 这类写法；
// 空值/非法值按 1 处理，保持"配了规则至少一条冲突即告警"的直觉语义。
func warningThreshold(rule domain.Rule) int {
	raw := strings.TrimSpace(rule.Threshold)
	if raw == "" {
		return 1
	}
	digits := strings.Builder{}
	for _, symbol := range raw {
		if symbol >= '0' && symbol <= '9' {
			digits.WriteRune(symbol)
			continue
		}
		if digits.Len() > 0 {
			break
		}
	}
	if digits.Len() == 0 {
		return 1
	}
	value, err := strconv.Atoi(digits.String())
	if err != nil || value < 1 {
		return 1
	}
	return value
}

// uniqueStrings 去重并保持首次出现顺序，避免多条规则命中同一条冲突时重复上报。

// uniqueStrings 去重并保持首次出现顺序，避免多条规则命中同一条冲突时重复上报。
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Service) authorizeProject(ctx context.Context, p platform.Principal, permission, projectID string) error {
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	_, err = s.Repo.GetProject(ctx, filter, projectID)
	return err
}

func (s *Service) authorizeServiceItem(ctx context.Context, p platform.Principal, permission, itemID string) error {
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	_, err = s.Repo.GetServiceItem(ctx, filter, itemID)
	return err
}

func deliveryEvent(p platform.Principal, projectID, itemID, typ string, payload map[string]any) domain.DeliveryEvent {
	return domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: p.TenantID, ProjectID: projectID, ServiceItemID: itemID, Type: typ, ActorUserID: p.UserID, Payload: payload, CreatedAt: time.Now().UTC()}
}

func projectID(now time.Time) string {
	return "PJ-" + now.Format("2006") + "-" + strings.ToUpper(ulid.Make().String()[20:])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

// splitGroup 是按生效分组方案合并后的一个服务项：清单行 + 已解析的拆解口径。

func joinUnique(existing, next string) string {
	if next == "" {
		return existing
	}
	for _, part := range strings.Split(existing, "、") {
		if part == next {
			return existing
		}
	}
	if existing == "" {
		return next
	}
	return existing + "、" + next
}

// effectiveWindowStart 返回用于设备检定校验的窗口开始日：
// 行级时段留空表示“全程使用”，此时取计划开始日；否则取行级填写值。
func effectiveWindowStart(windowStart, planStart time.Time) time.Time {
	if windowStart.IsZero() {
		return planStart
	}
	return windowStart
}
