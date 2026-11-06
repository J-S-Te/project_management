// Package shutdown 为长驻组件的后台 goroutine 提供有界 join（B2）。
//
// 收到停机信号后，只取消 context 不等于工作已经结束：dispatcher、通知循环、
// 派生事件等 goroutine 仍可能在写库或投递。Exit 前必须按名单等待它们退出，
// 预算内没退出就点名失败，避免把"已退出"写成既成事实。
package shutdown

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultDrainBudget 是后台 goroutine join 的默认预算（秒级，对齐容器 stop 宽限期）。
	DefaultDrainBudget = 20 * time.Second
	// DrainBudgetEnv 按秒覆盖 join 预算；非法或非正数回落默认值。
	DrainBudgetEnv = "PROJECT_MANAGEMENT_SHUTDOWN_DRAIN_SECONDS"
)

// Group 记录后台 goroutine 的名字与完成信号，Drain 时按名单有界等待。
type Group struct {
	mu    sync.Mutex
	tasks []*task
}

type task struct {
	name string
	done chan struct{}
}

// NewGroup 创建空的后台 goroutine 登记表。
func NewGroup() *Group { return &Group{} }

// Go 登记并启动一个后台 goroutine；run 返回即视为已退出。
// 参数：name 用于超预算点名，run 为实际工作循环。
func (group *Group) Go(name string, run func()) {
	if run == nil {
		return
	}
	if strings.TrimSpace(name) == "" {
		name = "unnamed"
	}
	entry := &task{name: name, done: make(chan struct{})}
	group.mu.Lock()
	group.tasks = append(group.tasks, entry)
	group.mu.Unlock()
	go func() {
		defer close(entry.done)
		run()
	}()
}

// Names 返回当前登记的后台 goroutine 名称快照（启动日志与测试用）。
func (group *Group) Names() []string {
	group.mu.Lock()
	defer group.mu.Unlock()
	names := make([]string, 0, len(group.tasks))
	for _, entry := range group.tasks {
		names = append(names, entry.name)
	}
	return names
}

// Drain 在 ctx 预算内等待所有已登记 goroutine 退出。
// 返回值：全部退出为 nil；预算耗尽返回 *TimeoutError，其中点名未退出的 goroutine。
func (group *Group) Drain(ctx context.Context) error {
	group.mu.Lock()
	tasks := make([]*task, len(group.tasks))
	copy(tasks, group.tasks)
	group.mu.Unlock()
	budget := budgetOf(ctx)
	for _, entry := range tasks {
		select {
		case <-entry.done:
		case <-ctx.Done():
			return &TimeoutError{Budget: budget, Pending: pendingNames(tasks)}
		}
	}
	return nil
}

// TimeoutError 表示停机排空预算耗尽，并点名仍未退出的后台 goroutine。
type TimeoutError struct {
	Pending []string
	Budget  time.Duration
}

// Error 输出机器可读的点名信息；调用方应据此记录日志并返回非零退出码。
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("shutdown drain deadline (%s) exceeded; pending goroutines: %s", e.Budget, strings.Join(e.Pending, ", "))
}

// Budget 返回 join 预算：默认 20s，PROJECT_MANAGEMENT_SHUTDOWN_DRAIN_SECONDS 按秒覆盖。
// 返回值：预算耗尽或进程需要等待的最长时间。
func Budget() time.Duration {
	raw := strings.TrimSpace(os.Getenv(DrainBudgetEnv))
	if raw == "" {
		return DefaultDrainBudget
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || seconds <= 0 {
		return DefaultDrainBudget
	}
	return time.Duration(seconds * float64(time.Second))
}

// pendingNames 返回尚未收到完成信号的 goroutine 名称。
func pendingNames(tasks []*task) []string {
	pending := make([]string, 0, len(tasks))
	for _, entry := range tasks {
		select {
		case <-entry.done:
		default:
			pending = append(pending, entry.name)
		}
	}
	return pending
}

// budgetOf 尽力从 ctx 读出剩余预算用于错误信息；无 deadline 时回落默认值。
func budgetOf(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline).Round(time.Millisecond)
	}
	return DefaultDrainBudget
}
