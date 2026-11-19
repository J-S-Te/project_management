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

// resolveApprovalRecipients 解析既有审批角色或核对原生结果用户，故障交由Outbox重试。
// 参数：ctx 为投递请求，message 为持久通知及可信用户排除项。
// 返回值：有效收件人；角色非法、目录故障或没有审批人时返回错误。
func (s *Service) resolveApprovalRecipients(ctx context.Context, message domain.NotificationMessage) ([]string, error) {
	if message.EventType == "NATIVE_SCOPE_CHANGE_APPROVED" || message.EventType == "NATIVE_SCOPE_CHANGE_REJECTED" {
		return s.nativeScopeResultRecipients(ctx, message.Recipients)
	}
	if message.RecipientRole == "" {
		return message.Recipients, nil
	}
	roles := []string{"technical_director"}
	if message.EventType == "NATIVE_SCOPE_CHANGE_REQUESTED" && message.RecipientRole == "technical_director,admin" {
		roles = append(roles, "admin")
	} else if message.RecipientRole != "technical_director" {
		return nil, ErrPersonnelUnavailable
	}
	users := []string{}
	for _, role := range roles {
		roleUsers, err := s.notificationRoleRecipients(ctx, role, message.ExcludeUserID)
		if err != nil {
			return nil, err
		}
		users = append(users, roleUsers...)
	}
	users = notificationRecipients(users)
	if len(users) == 0 {
		return nil, fmt.Errorf("%w: 未找到独立审批人", ErrPersonnelUnavailable)
	}
	return users, nil
}

// notificationRoleRecipients 从租户绑定目录完整读取一个既有角色。
// 参数：ctx 为投递上下文，role 为受控角色，exclude 为真实申请者用户编号。
// 返回值：该角色有效用户；目录故障或超出分页边界返回可重试错误。
func (s *Service) notificationRoleRecipients(ctx context.Context, role, exclude string) ([]string, error) {
	if s.Personnel == nil {
		return nil, ErrPersonnelUnavailable
	}
	recipients := []string{}
	const pageSize = 50
	for page := 1; page <= 50; page++ {
		result, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{RoleCodes: []string{role}, Page: page, PageSize: pageSize})
		if err != nil {
			return nil, fmt.Errorf("%w: 审批人目录读取失败", ErrPersonnelUnavailable)
		}
		for _, user := range result.Items {
			if id := strings.TrimSpace(user.UserID); id != "" && id != exclude {
				recipients = append(recipients, id)
			}
		}
		if len(result.Items) == 0 || result.Total <= int64(page*pageSize) {
			return notificationRecipients(recipients), nil
		}
	}
	return nil, fmt.Errorf("%w: 审批人目录超过查询上限", ErrPersonnelUnavailable)
}

// nativeScopeResultRecipients 精确核对原生范围申请者的真实平台用户编号。
// 参数：ctx 为租户绑定目录上下文，users 为持久事实给出的唯一候选。
// 返回值：目录一致时返回该用户；缺失或故障保持Outbox可重试失败。
func (s *Service) nativeScopeResultRecipients(ctx context.Context, users []string) ([]string, error) {
	if s.Personnel == nil || len(users) != 1 || strings.TrimSpace(users[0]) == "" {
		return nil, ErrPersonnelUnavailable
	}
	userID := strings.TrimSpace(users[0])
	page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{UserID: userID, Page: 1, PageSize: 2})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].UserID != userID {
		return nil, fmt.Errorf("%w: 原申请者用户无法确认", ErrPersonnelUnavailable)
	}
	return []string{userID}, nil
}
