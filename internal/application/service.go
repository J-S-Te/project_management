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
)

type Repository interface {
	ListProjects(context.Context, string, string, string) ([]domain.Project, error)
	GetProject(context.Context, string, string) (domain.Project, error)
	CreateProject(context.Context, domain.Project) error
	ListServiceItems(context.Context, string, string) ([]domain.ServiceItem, error)
	ConfirmServiceItems(context.Context, string, []string, string) ([]domain.ServiceItem, error)
	ListRules(context.Context, string, string) ([]domain.Rule, error)
	CreateRule(context.Context, domain.Rule) (domain.Rule, error)
	SetRuleEnabled(context.Context, string, int64, bool, string) (domain.Rule, error)
	Dashboard(context.Context, string) (domain.Dashboard, error)
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
	return s.Repo.ListProjects(ctx, p.TenantID, q, status)
}
func (s *Service) GetProject(ctx context.Context, p platform.Principal, id string) (domain.Project, error) {
	return s.Repo.GetProject(ctx, p.TenantID, id)
}
func (s *Service) Dashboard(ctx context.Context, p platform.Principal) (domain.Dashboard, error) {
	return s.Repo.Dashboard(ctx, p.TenantID)
}
func (s *Service) ListServiceItems(ctx context.Context, p platform.Principal, projectID string) ([]domain.ServiceItem, error) {
	return s.Repo.ListServiceItems(ctx, p.TenantID, projectID)
}
func (s *Service) ListRules(ctx context.Context, p platform.Principal, kind string) ([]domain.Rule, error) {
	return s.Repo.ListRules(ctx, p.TenantID, kind)
}

func (s *Service) CreateProject(ctx context.Context, p platform.Principal, input domain.Project) (domain.Project, error) {
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Customer) == "" || strings.TrimSpace(input.Contract) == "" {
		return input, ErrValidation
	}
	now := time.Now().UTC()
	input.ID = "PJ-" + now.Format("2006") + "-" + strings.ToUpper(ulid.Make().String()[20:])
	input.TenantID = p.TenantID
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
	if len(ids) == 0 {
		return nil, ErrValidation
	}
	if s.Temporal == nil {
		return nil, errors.New("temporal client unavailable")
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
	return s.Repo.SetRuleEnabled(ctx, p.TenantID, id, enabled, p.UserID)
}
