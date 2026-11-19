package mysql

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// createSpecialMethodReviewRequest 保存本轮真实待复核事实与事务通知。
// 参数：tx 为业务事务，item 为已进入PENDING的记录，user 为可信手动提交者，now 为进入时间。
// 返回值：普通服务项或幂等重放不写入；任一持久化失败使业务事务回滚。
func createSpecialMethodReviewRequest(tx *gorm.DB, item serviceItemRecord, user string, now time.Time) error {
	if item.Special != "是" || item.Status != "待分配" || item.TechReviewStatus != "PENDING" {
		return nil
	}
	id := approvalNoticeID("special-method-request", item.TenantID, item.ID, strconv.FormatUint(item.Version, 10))
	user = strings.TrimSpace(user)
	payload, err := json.Marshal(map[string]any{"review_requester_user_id": user, "request_item_version": item.Version})
	if err != nil {
		return err
	}
	actor := user
	if actor == "" {
		actor = "SYSTEM"
	}
	created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deliveryEventRecord{ID: id,
		TenantID: item.TenantID, ProjectID: item.ProjectID, ServiceItemID: item.ID,
		EventType: "SPECIAL_METHOD_REVIEW_REQUESTED", ActorUserID: actor, Payload: payload, CreatedAt: now})
	if created.Error != nil || created.RowsAffected == 0 {
		return created.Error
	}
	message := domain.NotificationMessage{EventID: "N_" + id, EventType: "SPECIAL_METHOD_REVIEW_REQUESTED",
		Scope: platform.NotificationScopeCrossSystem, Priority: "NORMAL", Title: "特殊方法待复核",
		Content: "有一项特殊方法待复核，请进入服务项核验。", ReferenceType: "SERVICE_ITEM", ReferenceID: item.ID,
		RecipientRole: "technical_director", OccurredAt: now, IdempotencyKey: "N_" + id,
		TargetURL: "/project_management/methods?" + url.Values{"service_item_id": {item.ID}}.Encode()}
	return createNotificationOutbox(tx, domain.DeliveryEvent{ID: id, TenantID: item.TenantID,
		ProjectID: item.ProjectID, ServiceItemID: item.ID, Type: message.EventType, CreatedAt: now, Notification: &message})
}

// attachSpecialMethodResult 由本轮持久待审事实确定原提交者，绑定精确决定事件。
// 参数：tx 为已锁服务项的事务，item 为原状态，event 为实际审批决定。
// 返回值：无可靠人工提交者时保留空身份且不发给机器，事实损坏时失败关闭。
func attachSpecialMethodResult(tx *gorm.DB, item serviceItemRecord, event *domain.DeliveryEvent) error {
	var request deliveryEventRecord
	err := tx.Where("tenant_id=? AND service_item_id=? AND event_type=?", item.TenantID, item.ID,
		"SPECIAL_METHOD_REVIEW_REQUESTED").Order("created_at DESC,id DESC").Take(&request).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	user := ""
	if err == nil {
		var payload map[string]any
		if err := json.Unmarshal(request.Payload, &payload); err != nil {
			return err
		}
		user = strings.TrimSpace(stringValue(payload, "review_requester_user_id"))
	}
	event.Payload["review_requester_user_id"] = user
	event.Payload["review_request_id"] = request.ID
	if user == "" {
		event.Notification = nil
		return nil
	}
	status := "已通过"
	if stringValue(event.Payload, "decision") == "REJECTED" {
		status = "已驳回"
	}
	message := domain.NotificationMessage{EventID: "N_" + approvalNoticeID("special-method-result", item.TenantID, event.ID),
		EventType: event.Type, Scope: platform.NotificationScopeCrossSystem, Priority: "NORMAL",
		Title: "特殊方法复核" + status, Content: "您提交的特殊方法复核" + status + "，请查看决定。",
		ReferenceType: "SPECIAL_METHOD_REVIEW", ReferenceID: event.ID, Recipients: []string{user},
		OccurredAt: event.CreatedAt, TargetURL: "/project_management/methods?" + url.Values{
			"service_item_id": {item.ID}, "review_id": {event.ID}}.Encode()}
	message.IdempotencyKey = message.EventID
	event.Notification = &message
	return nil
}

// attachDeviationResult 从原上报事实取真实报告人，保留具体偏离与决定语义。
// 参数：event 为本次决定，reported 为同租户查询取得的不可变上报事实。
// 返回值：无；没有可信上报者时不构造收件人，非法决定仍由原状态机拒绝。
func attachDeviationResult(event *domain.DeliveryEvent, reported deliveryEventRecord) {
	user := strings.TrimSpace(reported.ActorUserID)
	if user == "" {
		return
	}
	decision := map[string]string{"RELEASE": "已放行", "RETEST": "需重新测试", "TERMINATE": "已终止"}[stringValue(event.Payload, "decision")]
	if decision == "" {
		return
	}
	deviationID := stringValue(event.Payload, "deviation_id")
	event.Payload["deviation_requester_user_id"] = user
	message := domain.NotificationMessage{EventID: "N_" + approvalNoticeID("deviation-result", event.TenantID, event.ID),
		EventType: event.Type, Scope: platform.NotificationScopeCrossSystem, Priority: "NORMAL",
		Title: "现场偏离" + decision, Content: "您上报的现场偏离" + decision + "，请查看评审决定。",
		ReferenceType: "DEVIATION", ReferenceID: deviationID, Recipients: []string{user}, OccurredAt: event.CreatedAt,
		TargetURL: "/project_management/exceptions?" + url.Values{"service_item_id": {event.ServiceItemID},
			"deviation_id": {deviationID}}.Encode()}
	message.IdempotencyKey = message.EventID
	event.Notification = &message
}
