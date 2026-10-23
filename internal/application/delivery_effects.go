package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

type DeliveryEffectsRepository interface {
	ClaimDeliveryEffects(context.Context, string, string, int, time.Time) ([]domain.DeliveryEffectsItem, error)
	CompleteDeliveryEffects(context.Context, string, uint64, string, []domain.DeliveryEvent, []domain.NotificationMessage, time.Time) error
	RetryDeliveryEffects(context.Context, string, uint64, string, time.Time, time.Time, string) error
}

// ValidateOutboxIdentity rejects absent or oversized trusted worker boundaries.
func ValidateOutboxIdentity(tenantID, owner string) error {
	if strings.TrimSpace(tenantID) == "" || len(tenantID) > 64 || strings.TrimSpace(owner) == "" || len(owner) > 128 {
		return ErrValidation
	}
	return nil
}

// SnapshotDeliveryEffects 在生产事务内运行，使用该事务的规则行。
// 后续编辑不能追溯改变已接受命令。
func SnapshotDeliveryEffects(event domain.DeliveryEvent, automations, warnings []domain.Rule) domain.DeliveryEffects {
	effects := domain.DeliveryEffects{ID: ulid.Make().String(), Event: event, RoleNotification: event.RoleNotification}
	if event.Type == EventAutomationTriggered || event.Type == EventWarningTriggered {
		return effects
	}
	for _, rule := range automations {
		target := strings.TrimSpace(rule.Target)
		if rule.Enabled && strings.TrimSpace(rule.Trigger) == event.Type && validateCatalogRole(target) == nil {
			effects.Targets = append(effects.Targets, target)
		}
	}
	effects.Targets = uniqueStrings(effects.Targets)
	conflicts := payloadTextList(event.Payload, "conflicts")
	// 基于 JSON 的载荷将数组解码为 []any。
	if values, ok := event.Payload["conflicts"].([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				conflicts = append(conflicts, text)
			}
		}
	}
	effects.WarningPayload = matchingWarningPayload(warnings, conflicts)
	return effects
}

func matchingWarningPayload(rules []domain.Rule, conflicts []string) map[string]any {
	names, matched := []string{}, []string{}
	threshold := 0
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		hits := []string{}
		for _, conflict := range conflicts {
			if warningRuleMatches(rule, conflict) {
				hits = append(hits, conflict)
			}
		}
		required := warningThreshold(rule)
		if len(hits) < required {
			continue
		}
		names = append(names, rule.Name)
		matched = append(matched, hits...)
		if threshold == 0 || required < threshold {
			threshold = required
		}
	}
	if len(names) == 0 {
		return nil
	}
	return map[string]any{"conflicts": uniqueStrings(matched), "rules": names, "threshold": threshold}
}

