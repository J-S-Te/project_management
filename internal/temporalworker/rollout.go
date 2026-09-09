package temporalworker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
)

// 启动时收敛 Worker 当前版本的等待上限与重试间隔。Worker 刚启动时 Poller 注册存在短暂
// 延迟，而提升要求目标版本已经有 Poller，因此需要有限次重试。
const (
	// CurrentVersionEnsureTimeout 是入口等待当前版本收敛的上限。
	CurrentVersionEnsureTimeout = 20 * time.Second
	currentVersionAttempts      = 6
)

// currentVersionRetryInterval 是启动收敛的重试间隔；声明为变量以便测试缩短等待。
var currentVersionRetryInterval = 2 * time.Second

// DeploymentHandle 是发布控制命令使用的最小 Temporal 控制面边界。
type DeploymentHandle interface {
	Describe(context.Context, client.WorkerDeploymentDescribeOptions) (client.WorkerDeploymentDescribeResponse, error)
	SetCurrentVersion(context.Context, client.WorkerDeploymentSetCurrentVersionOptions) (client.WorkerDeploymentSetCurrentVersionResponse, error)
	SetRampingVersion(context.Context, client.WorkerDeploymentSetRampingVersionOptions) (client.WorkerDeploymentSetRampingVersionResponse, error)
}

// PromoteCurrent 使用最新 conflict token 将已有 Poller 的 Build ID 提升为 Current。
func PromoteCurrent(ctx context.Context, handle DeploymentHandle, buildID, identity string) error {
	if handle == nil || strings.TrimSpace(buildID) == "" || strings.TrimSpace(identity) == "" {
		return fmt.Errorf("deployment handle, build ID and identity are required")
	}
	description, err := handle.Describe(ctx, client.WorkerDeploymentDescribeOptions{})
	if err != nil {
		return fmt.Errorf("describe Temporal worker deployment: %w", err)
	}
	_, err = handle.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{
		BuildID: strings.TrimSpace(buildID), ConflictToken: description.ConflictToken,
		Identity: strings.TrimSpace(identity), IgnoreMissingTaskQueues: false, AllowNoPollers: false,
	})
	if err != nil {
		return fmt.Errorf("promote Temporal current worker version: %w", err)
	}
	return nil
}

// EnsureCurrentVersion 在 Worker 启动后确认其 Build ID 已成为 Deployment 的 Current 版本。
//
// Worker 版本路由开启而 Deployment 的 Current 版本为空时，新工作流会以 UNVERSIONED 入队，
// 而版本化 Worker 只消费自身版本的队列，任务因此没有任何 Poller 领取，只能一直等待到请求
// 超时（网关表现为 504，应用层表现为处理超时）。启动时主动收敛可消除这一原本只能依赖人工
// 执行 project-worker-rollout 的运维遗漏；已经指向同一 Build ID 时直接返回，保持幂等。
func EnsureCurrentVersion(ctx context.Context, handle DeploymentHandle, buildID, identity string) error {
	buildID = strings.TrimSpace(buildID)
	identity = strings.TrimSpace(identity)
	if handle == nil || buildID == "" || identity == "" {
		return fmt.Errorf("deployment handle, build ID and identity are required")
	}
	var lastErr error
	for attempt := 1; attempt <= currentVersionAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("wait for worker poller before promoting current version: %w", ctx.Err())
			case <-time.After(currentVersionRetryInterval):
			}
		}
		description, err := handle.Describe(ctx, client.WorkerDeploymentDescribeOptions{})
		if err != nil {
			lastErr = fmt.Errorf("describe Temporal worker deployment: %w", err)
			continue
		}
		if current := description.Info.RoutingConfig.CurrentVersion; current != nil && current.BuildID == buildID {
			return nil
		}
		if _, err := handle.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{
			BuildID: buildID, ConflictToken: description.ConflictToken, Identity: identity,
			IgnoreMissingTaskQueues: false, AllowNoPollers: false,
		}); err != nil {
			lastErr = fmt.Errorf("promote Temporal current worker version: %w", err)
			continue
		}
		return nil
	}
	return lastErr
}

// EnsureCurrentVersionOnStartup 在后台把本进程的 Worker Build ID 收敛为 Deployment 的 Current 版本。
// 只在版本路由开启时生效；失败仅记录日志，不阻断 API 或 Worker 继续启动。
func EnsureCurrentVersionOnStartup(ctx context.Context, temporalClient client.Client, logger *slog.Logger, config VersioningConfig) {
	if !config.Enabled || temporalClient == nil || logger == nil {
		return
	}
	deploymentName := strings.TrimSpace(config.DeploymentName)
	buildID := strings.TrimSpace(config.BuildID)
	if deploymentName == "" || buildID == "" {
		return
	}
	go func() {
		ensureCtx, cancel := context.WithTimeout(ctx, CurrentVersionEnsureTimeout)
		defer cancel()
		handle := temporalClient.WorkerDeploymentClient().GetHandle(deploymentName)
		if err := EnsureCurrentVersion(ensureCtx, handle, buildID, "startup-auto-promote:"+buildID); err != nil {
			logger.Error("ensure Temporal worker deployment current version", "error", err, "deployment", deploymentName, "build_id", buildID)
			return
		}
		logger.Info("Temporal worker deployment current version ensured", "deployment", deploymentName, "build_id", buildID)
	}()
}

// RampVersion 将 Build ID 灰度到允许的发布百分比；0 表示撤销当前灰度。
func RampVersion(ctx context.Context, handle DeploymentHandle, buildID, identity string, percentage float32) error {
	if handle == nil || strings.TrimSpace(identity) == "" || !allowedRampPercentage(percentage) || percentage > 0 && strings.TrimSpace(buildID) == "" {
		return fmt.Errorf("valid deployment handle, build ID, identity and ramp percentage are required")
	}
	description, err := handle.Describe(ctx, client.WorkerDeploymentDescribeOptions{})
	if err != nil {
		return fmt.Errorf("describe Temporal worker deployment: %w", err)
	}
	_, err = handle.SetRampingVersion(ctx, client.WorkerDeploymentSetRampingVersionOptions{
		BuildID: strings.TrimSpace(buildID), Percentage: percentage, ConflictToken: description.ConflictToken,
		Identity: strings.TrimSpace(identity), IgnoreMissingTaskQueues: false,
	})
	if err != nil {
		return fmt.Errorf("update Temporal ramping worker version: %w", err)
	}
	return nil
}

func allowedRampPercentage(value float32) bool {
	for _, allowed := range []float32{0, 5, 25, 50, 100} {
		if value == allowed {
			return true
		}
	}
	return false
}
