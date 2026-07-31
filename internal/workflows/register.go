package workflows

import (
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func Register(w worker.Worker, activities *Activities) {
	w.RegisterWorkflowWithOptions(ConfirmServiceItemsWorkflow, workflow.RegisterOptions{Name: ConfirmServiceItemsWorkflowName})
	w.RegisterActivity(activities)
}
