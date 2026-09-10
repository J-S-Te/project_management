package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/internal/workflows"
	"github.com/oklog/ulid/v2"
	"go.temporal.io/sdk/client"
)

var (
	ErrNotFound   = errors.New("resource not found")
	ErrValidation = errors.New("validation failed")
	ErrConflict   = errors.New("resource state conflict")
	ErrForbidden  = errors.New("operation is outside the authorized project scope")
	// ErrServiceTimeout 表示同步等待后端工作流在约定时间内没有完成。
	// 前端应提示用户稍后重试，而不是让网关吞掉请求并返回 504。
	ErrServiceTimeout = errors.New("service processing timeout")
	// ErrPersonnelUnavailable 表示项目子系统尚未开通基础平台人员目录集成。
	ErrPersonnelUnavailable = errors.New("platform personnel directory is unavailable")
)

type Repository interface {
	ListProjects(context.Context, platform.ScopeFilter, string, string) ([]domain.Project, error)
	GetProject(context.Context, platform.ScopeFilter, string) (domain.Project, error)
	CreateProject(context.Context, domain.Project) error
	ListServiceItems(context.Context, platform.ScopeFilter, string) ([]domain.ServiceItem, error)
	GetServiceItem(context.Context, platform.ScopeFilter, string) (domain.ServiceItem, error)
	ConfirmServiceItems(context.Context, string, []string, string) ([]domain.ServiceItem, error)
	ListRules(context.Context, string, string) ([]domain.Rule, error)
	CreateRule(context.Context, domain.Rule) (domain.Rule, error)
	UpdateRule(context.Context, string, string, int64, domain.Rule) (domain.Rule, error)
	SetRuleEnabled(context.Context, string, string, int64, bool, string) (domain.Rule, error)
	Dashboard(context.Context, platform.ScopeFilter) (domain.Dashboard, error)
}

// ProjectServiceCreator keeps manual project creation atomic with its initial
// service-item decomposition. Repositories that do not implement it remain
// compatible with the legacy project-only API.
type ProjectServiceCreator interface {
	CreateProjectWithServiceItems(context.Context, domain.Project, []domain.ServiceItem) error
}

type WorkflowExecutor interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, any, ...any) (client.WorkflowRun, error)
}

type Service struct {
	Repo      Repository
	Temporal  WorkflowExecutor
	TaskQueue string
	// Personnel 是基础平台负责人目录；未开通该集成时为 nil，读取人员会返回
	// ErrPersonnelUnavailable，不影响其余项目功能。
	Personnel platform.OwnerDirectory
}

func (s *Service) ListProjects(ctx context.Context, p platform.Principal, q, status string) ([]domain.Project, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	return s.Repo.ListProjects(ctx, filter, q, status)
}
func (s *Service) GetProject(ctx context.Context, p platform.Principal, id string) (domain.Project, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return domain.Project{}, err
	}
	return s.Repo.GetProject(ctx, filter, id)
}
func (s *Service) Dashboard(ctx context.Context, p platform.Principal) (domain.Dashboard, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return domain.Dashboard{}, err
	}
	return s.Repo.Dashboard(ctx, filter)
}
func (s *Service) ListServiceItems(ctx context.Context, p platform.Principal, projectID string) ([]domain.ServiceItem, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	return s.Repo.ListServiceItems(ctx, filter, projectID)
}

// ListPersonnel 从基础平台负责人目录读取可选人员，供服务项操作台选择团队负责人、
// 项目经理和工程师。只有具备分配权限的角色能读取，避免把平台人员清单暴露给纯查看角色。
func (s *Service) ListPersonnel(ctx context.Context, p platform.Principal, keyword, userID string, page, pageSize int) (platform.OwnerDirectoryPage, error) {
	if !p.Has("project.team.assign") && !p.Has("project.execution.assign") {
		return platform.OwnerDirectoryPage{}, ErrForbidden
	}
	if s.Personnel == nil {
		return platform.OwnerDirectoryPage{}, ErrPersonnelUnavailable
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 50
	}
	result, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{Keyword: strings.TrimSpace(keyword), UserID: strings.TrimSpace(userID), Page: page, PageSize: pageSize})
	if err != nil {
		// 目录不可用时对上层统一暴露“未配置/不可用”，不把平台内部错误细节透给浏览器。
		return platform.OwnerDirectoryPage{}, fmt.Errorf("%w: %v", ErrPersonnelUnavailable, err)
	}
	return result, nil
}
func (s *Service) ListRules(ctx context.Context, p platform.Principal, kind string) ([]domain.Rule, error) {
	if err := requireApplicationAuthorization(p, "project.read"); err != nil {
		return nil, err
	}
	return s.Repo.ListRules(ctx, p.TenantID, kind)
}

