package host

import (
	"strings"
	"time"
)

// DefaultHookBlockingTimeout Hook 阻塞处理的全局默认超时，对齐 hook_dispatcher 的 60s
const DefaultHookBlockingTimeout = 60 * time.Second

// RuntimeSettings 插件运行时可配置项，字段语义对齐 MaiBot PluginRuntimeConfig
type RuntimeSettings struct {
	HealthCheckInterval time.Duration
	MaxRestartAttempts  int
	RunnerSpawnTimeout  time.Duration
	HookBlockingTimeout time.Duration
	IPCSocketPath       string
}

// DefaultRuntimeSettings 返回 MaiBot 的默认运行时配置
func DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{
		HealthCheckInterval: DefaultRunnerHealthInterval,
		MaxRestartAttempts:  DefaultMaxRestartAttempt,
		RunnerSpawnTimeout:  DefaultRunnerSpawnWait,
		HookBlockingTimeout: DefaultHookBlockingTimeout,
		IPCSocketPath:       "",
	}
}

// RuntimeSettingsFromConfig 把实例配置中的秒值转换为运行时设置
//
// 非正数时长回退 MaiBot 默认值；max_restart_attempts 为 0 表示禁用自动重启，负值归零
func RuntimeSettingsFromConfig(healthCheckIntervalSec float64, maxRestartAttempts int, runnerSpawnTimeoutSec float64, hookBlockingTimeoutSec float64, ipcSocketPath string) RuntimeSettings {
	settings := DefaultRuntimeSettings()
	if healthCheckIntervalSec > 0 {
		settings.HealthCheckInterval = secondsDuration(healthCheckIntervalSec)
	}
	if runnerSpawnTimeoutSec > 0 {
		settings.RunnerSpawnTimeout = secondsDuration(runnerSpawnTimeoutSec)
	}
	if hookBlockingTimeoutSec > 0 {
		settings.HookBlockingTimeout = secondsDuration(hookBlockingTimeoutSec)
	}
	if maxRestartAttempts < 0 {
		maxRestartAttempts = 0
	}
	settings.MaxRestartAttempts = maxRestartAttempts
	settings.IPCSocketPath = strings.TrimSpace(ipcSocketPath)
	return settings
}

func secondsDuration(sec float64) time.Duration {
	return time.Duration(sec * float64(time.Second))
}

// ApplyRuntimeSettings 应用运行时配置快照
//
// 健康检查、拉起超时、Hook 默认超时与 IPC 路径在启动时生效；Hook 超时还可通过
// SetHookBlockingTimeoutGetter 接入热配置读取
func (h *Host) ApplyRuntimeSettings(settings RuntimeSettings) {
	if h == nil {
		return
	}
	h.SetRunnerHealthInterval(settings.HealthCheckInterval)
	h.SetRunnerSpawnTimeout(settings.RunnerSpawnTimeout)
	h.SetHookBlockingTimeout(settings.HookBlockingTimeout)
	h.SetMaxRestartAttempts(settings.MaxRestartAttempts)
	h.SetIPCSocketPath(settings.IPCSocketPath)
}

// RuntimeSettings 返回当前运行时配置快照
func (h *Host) RuntimeSettings() RuntimeSettings {
	if h == nil {
		return DefaultRuntimeSettings()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return RuntimeSettings{
		HealthCheckInterval: h.runnerHealthIntervalValueLocked(),
		MaxRestartAttempts:  h.maxRestartAttempts,
		RunnerSpawnTimeout:  h.runnerSpawnTimeoutValueLocked(),
		HookBlockingTimeout: h.hookBlockingTimeout,
		IPCSocketPath:       h.ipcSocketPath,
	}
}

// SetRunnerHealthInterval 设置健康巡检间隔，非正数回退默认 30s
func (h *Host) SetRunnerHealthInterval(interval time.Duration) {
	if h == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultRunnerHealthInterval
	}
	h.mu.Lock()
	h.runnerHealthInterval = interval
	h.mu.Unlock()
}

