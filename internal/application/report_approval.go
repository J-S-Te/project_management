package application

import (
	"net/url"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// AttachApprovalNotification 为审批申请和处理结果附加事务内持久提醒。
// 参数：event 为服务端生成的交付事件，仅从可信载荷取审批人与申请人。
// 返回值：事件属于审批链时返回 true，避免落入普通参与人广播。
func AttachApprovalNotification(event *domain.DeliveryEvent) bool {
	if event == nil {
		return false
	}
	requestID := event.ID
	message := domain.NotificationMessage{EventID: event.ID + "-approval", EventType: event.Type,
		Scope: platform.NotificationScopeCrossSystem, Priority: "NORMAL", OccurredAt: event.CreatedAt,
		IdempotencyKey: event.ID + "-approval", ReferenceType: "project_approval"}
	switch event.Type {
	case EventRollbackRequested:
		message.Title, message.Content = "现场回退待审批", "有现场回退申请，请查看原因并审批。"
		message.RecipientRole, message.ExcludeUserID = "technical_director", event.ActorUserID
		if payloadText(event.Payload, "kind") == "REPORT_TO_FIELD" {
			message.Title, message.Content = "报告返工待审批", "有报告返工申请，请技术总监查看原因并审批。"
			message.RecipientRole = "technical_director"
		}
	case EventReportCorrectionRequested:
		message.Title, message.Content = "报告更正待审批", "有已签发或已归档报告申请更正，请查看原因并审批。"
		message.RecipientRole, message.ExcludeUserID = "technical_director", event.ActorUserID
	case EventRollbackApproved, EventRollbackRejected, EventRollbackWithdrawn:
		requestID = payloadText(event.Payload, "request_id")
		subject := "现场回退申请"
		if payloadText(event.Payload, "kind") == "REPORT_TO_FIELD" {
			subject = "报告返工申请"
		}
		if event.Type != EventRollbackWithdrawn {
			message.Title, message.Content = approvalOutcomeMessage(subject, event.Type)
		}
		message.Recipients = []string{payloadText(event.Payload, "rollback_requester_id")}
		if event.Type == EventRollbackWithdrawn {
			message.Title, message.Content = "回退申请已撤回", "申请人已撤回申请，无需继续审批。"
			message.Recipients = nil
			message.RecipientRole, message.ExcludeUserID = "technical_director", event.ActorUserID
		}
	case EventReportCorrectionApproved, EventReportCorrectionRejected:
		requestID = payloadText(event.Payload, "request_id")
		message.Title, message.Content = approvalOutcomeMessage("报告更正申请", event.Type)
		message.Recipients = []string{payloadText(event.Payload, "report_correction_requester_id")}
	default:
		return false
	}
	message.Recipients = notificationRecipients(message.Recipients)
	message.ReferenceID = requestID
	message.TargetURL = "/project_management/approvals?request_id=" + url.QueryEscape(requestID)
	if requestID != "" && (len(message.Recipients) > 0 || message.RecipientRole != "") {
		event.Notification = &message
	}
	return true
}

// approvalOutcomeMessage 生成申请人能直接辨认审批结果的通知。
// 参数：subject 为申请类型名称，eventType 为通过或驳回的事件类型。
// 返回值：包含审批结果的通知标题和正文。
func approvalOutcomeMessage(subject, eventType string) (string, string) {
	outcome := "已通过"
	if approvalDecisionStatus(eventType) == "REJECTED" {
		outcome = "已驳回"
	}
	title := subject + outcome
	return title, title + "，请查看审批意见。"
}

// approvalDecisionStatus 将终决事件转换为审批详情状态。
// 参数：eventType 为最后一条有效终决事件类型，空串表示未处理。
// 返回值：PENDING、APPROVED、REJECTED 或 WITHDRAWN。
func approvalDecisionStatus(eventType string) string {
	for _, status := range []string{"APPROVED", "REJECTED", "WITHDRAWN"} {
		if strings.HasSuffix(eventType, "_"+status) {
			return status
		}
	}
	return "PENDING"
}
