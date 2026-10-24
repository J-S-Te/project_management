// This compatibility binary shares the same configured background runtime.
package main

import (
	"context"
	"github.com/j-s-te/project-management/internal/background"
	"github.com/j-s-te/project-management/internal/config"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("background configuration failed", "error_code", "INVALID_CONFIGURATION")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err = background.Run(ctx, logger, cfg); err != nil {
		logger.Error("background stopped", "error_code", "RUNTIME_FAILED")
		os.Exit(1)
	}
}
