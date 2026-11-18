package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type notificationAudienceRepository interface {
	SaveNotificationAudience(context.Context, string, uint64, string, time.Time, domain.NotificationMessage) error
}

// resolveApprovalRecipients 在投递时解析技术总监角色，目录故障交由 Outbox 重试。
// 参数：ctx 为投递请求，message 为持久化通知及排除的申请人身份。
// 返回值：去重后的有效收件人；目录不可用、无独立审批人或超限时返回错误。
func (s *Service) resolveApprovalRecipients(ctx context.Context, message domain.NotificationMessage) ([]string, error) {
	if message.RecipientRole == "" {
		return message.Recipients, nil
	}
	if message.RecipientRole != "technical_director" || s.Personnel == nil {
		return nil, ErrPersonnelUnavailable
	}
	recipients := []string{}
	// 平台负责人目录实际最多返回五十条，结束条件必须使用同一分页大小。
	const pageSize = 50
	for page := 1; page <= 50; page++ {
		result, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{
			RoleCodes: []string{message.RecipientRole}, Page: page, PageSize: pageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: 审批人目录读取失败", ErrPersonnelUnavailable)
		}
		for _, user := range result.Items {
			userID := strings.TrimSpace(user.UserID)
			if userID != "" && userID != message.ExcludeUserID {
				recipients = append(recipients, userID)
			}
		}
		if len(result.Items) == 0 || result.Total <= int64(page*pageSize) {
			recipients = uniqueStrings(recipients)
			if len(recipients) == 0 {
				return nil, fmt.Errorf("%w: 未找到独立技术总监审批人", ErrPersonnelUnavailable)
			}
			return recipients, nil
		}
	}
	return nil, fmt.Errorf("%w: 审批人目录超过查询上限", ErrPersonnelUnavailable)
}
