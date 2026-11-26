package application

import (
	"context"
	"fmt"
	"net/url"
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
	if q.TaskType != "" && (q.RequestID == "" || !isBusinessApprovalType(q.TaskType)) {
		return domain.ApprovalPage{}, ValidationError("办理事项类型或编号无效")
	}
	q.UserID, q.IdentityID = p.UserID, p.IdentityID
	q.CanRollback, q.CanCorrection = p.Has("project.rollback.approve"), p.Has("project.report.correction.approve")
	q.CanSpecial = p.Has("project.special_method.review")
	q.CanDeviation = p.Has("project.deviation.review")
	q.NativeAvailable = s.NativeEnabled && strings.TrimSpace(p.IdentityID) != ""
	q.CanNative = q.NativeAvailable && mayApproveNativeScope(p)
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
		rows[i].TargetURL = businessApprovalTarget(rows[i])
	}
	if err := s.maskApprovalTaskSites(ctx, p, rows); err != nil {
		return domain.ApprovalPage{}, err
	}
	s.fillApprovalApplicantNames(ctx, p, rows)
	return domain.ApprovalPage{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize}, nil
}

// ReadApprovalServiceItem 精确读取直达页面引用的服务项，仍使用普通服务项的授权与字段权限。
// 参数：ctx/p 为请求身份，itemID 是服务项编号，projectID 可约束同一项目。
// 返回值：单条授权服务项；项目不匹配与不可见使用相同的不存在语义。
func (s *Service) ReadApprovalServiceItem(
	ctx context.Context, p platform.Principal, itemID, projectID string,
) (domain.ServiceItem, error) {
	if !nativeReferencePattern.MatchString(itemID) {
		return domain.ServiceItem{}, ValidationError("服务项编号格式无效")
	}
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return domain.ServiceItem{}, err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return domain.ServiceItem{}, err
	}
	if projectID != "" && item.ProjectID != projectID {
		return domain.ServiceItem{}, ErrNotFound
	}
	_, masked, err := s.applyFieldPermissions(ctx, p, nil, []domain.ServiceItem{item})
	if err != nil {
		return domain.ServiceItem{}, err
	}
	return masked[0], nil
}

// isBusinessApprovalType 判断是否为跳转到原业务页面办理的新事项类型。
// 参数：kind 为精确查找时的事项类型。
// 返回值：需要业务页承接的已知类型返回 true。
func isBusinessApprovalType(kind string) bool {
	switch kind {
	case domain.ApprovalSpecialMethod, domain.ApprovalDeviation, domain.ApprovalNativeScope:
		return true
	default:
		return false
	}
}

// businessApprovalTarget 根据真实待办坐标构造受控的站内办理路径。
// 参数：task 为授权查询返回的业务事项。
// 返回值：对应业务页路径；内置申请沿用原审批详情路由。
func businessApprovalTarget(task domain.ApprovalTask) string {
	query := url.Values{}
	var section string
	switch task.Type {
	case domain.ApprovalSpecialMethod:
		section = "methods"
		query.Set("service_item_id", task.ServiceItemID)
		if task.Status != "PENDING" {
			query.Set("review_id", task.ID)
		}
	case domain.ApprovalDeviation:
		section = "exceptions"
		query.Set("service_item_id", task.ServiceItemID)
		query.Set("deviation_id", task.ID)
	case domain.ApprovalNativeScope:
		section = "project_detail"
		query.Set("project", task.ProjectID)
		query.Set("scope_change_id", task.ID)
	default:
		return ""
	}
	return "/project_management/" + section + "?" + query.Encode()
}

// maskApprovalTaskSites 在待办中沿用服务项的现场地点字段可见规则。
// 参数：ctx/p 为读取主体，rows 为授权范围内的审批结果。
// 返回值：规则读取错误；成功时就地掩码。
func (s *Service) maskApprovalTaskSites(ctx context.Context, p platform.Principal, rows []domain.ApprovalTask) error {
	for _, row := range rows {
		if row.Site == "" {
			continue
		}
		_, hidden, err := s.hiddenFieldsFor(ctx, p)
		if err != nil {
			return err
		}
		if _, ok := hidden["site"]; ok {
			for i := range rows {
				if rows[i].Site != "" {
					rows[i].Site = maskedFieldValue
				}
			}
		}
		break
	}
	return nil
}

// canDecideApprovalTask 对列表和详情再次应用与写接口相同的权限和异人规则。
// 参数：p 为可信主体，task 为受权查询取得的原申请及当前服务项信息。
// 返回值：当前用户可以对该类型申请提交审批时返回 true。
func canDecideApprovalTask(p platform.Principal, task domain.ApprovalTask) bool {
	switch task.Type {
	case domain.ApprovalSpecialMethod:
		return p.Has("project.special_method.review")
	case domain.ApprovalDeviation:
		return p.Has("project.deviation.review")
	case domain.ApprovalNativeScope:
		return p.IdentityID != task.RequesterUserID && mayApproveNativeScope(p)
	case "REPORT_CORRECTION":
		return p.UserID != task.RequesterUserID && p.Has("project.report.correction.approve")
	case "FIELD_ROLLBACK", "REPORT_REWORK":
		return p.UserID != task.RequesterUserID && p.Has("project.rollback.approve")
	default:
		return false
	}
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
