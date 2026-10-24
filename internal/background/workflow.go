package background

import (
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// RunTick only orchestrates a bounded pass. Business retries remain in the
// outbox; a Temporal retry must never spend another business attempt.
func RunTick(ctx workflow.Context, input TickInput) (TickResult, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return TickResult{}, temporal.NewNonRetryableApplicationError("invalid background tick", "INVALID_TICK", err)
	}
	name, _ := activityName(input.Kind)
	timeout := input.ActivityTimeout
	if input.Kind == TaskSLA {
		timeout = input.SLAActivityTimeout
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    timeout,
		ScheduleToCloseTimeout: timeout,
		RetryPolicy:            &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	started := workflow.Now(ctx)
	result := TickResult{}
	for i := 0; i < input.Limit && workflow.Now(ctx).Sub(started) < input.Budget; i++ {
		one := input
		one.Limit = 1
		var next TickResult
		if err := workflow.ExecuteActivity(ctx, name, one).Get(ctx, &next); err != nil {
			return result, err
		}
		result.Processed += next.Processed
		if next.PausedReason != "" {
			result.PausedReason = next.PausedReason
			return result, nil
		}
		// SLA owns a full current-time scan. Zero means no successful dispatch;
		// this also preserves a row retained for a later business retry as zero.
		if input.Kind == TaskSLA || next.Processed == 0 {
			break
		}
	}
	return result, nil
}
