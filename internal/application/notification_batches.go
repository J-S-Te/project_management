package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// publishNotificationMessage 依照平台五百人上限拆批，保留冻结受众的稳定幂等身份。
// 参数：ctx 为投递请求，message 为原始通知与已冻结收件人。
// 返回值：全部批次获平台确认返回 nil；任一失败保留原 Outbox 供重试。
func (s *Service) publishNotificationMessage(ctx context.Context, message domain.NotificationMessage) error {
	recipients := notificationRecipients(message.Recipients)
	event := platform.NotificationEvent{
		EventID: message.EventID, EventType: message.EventType, Scope: message.Scope,
		Priority: message.Priority, Title: message.Title, Content: message.Content,
		ReferenceType: message.ReferenceType, ReferenceID: message.ReferenceID, TargetURL: message.TargetURL,
		Recipients: recipients, OccurredAt: message.OccurredAt, IdempotencyKey: message.IdempotencyKey,
	}
	const maximumRecipients = 500
	if len(recipients) <= maximumRecipients {
		return s.Notifications.Publish(ctx, event)
	}
	slices.Sort(recipients)
	for start := 0; start < len(recipients); start += maximumRecipients {
		if s.CheckBusinessLicense != nil {
			if err := s.CheckBusinessLicense(ctx); err != nil {
				return err
			}
		}
		event.Recipients = recipients[start:min(start+maximumRecipients, len(recipients))]
		digest := sha256.Sum256([]byte(message.EventID + "\x00" + message.IdempotencyKey +
			"\x00" + strings.Join(event.Recipients, "\n")))
		identity := "approval-batch-" + hex.EncodeToString(digest[:])
		event.EventID, event.IdempotencyKey = identity, identity
		if err := s.Notifications.Publish(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// notificationRecipients 清理站内信收件人，不套用人员姓名查询的数量截断规则。
// 参数：users 为持久化或角色目录解析出的用户编号。
// 返回值：去空、去重并保留原顺序的完整收件人列表。
func notificationRecipients(users []string) []string {
	clean := make([]string, 0, len(users))
	for _, user := range users {
		if id := strings.TrimSpace(user); id != "" {
			clean = append(clean, id)
		}
	}
	return uniqueStrings(clean)
}
