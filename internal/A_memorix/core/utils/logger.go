package utils

// A_memorix 工具模块共用的最小日志接口，便于注入宿主 zap 日志

// Logger 提供与 Python logging 对齐的四级日志
type Logger interface {
	Debug(msg string)
	Info(msg string)
	Warning(msg string)
	Error(msg string)
}

// NopLogger 未注入日志时的静默实现
type NopLogger struct{}

// Debug 空实现
func (NopLogger) Debug(string) {}

// Info 空实现
func (NopLogger) Info(string) {}

// Warning 空实现
func (NopLogger) Warning(string) {}

// Error 空实现
func (NopLogger) Error(string) {}

// OrLogger 在 logger 为 nil 时回退到静默实现
func OrLogger(logger Logger) Logger {
	if logger == nil {
		return NopLogger{}
	}
	return logger
}
