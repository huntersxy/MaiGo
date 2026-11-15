package memruntime

// A_memorix 运行时日志适配，对齐 Python core/runtime 各模块的 get_logger

import (
	"go.uber.org/zap"

	"MaiGo/internal/A_memorix/core/utils"
)

// zapLogger 把 Python get_logger(name) 映射到全局 zap logger
type zapLogger struct {
	name string
}

// NewZapLogger 构造带 logger 名称的日志适配器
func NewZapLogger(name string) utils.Logger { return zapLogger{name: name} }

// fields 返回统一的 logger 名称字段
func (l zapLogger) fields() []zap.Field {
	return []zap.Field{zap.String("logger", l.name)}
}

// Debug 输出 debug 日志
func (l zapLogger) Debug(msg string) { zap.L().Debug(msg, l.fields()...) }

// Info 输出 info 日志
func (l zapLogger) Info(msg string) { zap.L().Info(msg, l.fields()...) }

// Warning 输出 warning 日志
func (l zapLogger) Warning(msg string) { zap.L().Warn(msg, l.fields()...) }

// Error 输出 error 日志
func (l zapLogger) Error(msg string) { zap.L().Error(msg, l.fields()...) }
