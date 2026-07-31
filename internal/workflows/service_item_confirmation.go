package workflows

import (
	"context"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const ConfirmServiceItemsWorkflowName = "project.confirm-service-items.v1"

type ConfirmServiceItemsInput struct {
	TenantID    string   `json:"tenant_id"`
	IDs         []string `json:"ids"`
	ActorUserID string   `json:"actor_user_id"`
}
type ConfirmServiceItemsResult struct {
	Items []domain.ServiceItem `json:"items"`
}

func ConfirmServiceItemsWorkflow(ctx workflow.Context, input ConfirmServiceItemsInput) (ConfirmServiceItemsResult, error) {
	options := workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 10 * time.Second, MaximumAttempts: 5}}
	ctx = workflow.WithActivityOptions(ctx, options)
	var result ConfirmServiceItemsResult
	err := workflow.ExecuteActivity(ctx, "ConfirmServiceItemsActivity", input).Get(ctx, &result)
	return result, err
}

type ServiceItemStore interface {
	ConfirmServiceItems(context.Context, string, []string, string) ([]domain.ServiceItem, error)
}
type Activities struct{ Store ServiceItemStore }

func (a *Activities) ConfirmServiceItemsActivity(ctx context.Context, input ConfirmServiceItemsInput) (ConfirmServiceItemsResult, error) {
	items, err := a.Store.ConfirmServiceItems(ctx, input.TenantID, input.IDs, input.ActorUserID)
	return ConfirmServiceItemsResult{Items: items}, err
}
