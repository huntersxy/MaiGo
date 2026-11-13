package logger

import (
	"strings"
	"time"

	"go.uber.org/zap/zapcore"
)

// 日志样式默认值，对齐 Python src/common/logger.py:383-385
const (
	defaultDateStyle     = "m-d H:i:s"
	defaultLogLevelStyle = "lite"
	defaultColorText     = "full"
)

// StyleOptions 日志样式配置，对齐 Python LogConfig 的 date_style/log_level_style/color_text
type StyleOptions struct {
	// DateStyle PHP 风格日期格式，如 "m-d H:i:s"
	DateStyle string
	// LogLevelStyle 级别显示样式：lite（不显示）/full（完整名）/compact（首字母）
	LogLevelStyle string
	// ColorText 着色范围：none/title/full
	ColorText string
}

// DefaultStyleOptions 对齐 Python logger.py:383-385 的默认值
func DefaultStyleOptions() StyleOptions {
	return StyleOptions{
		DateStyle:     defaultDateStyle,
		LogLevelStyle: defaultLogLevelStyle,
		ColorText:     defaultColorText,
	}
}

// normalized 归一化样式配置
func (o StyleOptions) normalized() StyleOptions {
	if strings.TrimSpace(o.DateStyle) == "" {
		o.DateStyle = defaultDateStyle
	}
	switch strings.ToLower(strings.TrimSpace(o.LogLevelStyle)) {
	case "full", "compact", "lite":
		o.LogLevelStyle = strings.ToLower(strings.TrimSpace(o.LogLevelStyle))
	default:
		o.LogLevelStyle = defaultLogLevelStyle
	}
	switch strings.ToLower(strings.TrimSpace(o.ColorText)) {
	case "none", "title", "full":
		o.ColorText = strings.ToLower(strings.TrimSpace(o.ColorText))
	default:
		o.ColorText = defaultColorText
	}
	return o
}

// phpDateToGoLayout 把 PHP 风格日期格式转成 Go layout
// 对齐 Python logger.py:432-450 get_timestamp_format 的字符映射
func phpDateToGoLayout(style string) string {
	replacements := []struct{ php, goLayout string }{
		{"Y", "2006"},
		{"m", "01"},
		{"d", "02"},
		{"H", "15"},
		{"i", "04"},
		{"s", "05"},
	}
	layout := style
	for _, item := range replacements {
		layout = strings.ReplaceAll(layout, item.php, item.goLayout)
	}
	return layout
}

// timeEncoderForStyle 按 date_style 生成时间编码器
func timeEncoderForStyle(style string) zapcore.TimeEncoder {
	layout := phpDateToGoLayout(style)
	if strings.TrimSpace(layout) == "" {
		layout = "15:04:05"
	}
	return zapcore.TimeEncoderOfLayout(layout)
}

// levelEncoderForStyle 按 log_level_style 生成级别编码器
// lite 不显示级别，full 显示完整名，compact 显示首字母
func levelEncoderForStyle(style string, color bool) zapcore.LevelEncoder {
	switch style {
	case "full":
		if color {
			return zapcore.CapitalColorLevelEncoder
		}
		return zapcore.CapitalLevelEncoder
	case "compact":
		if color {
			return compactColorLevelEncoder
		}
		return compactLevelEncoder
	default:
		// lite：不显示级别，返回空串
		return func(zapcore.Level, zapcore.PrimitiveArrayEncoder) {}
	}
}

// compactLevelEncoder 只输出级别首字母
func compactLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	name := strings.ToUpper(level.String())
	if name == "" {
		enc.AppendString("")
		return
	}
	enc.AppendString(name[:1])
}

// compactColorLevelEncoder 输出带颜色的级别首字母
func compactColorLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	letter := strings.ToUpper(level.String())
	if letter == "" {
		enc.AppendString("")
		return
	}
	enc.AppendString(colorizeLevel(level, letter[:1]))
}

// colorizeLevel 按级别返回 ANSI 着色文本
func colorizeLevel(level zapcore.Level, text string) string {
	var code string
	switch level {
	case zapcore.DebugLevel:
		code = "35"
	case zapcore.InfoLevel:
		code = "34"
	case zapcore.WarnLevel:
		code = "33"
	case zapcore.ErrorLevel:
		code = "31"
	case zapcore.DPanicLevel, zapcore.PanicLevel, zapcore.FatalLevel:
		code = "31"
	default:
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// applyStyleToEncoderConfig 把样式配置应用到编码器配置
func applyStyleToEncoderConfig(cfg *zapcore.EncoderConfig, style StyleOptions) {
	style = style.normalized()
	cfg.EncodeTime = timeEncoderForStyle(style.DateStyle)
	cfg.EncodeLevel = levelEncoderForStyle(style.LogLevelStyle, style.ColorText != "none")
}

// formatTimestampForStyle 按样式格式化时间，供测试与诊断
func formatTimestampForStyle(value time.Time, style string) string {
	layout := phpDateToGoLayout(style)
	if strings.TrimSpace(layout) == "" {
		layout = "15:04:05"
	}
	return value.Format(layout)
}
