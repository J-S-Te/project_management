package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"strings"
	"time"
)

type NotificationOutboxRepository interface {
	ClaimNotificationOutbox(context.Context, string, string, int, time.Time) ([]domain.NotificationOutboxItem, error)
	MarkNotificationOutboxSent(context.Context, string, uint64, string, time.Time) error
	MarkNotificationOutboxRetry(context.Context, string, uint64, string, time.Time, time.Time, bool) error
}

type NotificationOutboxEnqueuer interface {
	EnqueueNotification(context.Context, string, domain.NotificationMessage) (bool, error)
}

// enqueueBusinessNotification 是未附着于同一交付事件事务的通知
// 的唯一生产路径。保持该边界明确，防止未来功能
// 静默重新引入同步平台调用，并在暂时
// 网络故障后丢失通知。
func (s *Service) enqueueBusinessNotification(ctx context.Context, tenantID string, message domain.NotificationMessage) (bool, error) {
	outbox, ok := s.Repo.(NotificationOutboxEnqueuer)
	if !ok {
		return false, errors.New("notification outbox enqueuer unavailable")
	}
	return outbox.EnqueueNotification(ctx, strings.TrimSpace(tenantID), message)
}

// syncExistingContract 对已经存在的合同做幂等收尾：同步盖章状态并返回既有项目。

// ScanSlaNotifications 扫描一个租户的 SLA 超期/临近项并投递站内提醒。
//
// 这是"主动提醒"的唯一入口：平台此前没有任何周期任务，SLA 只能在有人打开页面查询时可见，
// 超期不会主动告诉任何人。扫描按租户执行，使用租户边界而非某个登录用户的授权
// （系统侧任务，不代替用户的权限判断）。
//
// 幂等键按「服务项 + 口径 + UTC 日期」生成：同一天重复扫描不会重复打扰，
// 跨天仍会重新提醒（超期是持续状态，每天都值得提醒一次）。
func (s *Service) ScanSlaNotifications(ctx context.Context, tenantID string, now time.Time) (int, error) {
	if s.CheckBusinessLicense != nil {
		if err := s.CheckBusinessLicense(ctx); err != nil {
			return 0, err
		}
	}
	outbox, hasOutbox := s.Repo.(NotificationOutboxEnqueuer)
	if strings.TrimSpace(tenantID) == "" {
		return 0, nil
	}
	if !hasOutbox {
		return 0, errors.New("notification outbox enqueuer unavailable")
	}
	filter := platform.ScopeFilter{TenantID: strings.TrimSpace(tenantID), AllowAll: true}
	repo, ok := s.Repo.(DeliveryRepository)
	if !ok {
		return 0, errors.New("delivery repository unavailable")
	}
	candidates, err := repo.ListSlaOverdue(ctx, filter)
	if err != nil {
		return 0, err
	}
	rules, err := s.Repo.ListRules(ctx, filter.TenantID, "sla")
	if err != nil {
		return 0, err
	}
	items, unparsable := computeSlaItems(candidates, rules, now)
	if unparsable > 0 && s.Logger != nil {
		s.Logger.Warn("sla scan skipped items with unparsable planned_end", "tenant_id", tenantID, "skipped", unparsable)
	}
	day := now.UTC().Format("2006-01-02")
	published := 0
	for _, item := range items {
		recipients := s.itemAssignees(ctx, filter.TenantID, item.ID)
		if len(recipients) == 0 {
			continue
		}
		title, content := slaNotificationText(item)
		notification := domain.NotificationMessage{
			EventID:        ulid.Make().String(),
			EventType:      "SLA_" + item.Kind,
			Scope:          platform.NotificationScopeCrossSystem,
			Priority:       slaNotificationPriority(item.Kind),
			Title:          title,
			Content:        content,
			ReferenceType:  "service_item",
			ReferenceID:    item.ID,
			Recipients:     recipients,
			OccurredAt:     now.UTC(),
			IdempotencyKey: fmt.Sprintf("sla-%s-%s-%s", item.ID, item.Kind, day),
		}
		if s.CheckBusinessLicense != nil {
			if err := s.CheckBusinessLicense(ctx); err != nil {
				return published, err
			}
		}
		queued, queueErr := outbox.EnqueueNotification(ctx, filter.TenantID, notification)
		if queueErr != nil {
			return published, queueErr
		}
		if queued {
			published++
		}
	}
	return published, nil
}

// slaNotificationText 生成 SLA 提醒的标题与正文；超期按高优先级，临近按普通。

