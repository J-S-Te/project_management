package workflows

import (
	"context"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestConfirmServiceItemsWorkflow(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.RegisterActivityWithOptions(func(context.Context, ConfirmServiceItemsInput) (ConfirmServiceItemsResult, error) {
		return ConfirmServiceItemsResult{Items: []domain.ServiceItem{{ID: "SI-1", Status: "待分配"}}}, nil
	}, activity.RegisterOptions{Name: "ConfirmServiceItemsActivity"})
	environment.ExecuteWorkflow(ConfirmServiceItemsWorkflow, ConfirmServiceItemsInput{TenantID: "tenant-1", IDs: []string{"SI-1"}, ActorUserID: "user-1"})
	require.NoError(t, environment.GetWorkflowError())
	var result ConfirmServiceItemsResult
	require.NoError(t, environment.GetWorkflowResult(&result))
	require.Len(t, result.Items, 1)
	require.Equal(t, "待分配", result.Items[0].Status)
}
