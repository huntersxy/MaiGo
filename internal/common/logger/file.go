package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap/zapcore"
)

// defaultLogPattern 日志文件主名，对齐 Python LOG_DIR 下的 *.log*
const defaultLogPattern = "maibot"

// FileOptions 文件日志配置，对齐 Python src/common/logger.py:57-73 的 RotatingFileHandler 参数
type FileOptions struct {
	// Dir 日志目录，默认 logs
	Dir string
	// MaxBytes 单文件上限，默认 5 MiB，最小 1024
	MaxBytes int64
	// BackupCount 保留份数，默认 30，最小 1
	BackupCount int
	// CleanupDays 过期天数，默认 30，最小 1
	CleanupDays int
}

// DefaultFileOptions 对齐 Python logger.py:387-391 的默认值
func DefaultFileOptions() FileOptions {
	return FileOptions{
		Dir:         "logs",
		MaxBytes:    5 * 1024 * 1024,
		BackupCount: 30,
		CleanupDays: 30,
	}
}

// normalized 归一化配置，对齐 Python 的 max(1024, ...) 与 max(1, ...)
func (o FileOptions) normalized() FileOptions {
	if strings.TrimSpace(o.Dir) == "" {
		o.Dir = "logs"
	}
	if o.MaxBytes < 1024 {
		o.MaxBytes = 1024
	}
	if o.BackupCount < 1 {
		o.BackupCount = 1
	}
	if o.CleanupDays < 1 {
		o.CleanupDays = 1
	}
	return o
}

// rotatingFileWriter 按大小轮转的日志写入器，对齐 TimestampedFileHandler 的轮转份数语义
type rotatingFileWriter struct {
	mu      sync.Mutex
	opts    FileOptions
	file    *os.File
	size    int64
	pattern string
}

// newRotatingFileWriter 打开或创建当前日志文件
func newRotatingFileWriter(opts FileOptions) (*rotatingFileWriter, error) {
	opts = opts.normalized()
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	writer := &rotatingFileWriter{opts: opts, pattern: defaultLogPattern}
	if err := writer.openCurrent(); err != nil {
		return nil, err
	}
	return writer, nil
}

// currentPath 当前写入的日志文件路径
func (w *rotatingFileWriter) currentPath() string {
	return filepath.Join(w.opts.Dir, w.pattern+".log")
}

func (w *rotatingFileWriter) openCurrent() error {
	path := w.currentPath()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return fmt.Errorf("读取日志文件状态失败: %w", statErr)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

// Write 写入日志，超过上限时先轮转
func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if err := w.openCurrent(); err != nil {
			return 0, err
		}
	}
	if w.size+int64(len(p)) > w.opts.MaxBytes && w.size > 0 {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	written, err := w.file.Write(p)
	w.size += int64(written)
	return written, err
}

// rotateLocked 关闭当前文件并按序号重命名，超出保留份数的旧文件删除
func (w *rotatingFileWriter) rotateLocked() error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	// 从最旧到最新依次后移，避免覆盖
	for index := w.opts.BackupCount - 1; index >= 1; index-- {
		from := w.backupPath(index)
		to := w.backupPath(index + 1)
		if _, err := os.Stat(from); err == nil {
			_ = os.Rename(from, to)
		}
	}
	if _, err := os.Stat(w.currentPath()); err == nil {
		_ = os.Rename(w.currentPath(), w.backupPath(1))
	}
	// 删除超出保留份数的文件
	for index := w.opts.BackupCount + 1; ; index++ {
		path := w.backupPath(index)
		if _, err := os.Stat(path); err != nil {
			break
		}
		_ = os.Remove(path)
	}
	return w.openCurrent()
}

func (w *rotatingFileWriter) backupPath(index int) string {
	return filepath.Join(w.opts.Dir, fmt.Sprintf("%s.log.%d", w.pattern, index))
}

// Sync 刷新到磁盘
func (w *rotatingFileWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Sync()
}

// Close 关闭当前文件
func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// CleanupOldLogs 删除超过保留天数的日志文件，对齐 Python cleanup_old_logs
func CleanupOldLogs(opts FileOptions) (deleted int, freed int64, err error) {
	opts = opts.normalized()
	entries, readErr := os.ReadDir(opts.Dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, 0, nil
		}
		return 0, 0, readErr
	}
	cutoff := time.Now().Add(-time.Duration(opts.CleanupDays) * 24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, defaultLogPattern+".log") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if removeErr := os.Remove(filepath.Join(opts.Dir, name)); removeErr != nil {
			continue
		}
		deleted++
		freed += info.Size()
	}
	return deleted, freed, nil
}

// parseZapLevel 解析日志级别，非法值回退 fallback
func parseZapLevel(value string, fallback zapcore.Level) zapcore.Level {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DEBUG":
		return zapcore.DebugLevel
	case "INFO":
		return zapcore.InfoLevel
	case "WARN", "WARNING":
		return zapcore.WarnLevel
	case "ERROR":
		return zapcore.ErrorLevel
	case "FATAL":
		return zapcore.FatalLevel
	default:
		return fallback
	}
}

// sortedLogFiles 返回按名称排序的日志文件，供测试与诊断
func sortedLogFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}