// RunnerHealthInterval 返回健康巡检间隔
func (h *Host) RunnerHealthInterval() time.Duration {
	if h == nil {
		return DefaultRunnerHealthInterval
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.runnerHealthIntervalValueLocked()
}

func (h *Host) runnerHealthIntervalValueLocked() time.Duration {
	if h.runnerHealthInterval <= 0 {
		return DefaultRunnerHealthInterval
	}
	return h.runnerHealthInterval
}

// SetRunnerSpawnTimeout 设置等待 Runner 就绪超时，非正数回退默认 30s
func (h *Host) SetRunnerSpawnTimeout(timeout time.Duration) {
	if h == nil {
		return
	}
	if timeout <= 0 {
		timeout = DefaultRunnerSpawnWait
	}
	h.mu.Lock()
	h.runnerSpawnTimeout = timeout
	h.mu.Unlock()
}

// RunnerSpawnTimeout 返回等待 Runner 就绪超时
func (h *Host) RunnerSpawnTimeout() time.Duration {
	if h == nil {
		return DefaultRunnerSpawnWait
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.runnerSpawnTimeoutValueLocked()
}

func (h *Host) runnerSpawnTimeoutValueLocked() time.Duration {
	if h.runnerSpawnTimeout <= 0 {
		return DefaultRunnerSpawnWait
	}
	return h.runnerSpawnTimeout
}

// SetHookBlockingTimeout 设置 Hook 默认阻塞超时，非正数回退默认 60s
func (h *Host) SetHookBlockingTimeout(timeout time.Duration) {
	if h == nil {
		return
	}
	if timeout <= 0 {
		timeout = DefaultHookBlockingTimeout
	}
	h.mu.Lock()
	h.hookBlockingTimeout = timeout
	h.mu.Unlock()
	if h.hooks != nil {
		h.hooks.SetDefaultTimeoutMS(timeout.Milliseconds())
	}
}

// HookBlockingTimeout 返回 Hook 默认阻塞超时快照
func (h *Host) HookBlockingTimeout() time.Duration {
	if h == nil {
		return DefaultHookBlockingTimeout
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.hookBlockingTimeout <= 0 {
		return DefaultHookBlockingTimeout
	}
	return h.hookBlockingTimeout
}

// HookBlockingTimeoutMS 返回当前生效的 Hook 阻塞超时毫秒数
func (h *Host) HookBlockingTimeoutMS() int64 {
	if h != nil && h.hooks != nil {
		return h.hooks.DefaultTimeoutMS()
	}
	return h.HookBlockingTimeout().Milliseconds()
}

// SetHookBlockingTimeoutGetter 设置每次 Hook 分发时读取的超时来源，对齐 Python 的每调用读取
//
// get 返回秒数；非正数回退 SetHookBlockingTimeout 的默认值
func (h *Host) SetHookBlockingTimeoutGetter(get func() float64) {
	if h == nil || h.hooks == nil {
		return
	}
	if get == nil {
		h.hooks.SetDefaultTimeoutGetter(nil)
		return
	}
	h.hooks.SetDefaultTimeoutGetter(func() int64 {
		sec := get()
		if sec <= 0 {
			return 0
		}
		ms := int64(sec * 1000)
		if ms < 1 {
			ms = 1
		}
		return ms
	})
}

// SetIPCSocketPath 设置 IPC 套接字基础路径，空串回退自动分配
func (h *Host) SetIPCSocketPath(path string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.ipcSocketPath = strings.TrimSpace(path)
	h.mu.Unlock()
}

// IPCSocketPath 返回 IPC 套接字基础路径
func (h *Host) IPCSocketPath() string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ipcSocketPath
}

// configuredRunnerIPCAddress 按分组拼接 IPC 地址，对齐 integration.py 的 -builtin / -third_party 后缀
func (h *Host) configuredRunnerIPCAddress(group RunnerGroup) string {
	if h == nil {
		return ""
	}
	group = runnerGroupOrDefault(group)
	h.mu.RLock()
	base := strings.TrimSpace(h.ipcSocketPath)
	h.mu.RUnlock()
	if base == "" {
		return ""
	}
	return base + "-" + string(group)
}
