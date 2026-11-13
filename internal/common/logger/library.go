package logger

import (
	"strings"
	"sync"

	gormlogger "gorm.io/gorm/logger"
)

// 第三方库日志默认级别，对齐 Python src/common/logger.py:34-37
const (
	defaultGormLogLevel = "WARN"
)

// LibraryLogOptions 第三方库日志控制，对齐 Python LogConfig 的 suppress_libraries/library_log_levels
type LibraryLogOptions struct {
	// SuppressLibraries 完全屏蔽的库名（大小写不敏感）
	SuppressLibraries []string
	// LibraryLogLevels 按库名指定级别
	LibraryLogLevels map[string]string
}

// libraryLogMu 保护当前库日志配置
var (
	libraryLogMu      sync.RWMutex
	currentLibraryLog = LibraryLogOptions{}
)

// ConfigureLibraryLogs 设置第三方库日志配置
func ConfigureLibraryLogs(opts LibraryLogOptions) {
	libraryLogMu.Lock()
	currentLibraryLog = opts
	libraryLogMu.Unlock()
}

// CurrentGormLogLevel 返回当前配置下的 gorm 日志级别
func CurrentGormLogLevel() gormlogger.LogLevel {
	libraryLogMu.RLock()
	opts := currentLibraryLog
	libraryLogMu.RUnlock()
	return opts.GormLogLevel()
}

// GormLogLevel 按配置解析 gorm 日志级别
// 对齐 Python logger.py:467-480：suppress 命中则完全屏蔽，否则按 library_log_levels 取级别
func (o LibraryLogOptions) GormLogLevel() gormlogger.LogLevel {
	for _, name := range o.SuppressLibraries {
		if matchesLibraryName(name, "gorm") {
			return gormlogger.Silent
		}
	}
	for name, level := range o.LibraryLogLevels {
		if !matchesLibraryName(name, "gorm") {
			continue
		}
		return gormLevelFromName(level)
	}
	return gormLevelFromName(defaultGormLogLevel)
}

// matchesLibraryName 判断配置的库名是否匹配目标库
// 对齐 Python 的 name.startswith(lib) 语义
func matchesLibraryName(configured, target string) bool {
	configured = strings.ToLower(strings.TrimSpace(configured))
	target = strings.ToLower(strings.TrimSpace(target))
	if configured == "" || target == "" {
		return false
	}
	return strings.HasPrefix(target, configured)
}

// gormLevelFromName 把级别名映射为 gorm 级别
func gormLevelFromName(name string) gormlogger.LogLevel {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "SILENT", "CRITICAL", "NONE":
		return gormlogger.Silent
	case "ERROR":
		return gormlogger.Error
	case "WARN", "WARNING":
		return gormlogger.Warn
	case "INFO":
		return gormlogger.Info
	default:
		return gormlogger.Warn
	}
}
