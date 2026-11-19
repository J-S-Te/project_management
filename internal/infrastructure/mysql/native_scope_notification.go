package mysql

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

// approvalNoticeID 将真实业务坐标折叠为兼容事件和审计列的稳定ULID。
// 参数：parts 为种类、租户、对象及决定或轮次等可信坐标。
// 返回值：二十六字符合法ULID，不含业务明文；平台事件另加字母前缀。
func approvalNoticeID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	var id ulid.ULID
	copy(id[:], digest[:16])
	return id.String()
}

// nativeScopeRequesterUser 读取服务端持久用户证据，历史主体仅保留可核对的规范候选。
// 参数：tx 为当前事务，project/change 为已锁定的原生项目与提案。
// 返回值：可信用户或旧OIDC规范候选；非规范历史身份返回空，损坏事实返回错误。
func nativeScopeRequesterUser(tx *gorm.DB, project projectRecord, change nativeScopeChangeRecord) (string, error) {
	var request deliveryEventRecord
	id := approvalNoticeID("native-scope-request", project.TenantID, change.ID)
	err := tx.Where("id=? AND tenant_id=? AND project_id=? AND event_type=?", id,
		project.TenantID, project.ID, "NATIVE_SCOPE_CHANGE_REQUESTED").Take(&request).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	if err == nil {
		var payload map[string]any
		if err := json.Unmarshal(request.Payload, &payload); err != nil {
			return "", err
		}
		if user := strings.TrimSpace(stringValue(payload, "scope_requester_user_id")); user != "" {
			return user, nil
		}
	}
	// 原生范围上线时OIDC已强制IdentityID=UserID。历史候选仍须由Worker精确回源核对。
	legacy := strings.TrimSpace(change.RequestedBy)
	if _, err := ulid.ParseStrict(legacy); err != nil {
		return "", nil
	}
	return legacy, nil
}

// createNativeScopeNotification 在提案事务内保存请求或原提交者结果提醒。
// 参数：tx 为当前事务，project/change 为真实对象，action/userID/now 为事实及已核对用户。
// 返回值：无可靠结果用户时不伪造收件人，其余编码或写入失败回滚事务。
func createNativeScopeNotification(tx *gorm.DB, project projectRecord, change nativeScopeChangeRecord,
	action, userID string, now time.Time) error {
	id := approvalNoticeID("native-scope-notice", project.TenantID, change.ID, action)
	message := domain.NotificationMessage{EventID: "N_" + id,
		EventType: action, Scope: platform.NotificationScopeCrossSystem, Priority: "NORMAL",
		ReferenceType: "NATIVE_SCOPE_CHANGE", ReferenceID: change.ID, OccurredAt: now,
		TargetURL: "/project_management/project_detail?" + url.Values{"project": {project.ID}, "scope_change_id": {change.ID}}.Encode()}
	switch action {
	case "NATIVE_SCOPE_CHANGE_REQUESTED":
		message.Title, message.Content = "原生委托范围变更待审批", "有一项原生委托范围变更待审批，请进入项目核验。"
		message.RecipientRole, message.ExcludeUserID = "technical_director,admin", userID
	case "NATIVE_SCOPE_CHANGE_APPROVED", "NATIVE_SCOPE_CHANGE_REJECTED":
		if userID == "" {
			return nil
		}
		status := "已批准"
		if action == "NATIVE_SCOPE_CHANGE_REJECTED" {
			status = "已驳回"
		}
		message.Title, message.Content = "原生委托范围变更"+status, "您的原生委托范围变更"+status+"，请查看决定。"
		message.Recipients = []string{userID}
	default:
		return nil
	}
	message.IdempotencyKey = message.EventID
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return tx.Create(&notificationOutboxRecord{EventID: id, TenantID: project.TenantID,
		EventType: action, IdempotencyKey: message.IdempotencyKey, AggregateType: "native_scope_change",
		AggregateID: change.ID, Payload: payload, Status: "PENDING", CreatedAt: now}).Error
}
