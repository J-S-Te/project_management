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
	SetRuleEnabled(context.Context, string, int64, bool, string) (domain.Rule, error)
	Dashboard(context.Context, platform.ScopeFilter) (domain.Dashboard, error)
}

type WorkflowExecutor interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, any, ...any) (client.WorkflowRun, error)
}

type Service struct {
	Repo      Repository
	Temporal  WorkflowExecutor
	TaskQueue string
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
func (s *Service) ListRules(ctx context.Context, p platform.Principal, kind string) ([]domain.Rule, error) {
	if err := requireApplicationAuthorization(p, "project.read"); err != nil {
		return nil, err
	}
	return s.Repo.ListRules(ctx, p.TenantID, kind)
}

func (s *Service) CreateProject(ctx context.Context, p platform.Principal, input domain.Project) (domain.Project, error) {
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
	if err := s.Repo.CreateProject(ctx, input); err != nil {
		return input, err
	}
	return input, nil
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
	run, err := s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: workflowID, TaskQueue: s.TaskQueue}, workflows.ConfirmServiceItemsWorkflowName, input)
	if err != nil {
		return nil, err
	}
	var result workflows.ConfirmServiceItemsResult
	if err := run.Get(ctx, &result); err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (s *Service) CreateRule(ctx context.Context, p platform.Principal, input domain.Rule) (domain.Rule, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return input, err
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Scope) == "" {
		return input, ErrValidation
	}
	input.TenantID = p.TenantID
	if input.Kind == "" {
		input.Kind = "split-rules"
	}
	input.Updated = time.Now().Format("2006-01-02 15:04")
	return s.Repo.CreateRule(ctx, input)
}
func (s *Service) SetRuleEnabled(ctx context.Context, p platform.Principal, id int64, enabled bool) (domain.Rule, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return domain.Rule{}, err
	}
	return s.Repo.SetRuleEnabled(ctx, p.TenantID, id, enabled, p.UserID)
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
