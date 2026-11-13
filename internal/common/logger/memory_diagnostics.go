package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// 内存诊断默认参数，对齐 Python src/common/memory_diagnostics.py:139-146
const (
	defaultDiagnosticsInterval     = 60 * time.Second
	defaultDiagnosticsDeepInterval = 600 * time.Second
	defaultDiagnosticsMaxBytes     = 5 * 1024 * 1024
	defaultDiagnosticsBackups      = 3
	// diagnosticsSchema 记录格式版本，对齐 Python 的 schema=1
	diagnosticsSchema = 1
)

// MemoryDiagnosticsOptions 内存诊断选项
type MemoryDiagnosticsOptions struct {
	// Dir 诊断文件目录
	Dir string
	// Interval 采样间隔，最小 1 秒
	Interval time.Duration
	// Deep 是否采集分配位置明细
	Deep bool
	// DeepInterval 深度采样间隔，最小 60 秒
	DeepInterval time.Duration
	// MaxBytes 单文件上限
	MaxBytes int64
	// Backups 保留份数
	Backups int
	// RuntimeCounts 业务运行时计数回调，对齐 Python runtime_counts
	RuntimeCounts func() map[string]any
	// DatabaseCounts 数据库连接池计数回调，对齐 Python database_counts
	DatabaseCounts func() map[string]any
}

func (o *MemoryDiagnosticsOptions) applyDefaults() {
	if o.Interval < time.Second {
		o.Interval = defaultDiagnosticsInterval
	}
	if o.DeepInterval < 60*time.Second {
		o.DeepInterval = defaultDiagnosticsDeepInterval
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = defaultDiagnosticsMaxBytes
	}
	if o.Backups <= 0 {
		o.Backups = defaultDiagnosticsBackups
	}
}

// MemoryDiagnostics 周期性采集进程内存与运行时计数
// 对齐 Python memory_diagnostics.py：只记录计数与分配位置，不记录消息或对象内容
type MemoryDiagnostics struct {
	opts     MemoryDiagnosticsOptions
	writer   *rotatingFileWriter
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	lastDeep time.Time
}

// StartMemoryDiagnostics 启动内存诊断；Dir 为空或未启用时返回 nil
func StartMemoryDiagnostics(opts MemoryDiagnosticsOptions) (*MemoryDiagnostics, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, nil
	}
	opts.applyDefaults()
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建内存诊断目录失败: %w", err)
	}
	writer, err := newRotatingFileWriter(FileOptions{
		Dir:         opts.Dir,
		MaxBytes:    opts.MaxBytes,
		BackupCount: opts.Backups,
		CleanupDays: 30,
	})
	if err != nil {
		return nil, fmt.Errorf("打开内存诊断文件失败: %w", err)
	}
	diagnostics := &MemoryDiagnostics{
		opts:   opts,
		writer: writer,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go diagnostics.run()
	return diagnostics, nil
}

// Stop 停止诊断并关闭文件
func (d *MemoryDiagnostics) Stop() {
	if d == nil {
		return
	}
	d.stopOnce.Do(func() { close(d.stopCh) })
	<-d.doneCh
	_ = d.writer.Close()
}

func (d *MemoryDiagnostics) run() {
	defer close(d.doneCh)
	ticker := time.NewTicker(d.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.sample()
		}
	}
}

// sample 采集一次并写入 JSONL
func (d *MemoryDiagnostics) sample() {
	record := map[string]any{
		"timestamp":    time.Now().Unix(),
		"pid":          os.Getpid(),
		"schema":       diagnosticsSchema,
		"process":      processMemorySnapshot(),
		"runtime":      runtimeCountsSnapshot(),
		"deep_enabled": d.opts.Deep,
	}
	// 对齐 Python memory_diagnostics.py:81-116 与 :51-79：业务计数与连接池状态由调用方注入
	if d.opts.RuntimeCounts != nil {
		if counts := d.opts.RuntimeCounts(); len(counts) > 0 {
			record["business"] = counts
		}
	}
	if d.opts.DatabaseCounts != nil {
		if counts := d.opts.DatabaseCounts(); len(counts) > 0 {
			record["databases"] = counts
		}
	}
	if d.opts.Deep && time.Since(d.lastDeep) >= d.opts.DeepInterval {
		d.lastDeep = time.Now()
		record["allocations"] = deepAllocationSnapshot()
	}
	raw, err := json.Marshal(record)
	if err != nil {
		zap.L().Warn("内存诊断记录序列化失败", zap.Error(err))
		return
	}
	if _, err := d.writer.Write(append(raw, '\n')); err != nil {
		zap.L().Warn("内存诊断记录写入失败", zap.Error(err))
	}
}

// processMemorySnapshot 进程内存计数，对齐 Python process_memory
func processMemorySnapshot() map[string]any {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return map[string]any{
		"heap_alloc_bytes":  stats.HeapAlloc,
		"heap_sys_bytes":    stats.HeapSys,
		"heap_inuse_bytes":  stats.HeapInuse,
		"heap_idle_bytes":   stats.HeapIdle,
		"stack_inuse_bytes": stats.StackInuse,
		"sys_bytes":         stats.Sys,
		"total_alloc_bytes": stats.TotalAlloc,
		"num_gc":            stats.NumGC,
		"gc_pause_total_ns": stats.PauseTotalNs,
		"goroutines":        runtime.NumGoroutine(),
		"process_threads":   runtime.GOMAXPROCS(0),
	}
}

// runtimeCountsSnapshot 运行时计数，对齐 Python runtime_counts
func runtimeCountsSnapshot() map[string]any {
	return map[string]any{
		"goroutines": runtime.NumGoroutine(),
		"gomaxprocs": runtime.GOMAXPROCS(0),
		"num_cpu":    runtime.NumCPU(),
	}
}

// deepAllocationSnapshot 采集堆分配位置明细，对齐 Python _deep_sample
// Go 用 MemStats 的按大小类统计替代 tracemalloc 的按行统计
func deepAllocationSnapshot() map[string]any {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	type sizeClass struct {
		Class int    `json:"class"`
		Bytes uint64 `json:"bytes"`
		Count uint64 `json:"count"`
	}
	classes := make([]sizeClass, 0, len(stats.BySize))
	for index, item := range stats.BySize {
		if item.Mallocs == 0 {
			continue
		}
		classes = append(classes, sizeClass{
			Class: index,
			Bytes: item.Mallocs * uint64(item.Size),
			Count: item.Mallocs,
		})
	}
	sort.Slice(classes, func(left, right int) bool {
		return classes[left].Bytes > classes[right].Bytes
	})
	if len(classes) > 20 {
		classes = classes[:20]
	}
	return map[string]any{
		"heap_alloc_bytes": stats.HeapAlloc,
		"heap_sys_bytes":   stats.HeapSys,
		"mallocs":          stats.Mallocs,
		"frees":            stats.Frees,
		"size_classes":     classes,
	}
}

// DiagnosticsPath 返回诊断文件路径，供测试与诊断
func DiagnosticsPath(dir string) string {
	return filepath.Join(dir, "maibot.log")
}

// RunMemoryDiagnosticsOnce 立即采集一次，供测试使用
func (d *MemoryDiagnostics) RunMemoryDiagnosticsOnce() {
	if d == nil {
		return
	}
	d.sample()
}

var _ = context.Background
