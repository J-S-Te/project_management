package application

import (
	"context"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
)

func rollbackKindAllowed(kind string, item domain.ServiceItem) bool {
	switch kind {
	case "FIELD_TO_PREPARATION":
		return item.Status == "实施中"
	case "REPORT_TO_FIELD":
		return item.Status == "现场实施完成" && (item.ReportStatus == "COMPILING" || item.ReportStatus == "REVIEWED")
	default:
		return false
	}
}

// RequestRollback 把现场/报告回退变成双人复核：申请本身不改变状态，也不会删除现场或报告证据。

// RequestRollback 把现场/报告回退变成双人复核：申请本身不改变状态，也不会删除现场或报告证据。
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

func (s *Service) DecideRollback(ctx context.Context, p platform.Principal, itemID, requestID string, input domain.RollbackDecisionInput) error {
	if err := validateProjectTextLength(input.Comment, 1000, "回退审批意见"); err != nil {
		return err
	}
	if err := s.authorizeServiceItem(ctx, p, "project.rollback.approve", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.rollback.approve", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "APPROVED" && decision != "REJECTED" {
		return ErrValidation
	}
	if strings.TrimSpace(input.Comment) == "" {
		return ValidationError("请填写回退审批意见")
	}
	filter, err := authorizeProjectScope(p, "project.rollback.approve")
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
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, eventType, map[string]any{"request_id": requestID, "kind": kind, "comment": strings.TrimSpace(input.Comment), "expected_version": input.ExpectedVersion, "rollback_requester_id": payloadText(request.Payload, "requester_user_id")}))
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
