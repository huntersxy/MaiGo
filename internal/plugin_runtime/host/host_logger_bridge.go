package host

import (
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"MaiGo/internal/plugin_runtime/protocol"
)

const runnerRuntimeLoggerName = "plugin_runtime.runtime"

// SetLogSink 注入 Runner 日志出口；为 nil 时写入全局 zap logger
func (h *Host) SetLogSink(sink func(pluginID string, entry protocol.RunnerLogEntry)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.logSink = sink
	h.mu.Unlock()
}

// HandleRunnerLogs 处理 runner.log_batch 广播，返回接受条数；对齐 host/logger_bridge.py
func (h *Host) HandleRunnerLogs(pluginID string, entries []protocol.RunnerLogEntry) int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	sink := h.logSink
	h.mu.RUnlock()
	accepted := 0
	for _, entry := range entries {
		normalized := normalizeRunnerLogEntry(entry)
		if sink != nil {
			sink(pluginID, normalized)
		} else {
			writeRunnerLogRecord(normalized)
		}
		accepted++
	}
	return accepted
}

func normalizeRunnerLogEntry(entry protocol.RunnerLogEntry) protocol.RunnerLogEntry {
	name := strings.TrimSpace(entry.LoggerName)
	// 对齐 Python RunnerLogBridge：Runner 内部运行时日志统一归到 plugin_runtime.runtime，插件日志保留原名
	if strings.HasPrefix(name, "plugin_runtime.") {
		name = runnerRuntimeLoggerName
	}
	entry.LoggerName = name
	return entry
}

// runnerLogZapLevel 把 stdlib logging 整数级别映射到 zap 级别
// Python 语义：10 DEBUG / 20 INFO / 30 WARNING / 40 ERROR / 50 CRITICAL
func runnerLogZapLevel(level int) zapcore.Level {
	switch {
	case level <= 10:
		return zapcore.DebugLevel
	case level <= 20:
		return zapcore.InfoLevel
	case level <= 30:
		return zapcore.WarnLevel
	default:
		return zapcore.ErrorLevel
	}
}

func writeRunnerLogRecord(entry protocol.RunnerLogEntry) {
	logger := zap.L()
	if entry.LoggerName != "" {
		logger = logger.Named(entry.LoggerName)
	}
	// Python 把 timestamp_ms 写回 LogRecord.created/msecs，日志时刻取 Runner 侧而非宿主收到时刻
	fields := make([]zap.Field, 0, 3)
	if entry.TimestampMS != 0 {
		fields = append(fields, zap.Int64("timestamp_ms", entry.TimestampMS))
		fields = append(fields, zap.Time("created", time.UnixMilli(entry.TimestampMS)))
	}
	// Python 侧把 exception_text 写入 LogRecord.exc_text；这里保留为结构化字段
	if entry.ExceptionText != "" {
		fields = append(fields, zap.String("exception_text", entry.ExceptionText))
	}
	logger.Log(runnerLogZapLevel(entry.Level), entry.Message, fields...)
}
