package host

import (
	runner "maibot-go/internal/plugin_runtime/runner"
	"sync/atomic"
	"time"
)

// 进程级关停状态，对齐 src/common/shutdown.py 的 request_shutdown / is_shutdown_requested
var processShutdownRequested atomic.Bool

// RequestShutdown 标记当前进程已进入关停流程，后续 Runner 关停走快速时序
func RequestShutdown() {
	processShutdownRequested.Store(true)
}

// IsShutdownRequested 返回当前进程是否已进入关停流程
func IsShutdownRequested() bool {
	return processShutdownRequested.Load()
}

// resetShutdownStateForTest 仅测试使用，隔离进程级关停状态
func resetShutdownStateForTest() {
	processShutdownRequested.Store(false)
}

// runnerShutdownTimings 单次 Runner 关停的等待时序，对齐 supervisor._shutdown_runner
//
// 结构与实现位于 runner 子包（对齐 Python src/plugin_runtime/runner/），
// 这里保留别名与构造函数。
type runnerShutdownTimings = runner.ShutdownTimings

func runnerShutdownTimingsFor(fast bool) runnerShutdownTimings {
	if fast {
		return runner.NewShutdownTimings(
			fastShutdownDrainTimeoutMS,
			1500*time.Millisecond, 2*time.Second, time.Second, time.Second,
		)
	}
	return runner.NewShutdownTimings(
		defaultShutdownDrainTimeoutMS,
		5*time.Second, 5*time.Second, 5*time.Second, time.Second,
	)
}
