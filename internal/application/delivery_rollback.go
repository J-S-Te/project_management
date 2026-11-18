package application

import (
	"context"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
)

// rollbackKindAllowed 判断当前服务项是否允许申请或批准指定回退。
// 参数：kind 为回退类型，item 为当前服务项状态快照。
// 返回值：满足现场或报告回退白名单时返回 true，否则返回 false。
func rollbackKindAllowed(kind string, item domain.ServiceItem) bool {
	switch kind {
	case "FIELD_TO_PREPARATION":
		return item.Status == "实施中"
	case "REPORT_TO_FIELD":
		return item.Status == "现场实施完成" &&
			(item.ReportStatus == "RECTIFYING" || item.ReportStatus == "COMPILING" || item.ReportStatus == "REVIEWED")
	default:
		return false
	}
}

// RequestRollback 提交由技术总监审批的现场回退或报告返工申请。
// 参数：ctx 为请求，p 为申请人，itemID 为服务项，input 为原因、类型及当前版本。
// 返回值：持久申请编号；权限、状态、审批归属或重复申请不符时返回错误。
func (s *Service) RequestRollback(ctx context.Context, p platform.Principal, itemID string, input domain.RollbackRequestInput) (string, error) {
	if err := validateProjectTextLength(input.Reason, 1000, "回退原因"); err != nil {
		return "", err
	}
	if err := s.authorizeServiceItem(ctx, p, "project.rollback.request", itemID); err != nil {
		return "", err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.rollback.request", itemID, input.ExpectedVersion); err != nil {
		return "", err
	}
	kind, reason := strings.ToUpper(strings.TrimSpace(input.Kind)), strings.TrimSpace(input.Reason)
	if reason == "" {
		return "", ValidationError("请填写回退申请原因")
	}
	filter, err := authorizeProjectScope(p, "project.rollback.request")
	if err != nil {
		return "", err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return "", err
	}
	if !rollbackKindAllowed(kind, item) {
		return "", PreconditionError("当前服务项状态不允许申请该回退")
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return "", err
	}
	events, err := repo.ListDeliveryEvents(ctx, filter, item.ProjectID)
	if err != nil {
		return "", err
	}
	if hasPendingRollback(events, itemID, kind) {
		return "", PreconditionError("该服务项已有同类型待审批回退申请")
	}
	event := deliveryEvent(p, "", itemID, EventRollbackRequested, map[string]any{"kind": kind, "reason": reason, "expected_version": input.ExpectedVersion, "requester_user_id": p.UserID})
	if err := s.applyEvent(ctx, event); err != nil {
		return "", err
	}
	return event.ID, nil
}

func hasPendingRollback(events []domain.DeliveryEvent, itemID, kind string) bool {
	closed := map[string]bool{}
	for _, event := range events {
		if event.Type == EventRollbackApproved || event.Type == EventRollbackRejected || event.Type == EventRollbackWithdrawn {
			closed[payloadText(event.Payload, "request_id")] = true
		}
	}
	for _, event := range events {
		if event.Type == EventRollbackRequested && event.ServiceItemID == itemID && strings.EqualFold(payloadText(event.Payload, "kind"), kind) && !closed[event.ID] {
			return true
		}
	}
	return false
}

// DecideRollback 由归属审批人批准或驳回尚未处理的回退申请。
// 参数：ctx 为请求，p 为审批人，itemID/requestID 为目标身份，input 为决定、意见及版本。
// 返回值：无；越权、自批、版本或状态冲突时返回错误。
func (s *Service) DecideRollback(ctx context.Context, p platform.Principal, itemID, requestID string, input domain.RollbackDecisionInput) error {
	if err := validateProjectTextLength(input.Comment, 1000, "回退审批意见"); err != nil {
		return err
	}
	permission := "project.rollback.approve"
	if err := s.authorizeServiceItem(ctx, p, permission, itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, permission, itemID, input.ExpectedVersion); err != nil {
		return err
	}
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "APPROVED" && decision != "REJECTED" {
		return ErrValidation
	}
	if strings.TrimSpace(input.Comment) == "" {
		return ValidationError("请填写回退审批意见")
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	request, err := s.findApprovalRequest(ctx, filter, item, requestID, EventRollbackRequested)
	if err != nil {
		return err
	}
	if request.ActorUserID == p.UserID {
		return ErrForbidden
	}
	kind := strings.ToUpper(payloadText(request.Payload, "kind"))
	if decision == "APPROVED" && !rollbackKindAllowed(kind, item) {
		return PreconditionError("服务项状态已变化，不能批准该回退")
	}
	eventType := EventRollbackApproved
	if decision == "REJECTED" {
		eventType = EventRollbackRejected
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, eventType, map[string]any{
		"request_id": requestID, "kind": kind, "comment": strings.TrimSpace(input.Comment),
		"expected_version": input.ExpectedVersion, "rollback_requester_id": request.ActorUserID,
	}))
}

// WithdrawRollback 仅允许原申请人在审批前撤回，避免用“驳回”冒充申请人取消。

// WithdrawRollback 仅允许原申请人在审批前撤回，避免用“驳回”冒充申请人取消。
func (s *Service) WithdrawRollback(ctx context.Context, p platform.Principal, itemID, requestID string, input domain.RollbackWithdrawInput) error {
	if err := validateProjectTextLength(input.Reason, 1000, "撤回回退原因"); err != nil {
		return err
	}
	if err := s.authorizeServiceItem(ctx, p, "project.rollback.request", itemID); err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" {
		return ValidationError("请填写撤回原因")
	}
	filter, err := authorizeProjectScope(p, "project.rollback.request")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	events, err := repo.ListDeliveryEvents(ctx, filter, item.ProjectID)
	if err != nil {
		return err
	}
	var request domain.DeliveryEvent
	for _, event := range events {
		if event.ID == requestID && event.ServiceItemID == itemID && event.Type == EventRollbackRequested {
			request = event
			break
		}
	}
	if request.ID == "" {
		return ErrNotFound
	}
	if request.ActorUserID != p.UserID {
		return ErrForbidden
	}
	if !hasPendingRollback([]domain.DeliveryEvent{request}, itemID, payloadText(request.Payload, "kind")) {
		return PreconditionError("该回退申请已处理，不能撤回")
	}
	for _, event := range events {
		if (event.Type == EventRollbackApproved || event.Type == EventRollbackRejected || event.Type == EventRollbackWithdrawn) && payloadText(event.Payload, "request_id") == requestID {
			return PreconditionError("该回退申请已处理，不能撤回")
		}
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventRollbackWithdrawn, map[string]any{"request_id": requestID, "kind": payloadText(request.Payload, "kind"), "reason": strings.TrimSpace(input.Reason), "rollback_requester_id": p.UserID}))
}

// RequestReportCorrection 只允许针对已签发或已归档的当前版本提出更正；申请不改变旧报告有效性。
