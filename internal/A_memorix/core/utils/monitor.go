package utils

// A_memorix 内存监控，对齐 Python core/utils/monitor.py
// psutil 能力由 MemoryProvider/SystemMemoryProvider 接口注入

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

// MemoryProvider 进程内存读取能力，对齐 psutil.Process().memory_info().rss
type MemoryProvider interface {
	// ProcessMemoryMB 返回当前进程内存占用（MB）
	ProcessMemoryMB() float64
}

// SystemMemoryProvider 系统内存读取能力，对齐 psutil.virtual_memory()
type SystemMemoryProvider interface {
	// VirtualMemory 返回总内存、可用内存（字节）与使用百分比
	VirtualMemory() (totalBytes uint64, availableBytes uint64, percent float64, err error)
	// ProcessRSSBytes 返回当前进程常驻内存（字节）
	ProcessRSSBytes() (uint64, error)
}

// RuntimeMemoryProvider 无 psutil 时的兜底实现，使用 Go 运行时统计
type RuntimeMemoryProvider struct{}

// ProcessMemoryMB 对齐降级分支的进程内存读取
func (RuntimeMemoryProvider) ProcessMemoryMB() float64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return float64(stats.Sys) / 1024 / 1024
}

// MemoryMonitorOptions 监控器配置，对齐 MemoryMonitor.__init__ 参数
type MemoryMonitorOptions struct {
	// MaxMemoryMB 最大内存限制（MB）
	MaxMemoryMB int
	// WarningThreshold 警告阈值（0-1）
	WarningThreshold float64
	// CheckInterval 检查间隔
	CheckInterval time.Duration
	// EnableAutoGC 是否启用自动垃圾回收
	EnableAutoGC bool
	// Provider 进程内存读取，nil 时回退到 RuntimeMemoryProvider
	Provider MemoryProvider
	// Logger 日志注入
	Logger Logger
}

// DefaultMemoryMonitorOptions 对齐 Python 默认参数
func DefaultMemoryMonitorOptions(maxMemoryMB int) MemoryMonitorOptions {
	return MemoryMonitorOptions{
		MaxMemoryMB:      maxMemoryMB,
		WarningThreshold: 0.9,
		CheckInterval:    10 * time.Second,
		EnableAutoGC:     true,
	}
}

// MemoryMonitor 内存监控器
type MemoryMonitor struct {
	mu        sync.Mutex
	options   MemoryMonitorOptions
	provider  MemoryProvider
	logger    Logger
	running   bool
	callbacks []func(currentMB float64, limitMB float64)
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewMemoryMonitor 创建内存监控器
func NewMemoryMonitor(options MemoryMonitorOptions) *MemoryMonitor {
	provider := options.Provider
	if provider == nil {
		provider = RuntimeMemoryProvider{}
	}
	return &MemoryMonitor{
		options:  options,
		provider: provider,
		logger:   OrLogger(options.Logger),
	}
}

// Start 启动监控
func (m *MemoryMonitor) Start() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		m.logger.Warning("内存监控已在运行")
		return
	}
	if m.provider == nil {
		m.mu.Unlock()
		m.logger.Warning("psutil 未安装，内存监控功能不可用")
		return
	}
	m.running = true
	m.stopCh = make(chan struct{})
	m.doneCh = make(chan struct{})
	stopCh := m.stopCh
	doneCh := m.doneCh
	m.mu.Unlock()

	go func() {
		defer close(doneCh)
		m.monitorLoop(stopCh)
	}()
	m.logger.Info(fmt.Sprintf("内存监控已启动 (限制: %dMB)", m.options.MaxMemoryMB))
}

// Stop 停止监控
func (m *MemoryMonitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	stopCh := m.stopCh
	doneCh := m.doneCh
	m.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
		}
	}
	m.logger.Info("内存监控已停止")
}

// RegisterCallback 注册内存超限回调，参数为当前使用 MB 与限制 MB
func (m *MemoryMonitor) RegisterCallback(callback func(currentMB float64, limitMB float64)) {
	if callback == nil {
		return
	}
	m.mu.Lock()
	m.callbacks = append(m.callbacks, callback)
	m.mu.Unlock()
}

// IsRunning 返回当前运行状态
func (m *MemoryMonitor) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// CurrentMemoryMB 获取当前进程内存使用量（MB）
func (m *MemoryMonitor) CurrentMemoryMB() float64 {
	if m.provider == nil {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return float64(stats.Sys) / 1024 / 1024
	}
	return m.provider.ProcessMemoryMB()
}

// MemoryUsageRatio 获取内存使用率（0-1）
func (m *MemoryMonitor) MemoryUsageRatio() float64 {
	current := m.CurrentMemoryMB()
	if m.options.MaxMemoryMB > 0 {
		return current / float64(m.options.MaxMemoryMB)
	}
	return 0
}

// monitorLoop 监控循环
func (m *MemoryMonitor) monitorLoop(stopCh <-chan struct{}) {
	interval := m.options.CheckInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			m.checkOnce()
		}
	}
}

// checkOnce 执行一次检查，对齐 _monitor_loop 的单轮逻辑
func (m *MemoryMonitor) checkOnce() {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.logger.Error(fmt.Sprintf("内存监控出错: %v", recovered))
		}
	}()

	currentMB := m.CurrentMemoryMB()
	ratio := 0.0
	if m.options.MaxMemoryMB > 0 {
		ratio = currentMB / float64(m.options.MaxMemoryMB)
	}
	if ratio >= m.options.WarningThreshold {
		m.logger.Warning(fmt.Sprintf(
			"内存使用率过高: %.1fMB / %dMB (%.1f%%)",
			currentMB, m.options.MaxMemoryMB, ratio*100,
		))
		m.mu.Lock()
		callbacks := append([]func(float64, float64){}, m.callbacks...)
		m.mu.Unlock()
		for _, callback := range callbacks {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						m.logger.Error(fmt.Sprintf("内存回调执行失败: %v", recovered))
					}
				}()
				callback(currentMB, float64(m.options.MaxMemoryMB))
			}()
		}
		if m.options.EnableAutoGC {
			before := m.CurrentMemoryMB()
			runtime.GC()
			after := m.CurrentMemoryMB()
			freed := before - after
			if freed > 1 {
				m.logger.Info(fmt.Sprintf("垃圾回收释放: %.1fMB", freed))
			}
		}
		return
	}
	if m.options.EnableAutoGC && time.Now().Unix()%60 == 0 {
		runtime.GC()
	}
}

// GetMemoryInfo 获取系统内存信息，对齐 get_memory_info
func GetMemoryInfo(provider SystemMemoryProvider) map[string]any {
	if provider == nil {
		return map[string]any{"error": "psutil 未安装"}
	}
	totalBytes, availableBytes, percent, err := provider.VirtualMemory()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	processBytes, err := provider.ProcessRSSBytes()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	const bytesPerGB = 1024.0 * 1024.0 * 1024.0
	const bytesPerMB = 1024.0 * 1024.0
	processPercent := 0.0
	if totalBytes > 0 {
		processPercent = (float64(processBytes) / float64(totalBytes)) * 100
	}
	return map[string]any{
		"system_total_gb":      float64(totalBytes) / bytesPerGB,
		"system_available_gb":  float64(availableBytes) / bytesPerGB,
		"system_usage_percent": percent,
		"process_mb":           float64(processBytes) / bytesPerMB,
		"process_percent":      processPercent,
	}
}