func (s *Service) CreateProject(ctx context.Context, p platform.Principal, input domain.Project) (domain.Project, error) {
	return s.CreateProjectWithServiceItems(ctx, p, input, nil)
}

func (s *Service) CreateProjectWithServiceItems(ctx context.Context, p platform.Principal, input domain.Project, requested []domain.ContractService) (domain.Project, error) {
	filter, err := authorizeProjectScope(p, "project.create")
	if err != nil {
		return input, err
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Customer) == "" || strings.TrimSpace(input.Contract) == "" {
		return input, ErrValidation
	}
	now := time.Now().UTC()
	input.ID = "PJ-" + now.Format("2006") + "-" + strings.ToUpper(ulid.Make().String()[20:])
	input.TenantID = p.TenantID
	input.OwnerIdentityID = p.IdentityID
	if input.OwnerIdentityID == "" {
		input.OwnerIdentityID = p.UserID
	}
	if !filter.AllowAll {
		if input.OwnerOrgID != "" && !contains(filter.OrganizationIDs, input.OwnerOrgID) {
			return input, ErrForbidden
		}
		if input.OwnerOrgID == "" && len(filter.OrganizationIDs) == 1 {
			input.OwnerOrgID = filter.OrganizationIDs[0]
		}
		if !filter.AllowSelf && input.OwnerOrgID == "" {
			return input, ErrForbidden
		}
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Customer = strings.TrimSpace(input.Customer)
	input.Contract = strings.TrimSpace(input.Contract)
	if input.Status == "" {
		input.Status = "待拆解确认"
	}
	if input.Health == "" {
		input.Health = "待确认"
	}
	if input.Team == "" {
		input.Team = "未分配"
	}
	if input.Manager == "" {
		input.Manager = "—"
	}
	input.CreatedAt, input.UpdatedAt = now, now
	if len(requested) == 0 {
		if err := s.Repo.CreateProject(ctx, input); err != nil {
			return input, err
		}
		return input, nil
	}
	items := make([]domain.ServiceItem, 0, len(requested))
	for index, source := range requested {
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if strings.TrimSpace(source.Site) == "" || mode != "STANDARD" && mode != "PENETRATION" {
			return input, ErrValidation
		}
		items = append(items, domain.ServiceItem{
			TenantID: p.TenantID, ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(input.ID, "PJ-"), index+1),
			ProjectID: input.ID, SourceServiceID: firstNonEmpty(source.SourceID, fmt.Sprintf("MANUAL-%03d", index+1)),
			Batch: strings.TrimSpace(source.Batch), Site: strings.TrimSpace(source.Site), Category: strings.TrimSpace(source.Category),
			Requirement: strings.TrimSpace(source.Requirement), System: strings.TrimSpace(source.System), SystemLevel: strings.TrimSpace(source.SystemLevel), Special: yesNo(mode == "PENETRATION"), TestMode: mode,
			Status: "待确认", ConflictStatus: "UNCHECKED",
		})
	}
	input.Services = len(items)
	if creator, ok := s.Repo.(ProjectServiceCreator); ok {
		if err := creator.CreateProjectWithServiceItems(ctx, input, items); err != nil {
			return input, err
		}
		return input, nil
	}
	return input, errors.New("project repository does not support atomic service-item creation")
}

