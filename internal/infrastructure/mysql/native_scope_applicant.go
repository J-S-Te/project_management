package mysql

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

type nativeScopeApplicant struct {
	id, projectID, identityID string
}

// readApprovalNativeScopeApplicants 为审批页中可见的范围提案补齐目录用户号。
// 参数：ctx/db 为读取会话，tenant 为可信租户，tasks 为授权后的本页审批事项。
// 返回值：读取故障返回错误；未记录平台用户时仅保留审批归属身份。
func readApprovalNativeScopeApplicants(ctx context.Context, db *gorm.DB, tenant string, tasks []domain.ApprovalTask) error {
	var refs []nativeScopeApplicant
	for _, task := range tasks {
		if task.Type == domain.ApprovalNativeScope {
			refs = append(refs, nativeScopeApplicant{task.ID, task.ProjectID, task.RequesterUserID})
		}
	}
	users, err := nativeScopeApplicantIDs(db.WithContext(ctx), tenant, refs)
	for i := range tasks {
		if tasks[i].Type == domain.ApprovalNativeScope {
			tasks[i].RequesterDirectoryID = users[tasks[i].ID]
		}
	}
	return err
}

// readNativeScopeApplicants 为授权范围提案填入实际平台用户号，供应用层按目录解析。
// 参数：tx 为范围读取事务，tenant 为可信租户，changes 为可见提案。
// 返回值：查询错误；用户号字段只用于服务端展示解析。
func readNativeScopeApplicants(tx *gorm.DB, tenant string, changes []domain.NativeScopeChange) error {
	refs := make([]nativeScopeApplicant, 0, len(changes))
	for _, change := range changes {
		refs = append(refs, nativeScopeApplicant{change.ID, change.ProjectID, change.RequestedBy})
	}
	users, err := nativeScopeApplicantIDs(tx, tenant, refs)
	for i := range changes {
		changes[i].RequestedByUserID = users[changes[i].ID]
	}
	return err
}

// nativeScopeApplicantIDs 按原请求事件的稳定主键批量查找可信目录用户。
// 参数：db 为授权后的查询会话，tenant 为服务端租户，refs 为已确认可见的范围提案坐标。
// 返回值：提案到平台用户号的映射；旧版仅接受可由权威目录精确验证的规范候选。
func nativeScopeApplicantIDs(db *gorm.DB, tenant string, refs []nativeScopeApplicant) (map[string]string, error) {
	users := make(map[string]string, len(refs))
	if len(refs) == 0 {
		return users, nil
	}
	ids := make([]string, 0, len(refs))
	byEventID := make(map[string]nativeScopeApplicant, len(refs))
	for _, ref := range refs {
		id := approvalNoticeID("native-scope-request", tenant, ref.id)
		ids = append(ids, id)
		byEventID[id] = ref
		if _, err := ulid.ParseStrict(ref.identityID); err == nil {
			users[ref.id] = ref.identityID
		}
	}
	var events []deliveryEventRecord
	if err := db.Select("id, project_id, actor_user_id, payload").
		Where("tenant_id=? AND event_type=? AND id IN ?", tenant, "NATIVE_SCOPE_CHANGE_REQUESTED", ids).
		Find(&events).Error; err != nil {
		return nil, err
	}
	for _, event := range events {
		ref := byEventID[event.ID]
		user := nativeScopeApplicantUser(event, ref)
		if user != "" {
			users[ref.id] = user
		}
	}
	return users, nil
}

// nativeScopeApplicantUser 仅从与提案一致的原始请求载荷提取平台用户号。
// 参数：event 为数据库交付事实，ref 为提案的项目、发起人与编号。
// 返回值：可信平台用户号；不一致或缺少字段时返回空。
func nativeScopeApplicantUser(event deliveryEventRecord, ref nativeScopeApplicant) string {
	if event.ProjectID != ref.projectID || event.ActorUserID != ref.identityID {
		return ""
	}
	var payload struct {
		ChangeID string `json:"change_id"`
		UserID   string `json:"scope_requester_user_id"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil || payload.ChangeID != ref.id {
		return ""
	}
	return strings.TrimSpace(payload.UserID)
}