// slaNotificationText 生成 SLA 提醒的标题与正文；超期按高优先级，临近按普通。
func slaNotificationText(item domain.SlaOverdueItem) (string, string) {
	switch item.Kind {
	case domain.SlaKindStatusApproaching:
		return "服务项临近 SLA 时限", fmt.Sprintf("服务项 %s 停留在「%s」的剩余时间不足，请及时推进。", item.ID, item.Status)
	case domain.SlaKindStatusOverdue:
		return "服务项状态停留超期", fmt.Sprintf("服务项 %s 停留在「%s」已超过规则时限 %d 小时（超期 %d 小时）。", item.ID, item.Status, item.DeadlineHours, item.OverdueHours)
	default:
		return "服务项计划完成超期", fmt.Sprintf("服务项 %s 的计划完成时间已过 %d 小时。", item.ID, item.OverdueHours)
	}
}

// slaNotificationPriority 让超期类提醒不被淹没在普通通知里。

// slaNotificationPriority 让超期类提醒不被淹没在普通通知里。
func slaNotificationPriority(kind string) string {
	if kind == domain.SlaKindStatusApproaching {
		return "NORMAL"
	}
	return "HIGH"
}

// verifyExpectedVersion 在写入前校验客户端声明的服务项版本。
//
// 这是"先能检测、能提示"的那一半：实际写入已在事务内加行锁并做条件更新，不会产生脏写；
// 但客户端此前拿不到任何版本信号，两人同时改同一对象时后者会静默覆盖前者的意图。
// 期望版本为 0 表示调用方未声明版本（内部调用或旧客户端），跳过校验以保持兼容。

// assignmentRoleUserIDs 从基础平台读取指定项目角色的有效授权用户。候选列表与提交校验
// 共用该口径，避免调用方绕过前端直接提交“有资质但没有对应职责权限”的人员。
func (s *Service) assignmentRoleUserIDs(ctx context.Context, roleCode string) (map[string]struct{}, error) {
	roleCode = strings.ToLower(strings.TrimSpace(roleCode))
	switch roleCode {
	case assignmentRoleTeamLead, assignmentRoleProjectManager, assignmentRoleEngineer:
	default:
		return nil, ValidationError("人员职责必须是团队负责人、项目经理或工程师")
	}
	if s.Personnel == nil {
		return nil, ErrPersonnelUnavailable
	}
	allowed := map[string]struct{}{}
	for page := 1; ; page++ {
		result, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{
			RoleCodes: []string{roleCode}, Page: page, PageSize: assignmentRoleDirectorySize,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPersonnelUnavailable, err)
		}
		for _, person := range result.Items {
			if userID := strings.TrimSpace(person.UserID); userID != "" {
				allowed[userID] = struct{}{}
			}
		}
		if len(result.Items) == 0 || result.Total <= int64(page*assignmentRoleDirectorySize) {
			break
		}
	}
	return allowed, nil
}

// ListQualifiedPersonnel 是唯一指派选择器来源。人员必须
// 同时拥有有效、关联身份的 PERSON 资质记录，
// 以及请求的平台有效角色。人员日期不约束
// 指派，只有设备校准使用有效期窗口。

