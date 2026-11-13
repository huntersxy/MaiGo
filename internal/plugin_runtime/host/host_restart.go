package host

import (
	"context"
	"sync"
)

// runnerOptionsMu 保护最近一次启动选项，供源码变更与配置启用时重启复用
var runnerOptionsMu sync.RWMutex

// rememberStartOptions 记录最近一次成功启动的选项
func (h *Host) rememberStartOptions(opts StartRunnerOptions) {
	if h == nil {
		return
	}
	runnerOptionsMu.Lock()
	h.lastStartOptions = opts
	h.hasStartOptions = true
	runnerOptionsMu.Unlock()
}

// LastStartOptions 返回最近一次启动选项与是否存在
func (h *Host) LastStartOptions() (StartRunnerOptions, bool) {
	if h == nil {
		return StartRunnerOptions{}, false
	}
	runnerOptionsMu.RLock()
	defer runnerOptionsMu.RUnlock()
	if !h.hasStartOptions {
		return StartRunnerOptions{}, false
	}
	return h.lastStartOptions, true
}

// RestartWithLastOptions 用最近一次启动选项重启 Runner
// 对齐 Python integration.py:2149 与 :287-289 的重启语义；未启动过时返回 false
func (h *Host) RestartWithLastOptions(ctx context.Context) (bool, error) {
	opts, ok := h.LastStartOptions()
	if !ok {
		return false, nil
	}
	if err := h.RestartRunner(ctx, opts); err != nil {
		return false, err
	}
	return true, nil
}
