package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type approvalTaskRepository interface {
	ListApprovalTasks(context.Context, platform.ScopeFilter, domain.ApprovalQuery) ([]domain.ApprovalTask, int64, error)
}

type approvalRequestRepository interface {
	GetApprovalRequest(context.Context, platform.ScopeFilter, string, string, string) (domain.DeliveryEvent, error)
}

// ListApprovalTasks 查询当前账号待审批、本人申请或精确申请的状态。
// 参数：ctx 为请求，p 为可信主体，q 为分页、视图及可选申请编号。
// 返回值：受权分页结果；未授权、输入非法或仓储不可用时返回错误。
func (s *Service) ListApprovalTasks(ctx context.Context, p platform.Principal, q domain.ApprovalQuery) (domain.ApprovalPage, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil || strings.TrimSpace(p.UserID) == "" {
		return domain.ApprovalPage{}, ErrForbidden
	}
	if q.Page < 1 || q.Page > 100000 || q.PageSize < 1 || q.PageSize > 100 ||
		(q.View != "assigned" && q.View != "submitted") || len(q.RequestID) > 64 {
		return domain.ApprovalPage{}, ValidationError("审批待办分页、视图或申请编号无效")
	}
	q.UserID = p.UserID
	q.CanRollback, q.CanCorrection = p.Has("project.rollback.approve"), p.Has("project.report.correction.approve")
	repo, ok := s.Repo.(approvalTaskRepository)
	if !ok {
		return domain.ApprovalPage{}, fmt.Errorf("approval task repository unavailable")
	}
	rows, total, err := repo.ListApprovalTasks(ctx, filter, q)
	if err != nil {
		return domain.ApprovalPage{}, err
	}
	for i := range rows {
		rows[i].Status = approvalDecisionStatus(rows[i].DecisionType)
		rows[i].CanDecide = rows[i].Status == "PENDING" && canDecideApprovalTask(p, rows[i])
	}
	return domain.ApprovalPage{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize}, nil
}

// canDecideApprovalTask 对列表和详情再次应用与写接口相同的权限和异人规则。
// 参数：p 为可信主体，task 为受权查询取得的原申请及当前服务项信息。
// 返回值：当前用户可以对该类型申请提交审批时返回 true。
func canDecideApprovalTask(p platform.Principal, task domain.ApprovalTask) bool {
	if task.Type == "REPORT_CORRECTION" {
		return p.UserID != task.RequesterUserID && p.Has("project.report.correction.approve")
	}
	return p.UserID != task.RequesterUserID && p.Has("project.rollback.approve")
}

// findApprovalRequest 回源单个原申请，兼容既有单测仓储而不依赖生产事件快照长度。
// 参数：ctx/filter 为可信范围，item 为目标服务项，requestID/type 为申请身份。
// 返回值：持久原申请；不存在或读取失败时返回错误。
func (s *Service) findApprovalRequest(ctx context.Context, filter platform.ScopeFilter, item domain.ServiceItem, requestID, eventType string) (domain.DeliveryEvent, error) {
	if repo, ok := s.Repo.(approvalRequestRepository); ok {
		return repo.GetApprovalRequest(ctx, filter, item.ID, requestID, eventType)
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return domain.DeliveryEvent{}, err
	}
	events, err := repo.ListDeliveryEvents(ctx, filter, item.ProjectID)
	if err != nil {
		return domain.DeliveryEvent{}, err
	}
	for _, event := range events {
		if event.ID == requestID && event.ServiceItemID == item.ID && event.Type == eventType {
			return event, nil
		}
	}
	return domain.DeliveryEvent{}, ErrNotFound
}