func (s *Service) applyEvent(ctx context.Context, event domain.DeliveryEvent) error {
	repo, e := s.deliveryRepo()
	if e != nil {
		return e
	}
	s.attachNotification(ctx, &event)
	if err := repo.ApplyDeliveryEvent(ctx, event); err != nil {
		if errors.Is(err, ErrAlreadyApplied) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) attachNotification(ctx context.Context, event *domain.DeliveryEvent) {
	if event == nil || strings.TrimSpace(event.ServiceItemID) == "" {
		return
	}
	spec, ok := assignmentNotificationFor(event.Type)
	if !ok {
		return
	}
	recipients := assignmentRecipients(*event)
	if len(recipients) == 0 {
		recipients = s.itemAssignees(ctx, event.TenantID, event.ServiceItemID)
	}
	if len(recipients) == 0 {
		return
	}
	event.Notification = &domain.NotificationMessage{
		EventID: ulid.Make().String(), EventType: event.Type,
		Scope: platform.NotificationScopeCrossSystem, Priority: "NORMAL",
		Title: spec.Title, Content: spec.Content, ReferenceType: "service_item",
		ReferenceID: event.ServiceItemID, Recipients: uniqueStrings(recipients),
		OccurredAt: event.CreatedAt, IdempotencyKey: event.ID + "-assigned",
	}
}

// DispatchNotificationOutbox 为持久通知取得租约，并通过有界重试发布。
// 生产事务已经提交；投递至少一次，平台
// 幂等键保证重试安全。

// DispatchNotificationOutbox 为持久通知取得租约，并通过有界重试发布。
// 生产事务已经提交；投递至少一次，平台
// 幂等键保证重试安全。
func (s *Service) DispatchNotificationOutbox(ctx context.Context, tenantID, workerID string, limit int, now time.Time) (int, error) {
	if err := ValidateOutboxIdentity(tenantID, workerID); err != nil {
		return 0, err
	}
	repo, ok := s.Repo.(NotificationOutboxRepository)
	if !ok {
		return 0, errors.New("notification outbox repository unavailable")
	}
	if s.CheckBusinessLicense != nil {
		if err := s.CheckBusinessLicense(ctx); err != nil {
			return 0, err
		}
	}
	if s.Notifications == nil {
		return 0, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	started := time.Now()
	sent := 0
	for processed := 0; processed < limit && time.Since(started) < 4*time.Minute; processed++ {
		if err := ctx.Err(); err != nil {
			return sent, err
		}
		if s.CheckBusinessLicense != nil {
			if err := s.CheckBusinessLicense(ctx); err != nil {
				return sent, err
			}
		}
		items, err := repo.ClaimNotificationOutbox(ctx, tenantID, workerID, 1, now.Add(time.Since(started)))
		if err != nil {
			return sent, err
		}
		if len(items) == 0 {
			break
		}
		item := items[0]
		if s.CheckBusinessLicense != nil {
			if err := s.CheckBusinessLicense(ctx); err != nil {
				return sent, err
			}
		}
		message := item.Message
		err = s.Notifications.Publish(ctx, platform.NotificationEvent{
			EventID: message.EventID, EventType: message.EventType, Scope: message.Scope,
			Priority: message.Priority, Title: message.Title, Content: message.Content,
			ReferenceType: message.ReferenceType, ReferenceID: message.ReferenceID,
			Recipients: message.Recipients, OccurredAt: message.OccurredAt,
			IdempotencyKey: message.IdempotencyKey,
		})
		// An accepted publication may cross a license boundary. Leave its lease intact;
		// recovery republishes the persisted payload and key without spending an attempt.
		if s.CheckBusinessLicense != nil {
			if checkErr := s.CheckBusinessLicense(ctx); checkErr != nil {
				return sent, checkErr
			}
		}
		ackNow := now.Add(time.Since(started))
		if err == nil {
			if markErr := repo.MarkNotificationOutboxSent(ctx, tenantID, item.ID, workerID, ackNow); markErr != nil {
				return sent, markErr
			}
			sent++
			continue
		}
		dead := item.RetryCount+1 >= 12
		backoff := time.Duration(1<<min(int(item.RetryCount), 8)) * time.Minute
		if markErr := repo.MarkNotificationOutboxRetry(ctx, tenantID, item.ID, workerID, ackNow, ackNow.Add(backoff), dead); markErr != nil {
			return sent, markErr
		}
		if s.Logger != nil {
			s.Logger.Warn("notification outbox delivery failed", "outbox_id", item.ID, "error", err, "dead_letter", dead)
		}
	}
	return sent, nil
}

// itemAssignees 读取服务项当前的被指派人（团队负责人、项目经理、实施工程师）。
// 供通知这类系统侧派生副作用使用：只按租户边界读取，不叠加调用者授权
// （调用者已经通过各自的操作权限鉴权，这里只是为了把提醒发给"需要行动的人"）。

// itemAssignees 读取服务项当前的被指派人（团队负责人、项目经理、实施工程师）。
// 供通知这类系统侧派生副作用使用：只按租户边界读取，不叠加调用者授权
// （调用者已经通过各自的操作权限鉴权，这里只是为了把提醒发给"需要行动的人"）。
func (s *Service) itemAssignees(ctx context.Context, tenantID, itemID string) []string {
	if strings.TrimSpace(itemID) == "" {
		return nil
	}
	item, err := s.Repo.GetServiceItem(ctx, platform.ScopeFilter{TenantID: tenantID, AllowAll: true}, itemID)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("resolve notification assignees failed", "service_item_id", itemID, "error", err)
		}
		return nil
	}
	recipients := make([]string, 0, 3)
	if teamLead := strings.TrimSpace(item.TeamLeadID); teamLead != "" {
		recipients = append(recipients, teamLead)
	}
	if manager := strings.TrimSpace(item.ProjectManagerID); manager != "" {
		recipients = append(recipients, manager)
	}
	for _, engineer := range item.EngineerIDs {
		if trimmed := strings.TrimSpace(engineer); trimmed != "" {
			recipients = append(recipients, trimmed)
		}
	}
	return recipients
}

// assignmentNotification 描述一条"指派类"事件对应的站内提醒文案。

// assignmentNotification 描述一条"指派类"事件对应的站内提醒文案。
type assignmentNotification struct {
	Title   string
	Content string
}

