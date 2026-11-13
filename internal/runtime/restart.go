package runtime

import (
	"os"
	"strings"
	"sync"
	"time"
)

var (
	restartMu      sync.Mutex
	restartTrigger chan struct{}
)

// SetRestartTrigger 注册 Worker 主循环的优雅退出触发器；由 main 在启动 Web 前调用
func SetRestartTrigger(ch chan struct{}) {
	restartMu.Lock()
	restartTrigger = ch
	restartMu.Unlock()
}

// ScheduleWorkerRestart 在响应已发送后触发 Worker 优雅关闭并以退出码 42 结束
//
// pendingBinary 非空时写入 pending 标记；为空则沿用已有标记或 MAIBOT_UPDATE_PENDING_PATH
func ScheduleWorkerRestart(pendingBinary string) error {
	if !IsWorkerProcess() {
		return ErrRestartRequiresWorker
	}
	pendingBinary = strings.TrimSpace(pendingBinary)
	if pendingBinary != "" {
		if err := SetPendingUpdate(pendingBinary); err != nil {
			return err
		}
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		restartMu.Lock()
		ch := restartTrigger
		restartMu.Unlock()
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
			return
		}
		// 单进程 Worker（无 Runner）时无法交给外部监督，只能直接退出
		os.Exit(RestartExitCode)
	}()
	return nil
}
