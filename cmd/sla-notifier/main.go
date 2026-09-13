// sla-notifier 周期性扫描 SLA 超期/临近项并投递站内提醒。
//
// 平台此前没有任何周期任务：SLA 只在有人打开页面查询时才可见，超期不会主动通知任何人。
// 本命令把扫描与提醒从请求路径里独立出来，按 SLA_SCAN_INTERVAL 周期执行，
// 目标租户由 SLA_SCAN_TENANTS 显式给出（系统侧任务不代替用户授权，必须限定范围）。
// 未配置通知集成或未给出租户时，它只记录日志并空转，不会报错退出。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/bootstrap"
	"github.com/j-s-te/project-management/internal/config"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/platform"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := bootstrap.OpenDatabase(ctx, cfg.MySQLDSN)
	if err != nil {
		logger.Error("database failed", "error", err)
		os.Exit(1)
	}
	defer bootstrap.CloseDatabase(db)

	notifications := platform.NewNotificationPublisher(cfg.PlatformBaseURL, cfg.PlatformNotificationURL, cfg.PlatformNotificationClientID, cfg.PlatformNotificationSecret, cfg.PlatformNotificationScope)
	if notifications == nil {
		logger.Warn("platform notification integration disabled; sla notifications will not be delivered")
	}
	service := &application.Service{Repo: store.NewRepository(db), Notifications: notifications, Logger: logger}
	if len(cfg.SlaScanTenants) == 0 {
		logger.Warn("SLA_SCAN_TENANTS is empty; nothing to scan")
	} else {
		logger.Info("sla scan started", "tenants", len(cfg.SlaScanTenants), "interval", cfg.SlaScanInterval.String())
	}

	scan := func() {
		for _, tenantID := range cfg.SlaScanTenants {
			published, err := service.ScanSlaNotifications(ctx, tenantID, time.Now().UTC())
			if err != nil {
				logger.Error("sla scan failed", "tenant_id", tenantID, "error", err)
				continue
			}
			if published > 0 {
				logger.Info("sla scan published notifications", "tenant_id", tenantID, "published", published)
			}
		}
	}
	scan()
	ticker := time.NewTicker(cfg.SlaScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("sla scan stopped")
			return
		case <-ticker.C:
			scan()
		}
	}
}