// assignmentNotificationFor 给出指派类事件的提醒文案；非指派事件返回 false。

// assignmentNotificationFor 给出指派类事件的提醒文案；非指派事件返回 false。
func assignmentNotificationFor(eventType string) (assignmentNotification, bool) {
	switch eventType {
	case EventTeamAssigned:
		return assignmentNotification{
			Title:   "已指派团队负责人",
			Content: "你被指派为服务项的团队负责人，请在「任务分配」中指派项目经理与实施工程师。",
		}, true
	case EventExecutionTeamAssigned:
		return assignmentNotification{
			Title:   "已指派项目经理与实施工程师",
			Content: "你被指派到该服务项，请在「实施计划」中确认排期并推进交付。",
		}, true
	case EventTeamAssignmentRevoked:
		return assignmentNotification{
			Title:   "团队负责人分配已撤销",
			Content: "你不再负责该服务项；撤销原因已记录，请关注后续重新分配。",
		}, true
	case EventExecutionAssignmentRevoked:
		return assignmentNotification{
			Title:   "执行团队分配已撤销",
			Content: "你不再负责该服务项；撤销原因已记录，请关注后续重新分配。",
		}, true
	case EventDecompositionReturned:
		return assignmentNotification{
			Title:   "服务项已退回拆解确认",
			Content: "该服务项已退出任务分配并退回拆解确认；原分配已失效，退回原因已记录。",
		}, true
	case EventRollbackApproved:
		return assignmentNotification{Title: "回退申请已批准", Content: "你的项目回退申请已批准，服务项已按补偿规则回退。"}, true
	case EventRollbackRejected:
		return assignmentNotification{Title: "回退申请已驳回", Content: "你的项目回退申请已驳回，请查看审批意见。"}, true
	case EventImplementationPlanned:
		return assignmentNotification{
			Title:   "实施计划已发布",
			Content: "该服务项的实施计划已发布，请按排期推进现场实施。",
		}, true
	case EventPreparationStarted:
		return assignmentNotification{
			Title:   "实施准备已发起",
			Content: "该服务项已进入实施准备，请确认设备已就位并按计划开展现场实施。",
		}, true
	case EventDeviationReported:
		return assignmentNotification{
			Title:   "有偏离待评审",
			Content: "该服务项上报了实施偏离，请及时安排评审。",
		}, true
	case EventReportStatusUpdated:
		return assignmentNotification{
			Title:   "报告阶段已推进",
			Content: "该服务项的报告阶段已推进，请按当前阶段继续处理。",
		}, true
	default:
		return assignmentNotification{}, false
	}
}

// assignmentRecipients 从事件载荷取被指派人：团队负责人、项目经理、实施工程师。

// assignmentRecipients 从事件载荷取被指派人：团队负责人、项目经理、实施工程师。
func assignmentRecipients(event domain.DeliveryEvent) []string {
	recipients := make([]string, 0, 3)
	if teamLead := payloadText(event.Payload, "team_lead_id"); teamLead != "" {
		recipients = append(recipients, teamLead)
	}
	if manager := payloadText(event.Payload, "project_manager_id"); manager != "" {
		recipients = append(recipients, manager)
	}
	recipients = append(recipients, payloadTextList(event.Payload, "engineer_ids")...)
	recipients = append(recipients, payloadTextList(event.Payload, "revoked_user_ids")...)
	if requester := payloadText(event.Payload, "rollback_requester_id"); requester != "" {
		recipients = append(recipients, requester)
	}
	// 实施计划的人员清单就是需要行动的现场实施人员：只取人员行，设备行不作收件人。
	if resources, ok := event.Payload["personnel"].([]domain.PlanResource); ok {
		for _, resource := range resources {
			if !strings.EqualFold(strings.TrimSpace(resource.ResourceType), "PERSON") {
				continue
			}
			if resourceID := strings.TrimSpace(resource.ResourceID); resourceID != "" {
				recipients = append(recipients, resourceID)
			}
		}
	}
	return recipients
}

// payloadText 读取事件载荷里的字符串字段，缺失或类型不符时返回空串。

// payloadText 读取事件载荷里的字符串字段，缺失或类型不符时返回空串。
func payloadText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

// payloadTextList 读取事件载荷里的字符串数组字段，逐项去掉空白并跳过空值。

// payloadTextList 读取事件载荷里的字符串数组字段，逐项去掉空白并跳过空值。
func payloadTextList(payload map[string]any, key string) []string {
	values, _ := payload[key].([]string)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// conflictKind 把能力校验产生的冲突描述归类，供预警规则的 check_type 匹配。
