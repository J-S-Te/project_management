package main

import (
	"context"
	"github.com/j-s-te/project-management/internal/bootstrap"
	"github.com/j-s-te/project-management/internal/config"
	"github.com/j-s-te/project-management/internal/temporalworker"
	"go.temporal.io/sdk/worker"
	"log/slog"
	"os"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	metrics := temporalworker.NewMetricsRegistry()
	if err := temporalworker.StartMetricsServer(ctx, cfg.TemporalMetricsAddress, metrics, logger); err != nil {
		logger.Error("start Temporal metrics server", "error", err)
		os.Exit(1)
	}
	db, err := bootstrap.OpenDatabase(ctx, cfg.MySQLDSN)
	if err != nil {
		logger.Error("database failed", "error", err)
		os.Exit(1)
	}
	defer bootstrap.CloseDatabase(db)
	temporalClient, err := bootstrap.OpenTemporal(ctx, cfg, metrics)
	if err != nil {
		logger.Error("temporal failed", "error", err)
		os.Exit(1)
	}
	defer temporalClient.Close()
	versioning := temporalworker.VersioningConfig{
		Enabled: cfg.TemporalWorkerVersioning, DeploymentName: cfg.TemporalWorkerDeploymentName,
		BuildID: cfg.TemporalWorkerBuildID, Policy: cfg.TemporalWorkerVersioningPolicy,
	}
	workerOptions, err := temporalworker.WorkerOptions(versioning)
	if err != nil {
		logger.Error("configure Temporal worker versioning", "error", err)
		os.Exit(1)
	}
	w := worker.New(temporalClient, cfg.TemporalTaskQueue, workerOptions)
	logger.Info("project workflow worker started", "task_queue", cfg.TemporalTaskQueue, "deployment", cfg.TemporalWorkerDeploymentName, "build_id", cfg.TemporalWorkerBuildID, "versioning", cfg.TemporalWorkerVersioning)
	// AUD-2026-027：本 worker 当前没有注册任何 workflow/activity（空跑 poller，属无害待机），
	// 因此不再启动即抢占 Deployment 的 Current 版本——空的 Current 版本会让未来/外部工作流
	// 入队后无人领取。恢复使用前必须先补齐工作流注册，版本收敛（promote/ramp）一律通过
	// 显式运维工具 project-worker-rollout 执行，见 internal/temporalworker/rollout.go。
	if err := w.Run(worker.InterruptCh()); err != nil {
		logger.Error("worker failed", "error", err)
		os.Exit(1)
	}
}