// DispatchDeliveryEffects 绝不确认部分接收者查询或部分
// 写入。所有派生事件及通知意图随任务完成共同提交。
// 失败任务无限期保留可重试状态，采用有界退避，不静默丢弃。
func (s *Service) DispatchDeliveryEffects(ctx context.Context, tenantID, workerID string, limit int, now time.Time) (int, error) {
	if err := ValidateOutboxIdentity(tenantID, workerID); err != nil {
		return 0, err
	}
	repo, ok := s.Repo.(DeliveryEffectsRepository)
	if !ok {
		return 0, errors.New("delivery effects repository unavailable")
	}
	if s.CheckBusinessLicense != nil {
		if err := s.CheckBusinessLicense(ctx); err != nil {
			return 0, err
		}
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	started := time.Now()
	completed := 0
	for processed := 0; processed < limit && time.Since(started) < 4*time.Minute; processed++ {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		if s.CheckBusinessLicense != nil {
			if err := s.CheckBusinessLicense(ctx); err != nil {
				return completed, err
			}
		}
		leaseTime := now.Add(time.Since(started))
		// 只认领下一个任务；较早目录查询等待远程响应时，
		// 排队任务不得过期。
		items, err := repo.ClaimDeliveryEffects(ctx, tenantID, workerID, 1, leaseTime)
		if err != nil {
			return completed, err
		}
		if len(items) == 0 {
			break
		}
		item := items[0]
		if item.TenantID != tenantID {
			return completed, ErrValidation
		}
		if s.CheckBusinessLicense != nil {
			if err := s.CheckBusinessLicense(ctx); err != nil {
				return completed, err
			}
		}
		item.Effects.Event.TenantID = tenantID
		jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		var events []domain.DeliveryEvent
		var messages []domain.NotificationMessage
		var buildErr error
		if item.InvalidPayload {
			buildErr = errors.New("invalid delivery effects payload")
		} else {
			events, messages, buildErr = s.buildDeliveryEffects(jobCtx, item.Effects)
		}
		// 目录解析可能跨越许可证绝对到期时间。保留
		// 已认领任务，待现有租约过期后恢复，
		// 不能提交新副作用或消耗业务失败次数。
		if s.CheckBusinessLicense != nil {
			if err := s.CheckBusinessLicense(jobCtx); err != nil {
				cancel()
				return completed, err
			}
		}
		if buildErr == nil {
			buildErr = repo.CompleteDeliveryEffects(jobCtx, tenantID, item.ID, workerID, events, messages, now.Add(time.Since(started)))
		}
		cancel()
		if buildErr != nil {
			if s.CheckBusinessLicense != nil {
				if err := s.CheckBusinessLicense(ctx); err != nil {
					return completed, err
				}
			}
			code := "EFFECTS_PROCESSING_FAILED"
			backoff := time.Duration(1<<min(item.RetryCount, 8)) * time.Minute
			if item.InvalidPayload {
				// Preserve the existing invalid-payload retention policy, but apply
				// it only after the post-claim license checks and using fresh time.
				code = "INVALID_EFFECTS_PAYLOAD"
				backoff = time.Hour
			} else if errors.Is(buildErr, ErrPersonnelUnavailable) {
				code = "RECIPIENT_DIRECTORY_UNAVAILABLE"
			}
			retryNow := now.Add(time.Since(started))
			if retryErr := repo.RetryDeliveryEffects(ctx, tenantID, item.ID, workerID, retryNow, retryNow.Add(backoff), code); retryErr != nil {
				return completed, retryErr
			}
			if s.Logger != nil {
				s.Logger.Warn("delivery effects retained for retry", "outbox_id", item.ID, "failure_code", code)
			}
			continue
		}
		completed++
	}
	return completed, nil
}

func (s *Service) buildDeliveryEffects(ctx context.Context, effects domain.DeliveryEffects) ([]domain.DeliveryEvent, []domain.NotificationMessage, error) {
	source := effects.Event
	events := []domain.DeliveryEvent{}
	messages := []domain.NotificationMessage{}
	if len(effects.Targets) > 0 {
		recipients, err := s.deliveryRoleRecipients(ctx, effects.Targets)
		if err != nil {
			return nil, nil, err
		}
		event := domain.DeliveryEvent{ID: effects.ID, TenantID: source.TenantID, ProjectID: source.ProjectID, ServiceItemID: source.ServiceItemID, ActorUserID: source.ActorUserID, Type: EventAutomationTriggered, CreatedAt: source.CreatedAt.Add(time.Millisecond), Payload: map[string]any{"trigger": source.Type, "targets": effects.Targets, "source_event_id": source.ID}}
		events = append(events, event)
		referenceType, referenceID := "service_item", source.ServiceItemID
		if referenceID == "" {
			referenceType, referenceID = "project", source.ProjectID
		}
		messages = append(messages, domain.NotificationMessage{EventID: effects.ID, EventType: EventAutomationTriggered, Scope: platform.NotificationScopeCrossSystem, Priority: "NORMAL", Title: "项目自动化规则触发", Content: fmt.Sprintf("规则命中的事件：%s。项目 %s，服务项 %s。", source.Type, source.ProjectID, source.ServiceItemID), ReferenceType: referenceType, ReferenceID: referenceID, Recipients: recipients, OccurredAt: source.CreatedAt, IdempotencyKey: source.ID + "-automation-notification"})
	}
	if effects.WarningPayload != nil {
		// 独立的确定性 ID 使其可与自动化事件共存。
		id, err := ulid.ParseStrict(effects.ID)
		if err != nil {
			return nil, nil, err
		}
		id[15] ^= 1
		events = append(events, domain.DeliveryEvent{ID: id.String(), TenantID: source.TenantID, ProjectID: source.ProjectID, ServiceItemID: source.ServiceItemID, ActorUserID: source.ActorUserID, Type: EventWarningTriggered, Payload: effects.WarningPayload, CreatedAt: source.CreatedAt.Add(2 * time.Millisecond)})
	}
	if effects.RoleNotification != nil {
		recipients, err := s.deliveryRoleRecipients(ctx, effects.RoleNotification.Roles)
		if err != nil {
			return nil, nil, err
		}
		message := effects.RoleNotification.Message
		message.Recipients = recipients
		messages = append(messages, message)
	}
	// 平台摄取每个事件最多接受 500 个接收者。完整解析后才分批，
	// 并为每个分块保留稳定幂等键。
	batched := make([]domain.NotificationMessage, 0, len(messages))
	for _, message := range messages {
		if len(message.Recipients) <= 500 {
			batched = append(batched, message)
			continue
		}
		for offset := 0; offset < len(message.Recipients); offset += 500 {
			part := message
			part.Recipients = message.Recipients[offset:min(offset+500, len(message.Recipients))]
			part.EventID = fmt.Sprintf("%s-%d", message.EventID, offset/500+1)
			part.IdempotencyKey = fmt.Sprintf("%s-part-%d", message.IdempotencyKey, offset/500+1)
			batched = append(batched, part)
		}
	}
	return events, batched, nil
}

func (s *Service) deliveryRoleRecipients(ctx context.Context, roles []string) ([]string, error) {
	if s.Personnel == nil {
		return nil, ErrPersonnelUnavailable
	}
	recipients := []string{}
	seen := map[string]bool{}
	for _, role := range uniqueStrings(roles) {
		found := false
		expectedTotal := int64(-1)
		roleSeen := map[string]bool{}
		resolved := int64(0)
		for pageNumber := 1; ; pageNumber++ {
			if pageNumber > 10000 {
				return nil, ErrPersonnelUnavailable
			}
			page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{RoleCodes: []string{role}, Page: pageNumber, PageSize: assignmentRoleDirectorySize})
			if err != nil {
				return nil, fmt.Errorf("%w: role directory lookup failed", ErrPersonnelUnavailable)
			}
			if page.Page != pageNumber || page.PageSize != assignmentRoleDirectorySize || page.Total < 0 || len(page.Items) > page.PageSize {
				return nil, ErrPersonnelUnavailable
			}
			if expectedTotal < 0 {
				expectedTotal = page.Total
			}
			if page.Total != expectedTotal {
				return nil, ErrPersonnelUnavailable
			}
			for _, person := range page.Items {
				id := strings.TrimSpace(person.UserID)
				if id == "" || len(id) > 26 {
					return nil, ErrPersonnelUnavailable
				}
				if roleSeen[id] {
					return nil, ErrPersonnelUnavailable
				}
				roleSeen[id] = true
				resolved++
				found = true
				if !seen[id] {
					recipients = append(recipients, id)
					seen[id] = true
				}
			}
			if int64(pageNumber*page.PageSize) >= page.Total {
				if resolved != expectedTotal {
					return nil, ErrPersonnelUnavailable
				}
				break
			}
			if len(page.Items) == 0 {
				return nil, ErrPersonnelUnavailable
			}
		}
		if !found {
			return nil, ErrPersonnelUnavailable
		}
	}
	return recipients, nil
}
