package committer

import "go.uber.org/zap"

// runSafely 执行单条事件；panic 只记录日志，不中断调度协程。
func runSafely(run func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			zap.L().Error("入站事件处理 panic", zap.Any("panic", recovered))
		}
	}()
	run()
}
