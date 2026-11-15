package platform

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"sync"
	"time"

	central "github.com/j-s-te/project-management/internal/sessiongovernance"
)

const centralReadBudget = 2 * time.Second
const maximumCentralReadFlights = 256

type centralReadFlight struct {
	done  chan struct{}
	state central.Context
	err   error
}

type centralReadGroup struct {
	mu      sync.Mutex
	flights map[[sha256.Size]byte]*centralReadFlight
}

// readCentralState 合并同绑定的在途只读检查，不额外缓存权威结果。
// 参数：ctx控制当前等待者，binding为服务端绑定秘密，仅以摘要作为合并键。
// 返回值：中央原始窗口或错误；单个等待者取消不会结束其他等待者的检查。
func (a *OIDCAuthenticator) readCentralState(ctx context.Context, binding string) (central.Context, error) {
	if ctx.Err() != nil {
		return central.Context{}, central.ErrUnavailable
	}
	key := sha256.Sum256([]byte(binding))
	group := &a.centralReads
	group.mu.Lock()
	flight := group.flights[key]
	if flight == nil {
		if len(group.flights) >= maximumCentralReadFlights {
			group.mu.Unlock()
			return central.Context{}, central.ErrUnavailable
		}
		if group.flights == nil {
			group.flights = make(map[[sha256.Size]byte]*centralReadFlight)
		}
		flight = &centralReadFlight{done: make(chan struct{})}
		group.flights[key] = flight
		go a.runCentralRead(context.WithoutCancel(ctx), key, binding, flight)
	}
	group.mu.Unlock()
	select {
	case <-ctx.Done():
		return central.Context{}, central.ErrUnavailable
	case <-flight.done:
		return flight.state, flight.err
	}
}

// runCentralRead 在整体期限内最多重试一次明确的暂时只读故障。
// 参数：parent保留追踪信息，key为绑定摘要，binding为秘密，flight为共同等待结果。
// 返回值：无；不重试已失效、冲突或活动写入，不从旧窗口回退放行。
func (a *OIDCAuthenticator) runCentralRead(parent context.Context, key [sha256.Size]byte,
	binding string, flight *centralReadFlight) {
	defer a.finishCentralRead(key, flight)
	ctx, cancel := context.WithTimeout(parent, centralReadBudget)
	defer cancel()
	flight.state, flight.err = a.options.CentralClient.State(ctx, binding)
	if errors.Is(flight.err, central.ErrUnavailable) && ctx.Err() == nil {
		flight.state, flight.err = a.options.CentralClient.State(ctx, binding)
	}
}

// finishCentralRead 发布结果并释放合并槽位，异常只能返回暂不可用。
// 参数：key为绑定摘要，flight为本次结果，均不写入日志或持久化。
// 返回值：无；关闭完成信号后所有等待者获得同一分类结果。
func (a *OIDCAuthenticator) finishCentralRead(key [sha256.Size]byte, flight *centralReadFlight) {
	if recover() != nil {
		flight.state, flight.err = central.Context{}, central.ErrUnavailable
		slog.Error("central session reader panicked")
	}
	a.centralReads.mu.Lock()
	delete(a.centralReads.flights, key)
	close(flight.done)
	a.centralReads.mu.Unlock()
}
