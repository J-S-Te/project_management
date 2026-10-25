package application

import (
	"context"
	"errors"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"strings"
	"time"
)

type ReportRevisionRepository interface {
	ListReportRevisions(context.Context, string, string) ([]domain.ReportRevision, error)
	RegisterReportArtifact(context.Context, string, string, uint64, domain.ReportArtifactInput, string, time.Time) error
}

// UpdateReportStatus 服务项报告从编制中逐级推进：编制中→已审核→已签发→已归档。
// 报告推进把项目推向「已完成」，是交付收口动作，使用独立的报告管理权限，
// 不与现场执行权限（project.field.complete）混用。
func (s *Service) UpdateReportStatus(ctx context.Context, p platform.Principal, itemID string, input domain.ReportStatusInput) error {
	// 报告推进拆成两级职责：编制/审核/签发属于报告编制（project.report.manage），
	// 归档会把项目推向"已完成"，属于独立治理动作（project.report.archive）。
	// 现场执行角色不再顺带拥有归档权。
	phase := strings.ToUpper(strings.TrimSpace(input.Phase))
	if err := s.authorizeServiceItem(ctx, p, reportPhasePermission(phase), itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, reportPhasePermission(phase), itemID, input.ExpectedVersion); err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventReportStatusUpdated, map[string]any{"phase": phase, "expected_version": input.ExpectedVersion}))
}

func (s *Service) ListReportRevisions(ctx context.Context, p platform.Principal, itemID string) ([]domain.ReportRevision, error) {
	if err := s.authorizeServiceItem(ctx, p, "project.read", itemID); err != nil {
		return nil, err
	}
	repo, ok := s.Repo.(ReportRevisionRepository)
	if !ok {
		return nil, errors.New("report revision repository unavailable")
	}
	return repo.ListReportRevisions(ctx, p.TenantID, itemID)
}

func (s *Service) RegisterReportArtifact(ctx context.Context, p platform.Principal, itemID string, revision uint64, input domain.ReportArtifactInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.report.prepare", itemID); err != nil {
		return err
	}
	if err := normalizeFileEvidence(&input, "报告"); err != nil {
		return err
	}
	repo, ok := s.Repo.(ReportRevisionRepository)
	if !ok {
		return errors.New("report revision repository unavailable")
	}
	return repo.RegisterReportArtifact(ctx, p.TenantID, itemID, revision, input, p.UserID, time.Now().UTC())
}

// reportPhasePermission 返回推进到目标报告阶段所需的权限码。
// 归档（ARCHIVED）会把项目推向"已完成"，属于独立治理动作；编制/审核/签发属于报告编制。
func reportPhasePermission(phase string) string {
	switch strings.ToUpper(strings.TrimSpace(phase)) {
	case "COMPILING":
		return "project.report.prepare"
	case "REVIEWED":
		return "project.report.review"
	case "ISSUED":
		return "project.report.issue"
	case "ARCHIVED":
		return "project.report.archive"
	default:
		return "project.report.prepare"
	}
}

// RequestReportCorrection 只允许针对已签发或已归档的当前版本提出更正；申请不改变旧报告有效性。
func (s *Service) RequestReportCorrection(ctx context.Context, p platform.Principal, itemID string, input domain.ReportCorrectionRequestInput) (string, error) {
	if err := validateProjectTextLength(input.Reason, 1000, "报告更正原因"); err != nil {
		return "", err
	}
	if err := s.authorizeServiceItem(ctx, p, "project.report.correction.request", itemID); err != nil {
		return "", err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.report.correction.request", itemID, input.ExpectedVersion); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Reason) == "" {
		return "", ValidationError("请填写报告更正原因")
	}
	filter, err := authorizeProjectScope(p, "project.report.correction.request")
	if err != nil {
		return "", err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return "", err
	}
	if item.Status != "现场实施完成" || (item.ReportStatus != "ISSUED" && item.ReportStatus != "ARCHIVED") {
		return "", PreconditionError("仅可对已签发或已归档报告申请更正")
	}
	event := deliveryEvent(p, "", itemID, EventReportCorrectionRequested, map[string]any{"reason": strings.TrimSpace(input.Reason), "old_revision": item.ReportRevision, "requester_user_id": p.UserID})
	if err := s.applyEvent(ctx, event); err != nil {
		return "", err
	}
	return event.ID, nil
}

func (s *Service) DecideReportCorrection(ctx context.Context, p platform.Principal, itemID, requestID string, input domain.ReportCorrectionDecisionInput) error {
	if err := validateProjectTextLength(input.Comment, 1000, "报告更正审批意见"); err != nil {
		return err
	}
	if err := s.authorizeServiceItem(ctx, p, "project.report.correction.approve", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.report.correction.approve", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "APPROVED" && decision != "REJECTED" {
		return ErrValidation
	}
	if strings.TrimSpace(input.Comment) == "" {
		return ValidationError("请填写报告更正审批意见")
	}
	filter, err := authorizeProjectScope(p, "project.report.correction.approve")
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
		if event.ID == requestID && event.ServiceItemID == itemID && event.Type == EventReportCorrectionRequested {
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
	if decision == "APPROVED" && (item.ReportStatus != "ISSUED" && item.ReportStatus != "ARCHIVED") {
		return PreconditionError("报告状态已变化，不能批准更正")
	}
	typ := EventReportCorrectionApproved
	if decision == "REJECTED" {
		typ = EventReportCorrectionRejected
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, typ, map[string]any{"request_id": requestID, "comment": strings.TrimSpace(input.Comment), "reason": payloadText(request.Payload, "reason"), "old_revision": request.Payload["old_revision"], "report_correction_requester_id": request.ActorUserID, "expected_version": input.ExpectedVersion}))
}

// ReturnEquipment 把某台设备从服务项的实施准备清单中归还：清单行保留（保留借出历史），
// 但标记归还时间后不再占用设备，也不再算「不在公司」。设备维护人员与项目经理都可发起归还，
// 因为设备可能由现场提前寄回。
