package logger

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type logBuffer struct {
	mu      sync.RWMutex
	lines   []string
	written uint64
	max     int
}

func newLogBuffer(max int) *logBuffer { return &logBuffer{max: max} }

func (b *logBuffer) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if line == "" {
		return len(p), nil
	}
	b.mu.Lock()
	b.lines = append(b.lines, line)
	seq := b.written
	b.written++
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
	b.mu.Unlock()
	publishRawLogLine(line, seq)
	return len(p), nil
}

func (b *logBuffer) Sync() error { return nil }

// Snapshot 返回缓冲副本和首行对应的绝对序号，保证同一行内容重复出现时也能唯一定位
func (b *logBuffer) Snapshot() ([]string, uint64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]string, len(b.lines))
	copy(result, b.lines)
	return result, b.written - uint64(len(b.lines))
}

var recentLogs = newLogBuffer(300)
var logJSONAPI = sonic.Config{UseNumber: true}.Froze()

// InitOptions 日志初始化选项，对齐 Python LogConfig 的 file/console 分级
type InitOptions struct {
	// Level 全局级别，作为 console/file 未指定时的回退
	Level string
	// Debug 未指定 Level 时是否按 debug 处理
	Debug bool
	// ConsoleLevel 控制台级别，空值回退 Level
	ConsoleLevel string
	// FileLevel 文件级别，空值回退 Level
	FileLevel string
	// File 文件日志配置；Enabled 为 false 时不写文件
	File FileOptions
	// FileEnabled 是否启用文件日志
	FileEnabled bool
	// Style 日志样式（date_style/log_level_style/color_text）
	Style StyleOptions
}

// InitWithOptions 按分级配置初始化日志系统
// 对齐 Python src/common/logger.py:57-73：file_level 取 file_log_level 否则 log_level，console_level 同理
func InitWithOptions(opts InitOptions) {
	fallback := parseZapLevel(opts.Level, zapcore.InfoLevel)
	if strings.TrimSpace(opts.Level) == "" && opts.Debug {
		fallback = zapcore.DebugLevel
	}
	consoleLevel := fallback
	if strings.TrimSpace(opts.ConsoleLevel) != "" {
		consoleLevel = parseZapLevel(opts.ConsoleLevel, fallback)
	}
	fileLevel := fallback
	if strings.TrimSpace(opts.FileLevel) != "" {
		fileLevel = parseZapLevel(opts.FileLevel, fallback)
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.TimeEncoderOfLayout("15:04:05"),
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	// 对齐 Python logger.py:432-450 与 :663-690：date_style 决定时间格式，log_level_style 决定级别显示
	applyStyleToEncoderConfig(&encoderConfig, opts.Style)
	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)
	bufferConfig := encoderConfig
	bufferConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
	bufferConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	bufferEncoder := zapcore.NewJSONEncoder(bufferConfig)

	cores := []zapcore.Core{
		zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), consoleLevel),
		zapcore.NewCore(bufferEncoder, recentLogs, consoleLevel),
	}
	if opts.FileEnabled {
		if writer, err := newRotatingFileWriter(opts.File); err == nil {
			fileEncoder := zapcore.NewJSONEncoder(bufferConfig)
			cores = append(cores, zapcore.NewCore(fileEncoder, zapcore.AddSync(writer), fileLevel))
			activeFileWriter = writer
		}
	}
	zap.ReplaceGlobals(zap.New(zapcore.NewTee(cores...)))
}

// activeFileWriter 当前文件写入器，供关停时刷新
var activeFileWriter *rotatingFileWriter

// CloseFile 刷新并关闭文件日志
func CloseFile() error {
	if activeFileWriter == nil {
		return nil
	}
	writer := activeFileWriter
	activeFileWriter = nil
	_ = writer.Sync()
	return writer.Close()
}

// Init 初始化日志系统
func Init(level string, debug bool) {
	// 解析日志级别
	var zapLevel zapcore.Level
	switch level {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "info":
		zapLevel = zapcore.InfoLevel
	case "warn":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		if debug {
			zapLevel = zapcore.DebugLevel
		} else {
			zapLevel = zapcore.InfoLevel
		}
	}

	// 编码器配置
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.TimeEncoderOfLayout("15:04:05"),
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// 控制台与后台日志页共享同一条日志流，内存缓冲有界，重启后自然清空
	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)
	bufferConfig := encoderConfig
	bufferConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
	bufferConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	bufferEncoder := zapcore.NewJSONEncoder(bufferConfig)
	consoleCore := zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), zapLevel)
	bufferCore := zapcore.NewCore(bufferEncoder, recentLogs, zapLevel)
	zap.ReplaceGlobals(zap.New(zapcore.NewTee(consoleCore, bufferCore)))
}

type QueryResult struct {
	Lines    []Line
	Total    int
	Filtered int
}

type Line struct {
	Seq     uint64
	Time    string
	Level   string
	Message string
	Fields  []Field
	Text    string
}

type Field struct {
	Name  string
	Value string
}

func Query(keyword, minimumLevel string) QueryResult {
	raw, firstSeq := recentLogs.Snapshot()
	result := QueryResult{Total: len(raw), Lines: make([]Line, 0, len(raw))}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	minimum := zapcore.DebugLevel
	if err := minimum.UnmarshalText([]byte(strings.ToLower(strings.TrimSpace(minimumLevel)))); err != nil {
		minimum = zapcore.DebugLevel
	}
	for i, line := range raw {
		var fields map[string]any
		if err := logJSONAPI.UnmarshalFromString(line, &fields); err != nil {
			continue
		}
		levelText, _ := fields["level"].(string)
		var level zapcore.Level
		if level.UnmarshalText([]byte(levelText)) != nil || level < minimum {
			continue
		}
		formatted := formatLogLine(fields)
		if keyword != "" && !strings.Contains(strings.ToLower(formatted.Text), keyword) {
			continue
		}
		formatted.Seq = firstSeq + uint64(i)
		result.Lines = append(result.Lines, formatted)
	}
	result.Filtered = len(result.Lines)
	return result
}

func formatLogLine(fields map[string]any) Line {
	timeText := fmt.Sprint(fields["time"])
	levelText := strings.ToUpper(fmt.Sprint(fields["level"]))
	message := fmt.Sprint(fields["msg"])
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if key != "time" && key != "level" && key != "msg" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	logFields := make([]Field, 0, len(keys))
	fieldParts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := fmt.Sprint(fields[key])
		logFields = append(logFields, Field{Name: key, Value: value})
		fieldParts = append(fieldParts, key+"="+value)
	}
	fieldText := strings.Join(fieldParts, " ")
	text := strings.Join([]string{timeText, levelText, message}, " ")
	if fieldText != "" {
		text += " " + fieldText
	}
	return Line{Time: timeText, Level: levelText, Message: message, Fields: logFields, Text: text}
}
