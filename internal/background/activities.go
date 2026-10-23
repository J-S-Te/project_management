package background

import (
	"context"
	"fmt"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/oklog/ulid/v2"
)

type Activities struct {
	Service  *application.Service
	TenantID string
}

func (a *Activities) RunOne(ctx context.Context, input TickInput) (TickResult, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return TickResult{}, err
	}
	if a == nil || !validTenant(a.TenantID) || input.TenantID != a.TenantID {
		return TickResult{}, fmt.Errorf("background tenant does not match deployment")
	}
	if a.Service == nil || a.Service.CheckBusinessLicense == nil {
		return TickResult{}, fmt.Errorf("background business license gate is required")
	}
	if err := ctx.Err(); err != nil {
		return TickResult{}, err
	}
	// Copy the service so each concurrent Activity tracks only its own license
	// refusals without replacing the callback shared by API/runtime callers.
	service := *a.Service
	gate := service.CheckBusinessLicense
	paused := false
	service.CheckBusinessLicense = func(ctx context.Context) error {
		err := gate(ctx)
		if err != nil {
			paused = true
		}
		return err
	}
	if err := service.CheckBusinessLicense(ctx); err != nil {
		return TickResult{PausedReason: "LICENSE"}, nil
	}
	if service.Repo == nil {
		return TickResult{}, fmt.Errorf("background repository is required")
	}
	if input.Kind == TaskNotifications && service.Notifications == nil {
		return TickResult{}, fmt.Errorf("background notification publisher is required")
	}
	timeout := input.ActivityTimeout
	if input.Kind == TaskSLA {
		timeout = input.SLAActivityTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Wall time and randomness are intentionally restricted to Activities.
	now := time.Now().UTC()
	owner := ulid.Make().String()
	var processed int
	switch input.Kind {
	case TaskSLA:
		processed, err = service.ScanSlaNotifications(ctx, a.TenantID, now)
	case TaskEffects:
		processed, err = service.DispatchDeliveryEffects(ctx, a.TenantID, owner, 1, now)
	case TaskNotifications:
		processed, err = service.DispatchNotificationOutbox(ctx, a.TenantID, owner, 1, now)
	}
	result := TickResult{Processed: processed}
	if paused {
		result.PausedReason = "LICENSE"
		return result, nil
	}
	return result, err
}
