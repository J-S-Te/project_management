package application

import (
	"context"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// ManualProjectManagerReportMode 区分经理维护的阶段和真实报告审核事实。
const ManualProjectManagerReportMode = "PROJECT_MANAGER_MANUAL"

// AdvanceManualReportStatus 由当前指派项目经理手动推进主报告阶段。
// 参数：ctx 为请求，p 为可信主体，itemID 为服务项，input 为下一阶段及必填当前版本。
// 返回值：权限、指派、阶段或版本不符时拒绝，成功返回 nil 并保留手动操作事件。
func (s *Service) AdvanceManualReportStatus(
	ctx context.Context, p platform.Principal, itemID string, input domain.ReportStatusInput,
) error {
	filter, err := authorizeProjectScope(p, "project.report.prepare")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.TenantID != p.TenantID {
		return ErrNotFound
	}
	if p.UserID == "" || item.ProjectManagerID != p.UserID {
		return ErrForbidden
	}
	phase := strings.ToUpper(strings.TrimSpace(input.Phase))
	if input.ExpectedVersion == 0 || domain.ReportPhaseRank(phase) <= 0 {
		return ValidationError("请选择有效的报告阶段并携带当前服务项版本")
	}
	if item.Version != input.ExpectedVersion {
		return ErrConflict
	}
	if item.Status != "现场实施完成" || !domain.CanAdvanceReportPhase(item.ReportStatus, phase) {
		return PreconditionError("请先完成现场实施，再按报告阶段顺序逐步流转")
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventReportStatusUpdated, map[string]any{
		"phase": phase, "expected_version": input.ExpectedVersion,
		"transition_mode": ManualProjectManagerReportMode,
	}))
}