func (s *Service) ConfirmServiceItems(ctx context.Context, p platform.Principal, ids []string) ([]domain.ServiceItem, error) {
	filter, err := authorizeProjectScope(p, "service_item.confirm")
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrValidation
	}
	if s.Temporal == nil {
		return nil, errors.New("temporal client unavailable")
	}
	for _, id := range ids {
		if _, err := s.Repo.GetServiceItem(ctx, filter, id); err != nil {
			return nil, err
		}
	}
	input := workflows.ConfirmServiceItemsInput{TenantID: p.TenantID, IDs: ids, ActorUserID: p.UserID}
	workflowID := fmt.Sprintf("project-service-items-confirm:%s:%s", p.TenantID, ulid.Make().String())
	// 为工作流设置明确的执行超时：确认拆解会被 API 同步等待，若 Worker 未就绪或活动持续失败，
	// 该超时会终止工作流，避免它在后台无限期运行（进一步从根上杜绝网关 504）。
	run, err := s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: workflowID, TaskQueue: s.TaskQueue, WorkflowExecutionTimeout: 2 * time.Minute}, workflows.ConfirmServiceItemsWorkflowName, input)
	if err != nil {
		return nil, err
	}
	// 确认拆解本应是秒级的事务性写入，却在请求路径上同步等待 Temporal 工作流。
	// 若工作流因 Worker 未就绪、活动重试或数据库锁等待而长时间不返回，会被网关默认的
	// proxy_read_timeout(60s) 直接打成 504。这里给等待加一个硬超时并返回明确错误，
	// 让用户看到“处理超时请重试”而不是网关错误页；工作流随后由自身的执行超时收敛。
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var result workflows.ConfirmServiceItemsResult
	if err := run.Get(waitCtx, &result); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrServiceTimeout
		}
		return nil, err
	}
	return result.Items, nil
}

func (s *Service) CreateRule(ctx context.Context, p platform.Principal, input domain.Rule) (domain.Rule, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return input, err
	}
	input.Kind = strings.TrimSpace(input.Kind)
	if input.Kind == "" {
		input.Kind = "split-rules"
	}
	if strings.TrimSpace(input.Name) == "" {
		return input, ErrValidation
	}
	switch input.Kind {
	case "split-rules":
		if strings.TrimSpace(input.Scope) == "" {
			return input, ErrValidation
		}
	case "warning-rules":
		if strings.TrimSpace(input.CheckType) == "" {
			return input, ErrValidation
		}
	case "automations":
		if strings.TrimSpace(input.Trigger) == "" || strings.TrimSpace(input.Target) == "" {
			return input, ErrValidation
		}
	case "permissions":
		if strings.TrimSpace(input.RoleCode) == "" || strings.TrimSpace(input.FieldName) == "" {
			return input, ErrValidation
		}
	case "sla":
		if strings.TrimSpace(input.Status) == "" || input.DeadlineHours <= 0 {
			return input, ErrValidation
		}
	default:
		return input, ErrValidation
	}
	input.TenantID = p.TenantID
	input.UpdatedBy = p.UserID
	input.Updated = time.Now().Format("2006-01-02 15:04")
	if input.AccessLevel == "" {
		input.AccessLevel = "view"
	}
	return s.Repo.CreateRule(ctx, input)
}

// UpdateRule 整行更新配置。载荷覆盖该 kind 的专属字段与启停开关。
func (s *Service) UpdateRule(ctx context.Context, p platform.Principal, id int64, input domain.Rule) (domain.Rule, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return domain.Rule{}, err
	}
	if strings.TrimSpace(input.Name) == "" {
		return domain.Rule{}, ErrValidation
	}
	input.TenantID = p.TenantID
	input.UpdatedBy = p.UserID
	input.Updated = time.Now().Format("2006-01-02 15:04")
	return s.Repo.UpdateRule(ctx, p.TenantID, strings.TrimSpace(input.Kind), id, input)
}

func (s *Service) SetRuleEnabled(ctx context.Context, p platform.Principal, kind string, id int64, enabled bool) (domain.Rule, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return domain.Rule{}, err
	}
	return s.Repo.SetRuleEnabled(ctx, p.TenantID, strings.TrimSpace(kind), id, enabled, p.UserID)
}

func authorizeProjectScope(p platform.Principal, permission string) (platform.ScopeFilter, error) {
	if !p.Has(permission) {
		return platform.ScopeFilter{}, ErrForbidden
	}
	filter, err := p.ProjectScopeFilter()
	if err != nil {
		return platform.ScopeFilter{}, ErrForbidden
	}
	return filter, nil
}

func requireApplicationAuthorization(p platform.Principal, permission string) error {
	if !p.Has(permission) || !p.HasFullDataScope() {
		return ErrForbidden
	}
	return nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
