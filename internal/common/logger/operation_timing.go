package logger

import (
	"time"

	"go.uber.org/zap"
)

// 慢操作阈值，对齐 Python src/common/operation_timing.py:34
const slowOperationThreshold = 500 * time.Millisecond

// OperationTimingOptions 存储链路耗时日志选项
// 对齐 Python operation_timing.py:18-24 的 log_operation 参数
type OperationTimingOptions struct {
	// Operation 操作名，如 snapshot.write_snapshot
	Operation string
	// Report 为真时正常结束也记 INFO
	Report bool
	// Quiet 为真时省略正常日志
	Quiet bool
	// WarnSlow 为真时慢操作记 WARNING
	WarnSlow bool
	// Details 附加字段，不记录请求或文件内容
	Details []zap.Field
}

// OperationTiming 一次存储操作的计时句柄
type OperationTiming struct {
	opts      OperationTimingOptions
	startedAt time.Time
}

// StartOperation 开始计时，返回句柄供 Finish 使用
// 对齐 Python log_operation 的 contextmanager 语义
func StartOperation(opts OperationTimingOptions) *OperationTiming {
	timing := &OperationTiming{opts: opts, startedAt: time.Now()}
	if !opts.Quiet {
		fields := append([]zap.Field{zap.String("operation", opts.Operation)}, opts.Details...)
		if opts.Report {
			zap.L().Info("存储操作开始", fields...)
		} else {
			zap.L().Debug("存储操作开始", fields...)
		}
	}
	return timing
}

// Finish 结束计时并按耗时与结果决定日志级别
// outcome 为 "completed" 或 "failed"，对齐 Python 的 outcome 取值
func (t *OperationTiming) Finish(outcome string) {
	if t == nil {
		return
	}
	if outcome == "" {
		outcome = "completed"
	}
	elapsed := time.Since(t.startedAt)
	slow := t.opts.WarnSlow && elapsed >= slowOperationThreshold
	fields := append([]zap.Field{
		zap.String("operation", t.opts.Operation),
		zap.Float64("elapsed_seconds", elapsed.Seconds()),
		zap.String("outcome", outcome),
	}, t.opts.Details...)
	if slow || outcome == "failed" {
		zap.L().Warn("存储操作结束", fields...)
		return
	}
	if t.opts.Quiet {
		return
	}
	if t.opts.Report {
		zap.L().Info("存储操作结束", fields...)
		return
	}
	zap.L().Debug("存储操作结束", fields...)
}

// TimedOperation 测量一次同步操作的整体耗时，异常时记 failed
// 对齐 Python operation_timing.py:43-54 的 timed_operation 装饰器
func TimedOperation(opts OperationTimingOptions, run func() error) error {
	timing := StartOperation(opts)
	err := run()
	if err != nil {
		timing.Finish("failed")
		return err
	}
	timing.Finish("completed")
	return nil
}
