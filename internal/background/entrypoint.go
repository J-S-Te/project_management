package background

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/bootstrap"
	"github.com/j-s-te/project-management/internal/config"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/internal/temporalworker"
	"go.temporal.io/sdk/client"
)

func optionsFromConfig(c config.Config) Options {
	b := c.Background
	return Options{Namespace: c.TemporalNamespace, Engine: Engine(b.Engine), TenantID: b.TenantID, Environment: b.Environment, TaskQueue: b.TaskQueue, DeploymentName: b.DeploymentName, BuildID: b.BuildID, HealthAddress: b.HealthAddress, ControlSocket: b.ControlSocket, VersioningEnabled: b.VersioningEnabled, SLAInterval: b.SLAInterval, DispatchInterval: b.DispatchInterval, RunBudget: b.RunBudget, ActivityTimeout: b.ActivityTimeout, SLAActivityTimeout: b.SLAActivityTimeout, WorkflowRunTimeout: b.WorkflowRunTimeout, MaxItems: b.MaxItems}
}

// Run is shared by both historical binary names. A selected engine is never
// changed after a dependency failure. The commercial gate stays alive until all
// admitted activities have completed or the bounded stop has canceled them.
func Run(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	o := optionsFromConfig(cfg)
	if e := o.Validate(); e != nil {
		return e
	}
	lifeCtx, lifeCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer lifeCancel()
	gate, e := bootstrap.OpenCommercialLicense(lifeCtx, logger)
	if e != nil {
		return errors.New("commercial license configuration unavailable")
	}
	db, e := bootstrap.OpenDatabase(ctx, cfg.MySQLDSN)
	if e != nil {
		return errors.New("background database unavailable")
	}
	defer bootstrap.CloseDatabase(db)
	notifications := platform.NewNotificationPublisher(cfg.PlatformBaseURL, cfg.PlatformNotificationURL, cfg.PlatformNotificationClientID, cfg.PlatformNotificationSecret, cfg.PlatformNotificationScope)
	var personnel platform.OwnerDirectory
	if cfg.OwnerDirectoryEnabled {
		personnel = platform.NewOwnerDirectory(cfg.PlatformBaseURL, cfg.PlatformOwnerDirectoryURL, cfg.PlatformOwnerDirectoryClientID, cfg.PlatformOwnerDirectorySecret, cfg.PlatformOwnerDirectoryScope)
		if personnel == nil {
			return errors.New("background owner directory unavailable")
		}
	}
	svc := &application.Service{Repo: store.NewRepository(db), Personnel: personnel, Notifications: notifications, Logger: logger, CheckBusinessLicense: func(ctx context.Context) error { return gate.Check(ctx, "MUTATE_BUSINESS") }}
	// Validates the migration-dependent outbox surfaces with a real read, without
	// changing business data or confusing an unavailable table with an empty queue.
	if _, e = svc.CountActiveBackgroundLeases(ctx, o.TenantID, time.Now().UTC()); e != nil {
		return errors.New("background schema unavailable")
	}
	var tc client.Client
	if o.Engine == EngineTemporal {
		metrics := temporalworker.NewMetricsRegistry()
		if e = temporalworker.StartMetricsServer(lifeCtx, cfg.TemporalMetricsAddress, metrics, logger); e != nil {
			return errors.New("background metrics unavailable")
		}
		co := client.Options{HostPort: cfg.TemporalAddress, Namespace: cfg.TemporalNamespace, Logger: quietTemporalLogger{logger}, MetricsHandler: metrics}
		if cfg.TemporalTLS {
			co.ConnectionOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			co.ConnectionOptions.TLSDisabled = true
		}
		if cfg.TemporalAPIKey != "" {
			co.Credentials = client.NewAPIKeyStaticCredentials(cfg.TemporalAPIKey)
		}
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		tc, e = client.DialContext(dialCtx, co)
		cancel()
		if e != nil {
			return errors.New("background Temporal unavailable")
		}
		defer tc.Close()
	}
	r, e := NewRuntime(o, svc, tc, logger)
	if e != nil {
		return e
	}
	healthListener, e := net.Listen("tcp", o.HealthAddress)
	if e != nil {
		return errors.New("background health listener unavailable")
	}
	defer healthListener.Close()
	health := &http.Server{Handler: r.Handler(), ReadHeaderTimeout: 5 * time.Second}
	defer health.Close()
	if e = r.Start(lifeCtx); e != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), o.WorkflowRunTimeout+time.Minute)
		defer cancel()
		_ = r.Stop(stopCtx)
		return e
	}
	healthErr := make(chan error, 1)
	go func() { healthErr <- health.Serve(healthListener) }()
	select {
	case <-ctx.Done():
	case <-healthErr:
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), o.WorkflowRunTimeout+time.Minute)
	defer cancel()
	if e = r.Stop(stopCtx); e != nil {
		return e
	}
	if ctx.Err() == nil {
		return errors.New("background health server stopped")
	}
	return nil
}
