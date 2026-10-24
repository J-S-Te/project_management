package bootstrap

import (
	"context"
	"github.com/J-S-Te/license-core/consumer"
	"log/slog"
)

func OpenCommercialLicense(ctx context.Context, logger *slog.Logger) (*consumer.Gate, error) {
	gate, err := consumer.FromEnvironment("project_management")
	if err != nil {
		return nil, err
	}
	go func() {
		_ = gate.Run(ctx, func(error) {
			logger.Warn("commercial license synchronization unavailable; local expiry remains authoritative")
		})
	}()
	return gate, nil
}
