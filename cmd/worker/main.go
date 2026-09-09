package main

import (
	"context"
	"github.com/j-s-te/project-management/internal/bootstrap"
	"github.com/j-s-te/project-management/internal/config"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/temporalworker"
	"github.com/j-s-te/project-management/internal/workflows"
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
	workflows.Register(w, &workflows.Activities{Store: store.NewRepository(db)})
	logger.Info("project workflow worker started", "task_queue", cfg.TemporalTaskQueue, "deployment", cfg.TemporalWorkerDeploymentName, "build_id", cfg.TemporalWorkerBuildID, "versioning", cfg.TemporalWorkerVersioning)
	// 版本路由开启时 Worker 只消费自身版本队列；Deployment 的 Current 版本为空会让新工作流
	// 以 UNVERSIONED 入队且无人领取，因此启动时主动收敛，不再依赖人工 PROMOTE。
	temporalworker.EnsureCurrentVersionOnStartup(ctx, temporalClient, logger, versioning)
	if err := w.Run(worker.InterruptCh()); err != nil {
		logger.Error("worker failed", "error", err)
		os.Exit(1)
	}
}
