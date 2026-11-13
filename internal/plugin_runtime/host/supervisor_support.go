package host

import (
	"strings"

	"maibot-go/internal/plugin_runtime/protocol"
)

// ApplyRunnerReady 记录 runner.ready 上报的装载状态，并结算使用时长
func (h *Host) ApplyRunnerReady(payload protocol.RunnerReadyPayload) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.runnerReady = payload
	h.mu.Unlock()
	h.notifyStatusChanges()
}

// ApplyRunnerReadyForGroup 记录分组 runner.ready 并推进生命周期到 running
func (h *Host) ApplyRunnerReadyForGroup(payload protocol.RunnerReadyPayload, group RunnerGroup) {
	if h == nil {
		return
	}
	group = runnerGroupOrDefault(group)
	h.mu.Lock()
	h.runnerReadyByGroup[group] = payload
	h.mu.Unlock()
	h.ApplyRunnerReady(payload)
	h.markRunnerReadyForGroup(group)
}

// RunnerReadyState 返回最近一次 runner.ready 状态副本
func (h *Host) RunnerReadyState() protocol.RunnerReadyPayload {
	if h == nil {
		return protocol.RunnerReadyPayload{}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.runnerReady
}

// LoadedPluginIDs 返回已注册插件 ID，对齐 get_loaded_plugin_ids
func (h *Host) LoadedPluginIDs() []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.plugins))
	for pluginID := range h.plugins {
		out = append(out, pluginID)
	}
	return out
}

// LoadedPluginIDsByType 按插件类型过滤，对齐 get_loaded_plugin_ids_by_type
func (h *Host) LoadedPluginIDsByType(pluginType string) []string {
	if h == nil {
		return nil
	}
	pluginType = strings.TrimSpace(pluginType)
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0)
	for pluginID, rec := range h.plugins {
		if pluginType == "" || rec.PluginType == pluginType {
			out = append(out, pluginID)
		}
	}
	return out
}

// LoadedPluginVersions 返回插件 ID → 版本
func (h *Host) LoadedPluginVersions() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]string, len(h.plugins))
	for pluginID, rec := range h.plugins {
		out[pluginID] = rec.PluginVersion
	}
	return out
}

// PluginConfigError 返回插件配置解析错误
func (h *Host) PluginConfigError(pluginID string) string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.plugins[strings.TrimSpace(pluginID)].ConfigError
}

// GetConfigReloadSubscribers 返回订阅指定全局配置广播的插件，scope 仅 bot/model
func (h *Host) GetConfigReloadSubscribers(scope string) []string {
	if h == nil {
		return nil
	}
	scope = strings.TrimSpace(scope)
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0)
	for pluginID, rec := range h.plugins {
		for _, subscribed := range rec.ConfigReloadSubscriptions {
			if subscribed == scope {
				out = append(out, pluginID)
				break
			}
		}
	}
	return out
}

// SetBlockedPluginReasons 记录依赖阻塞原因，对齐 set_blocked_plugin_reasons
func (h *Host) SetBlockedPluginReasons(reasons map[string]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.blockedReasons = make(map[string]string, len(reasons))
	for pluginID, reason := range reasons {
		h.blockedReasons[pluginID] = reason
	}
	h.mu.Unlock()
	h.notifyStatusChanges()
}

// BlockedPluginReasons 返回依赖阻塞原因副本
func (h *Host) BlockedPluginReasons() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]string, len(h.blockedReasons))
	for pluginID, reason := range h.blockedReasons {
		out[pluginID] = reason
	}
	return out
}

// PluginLoadStatuses 返回 plugin_id → loaded/blocked/config_error
func (h *Host) PluginLoadStatuses() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]string, len(h.plugins))
	for pluginID, rec := range h.plugins {
		status := "loaded"
		if rec.ConfigError != "" {
			status = "config_error"
		}
		if _, blocked := h.blockedReasons[pluginID]; blocked {
			status = "blocked"
		}
		for _, failed := range h.runnerReady.FailedPlugins {
			if failed == pluginID {
				status = "failed"
				break
			}
		}
		out[pluginID] = status
	}
	return out
}

// PluginLoadFailureReasons 返回装载失败原因
func (h *Host) PluginLoadFailureReasons() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]string, len(h.runnerReady.FailedPluginReasons)+len(h.blockedReasons))
	for pluginID, reason := range h.runnerReady.FailedPluginReasons {
		out[pluginID] = reason
	}
	for pluginID, reason := range h.blockedReasons {
		out[pluginID] = reason
	}
	return out
}

// SetLoading 标记运行时是否正在装载插件
func (h *Host) SetLoading(loading bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.loading = loading
	h.mu.Unlock()
}

// IsLoading 返回是否正在装载
func (h *Host) IsLoading() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.loading
}

// AttachUsageTracker 绑定插件使用时长记录器
func (h *Host) AttachUsageTracker(tracker *PluginUsageTracker) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.usage = tracker
	h.mu.Unlock()
}

// UsageSnapshot 返回插件累计使用时长
func (h *Host) UsageSnapshot() map[string]float64 {
	if h == nil {
		return nil
	}
	h.ReconcileUsage()
	h.mu.RLock()
	tracker := h.usage
	h.mu.RUnlock()
	if tracker == nil {
		return map[string]float64{}
	}
	return tracker.Snapshot()
}

// ReconcileUsage 按当前真实运行且无配置错误的插件结算使用时长
func (h *Host) ReconcileUsage() {
	if h == nil {
		return
	}
	h.mu.RLock()
	tracker := h.usage
	tracking := h.usageTracking
	active := h.usageActivePluginIDsLocked()
	h.mu.RUnlock()
	if tracker == nil {
		return
	}
	if !tracking {
		active = nil
	}
	tracker.Reconcile(active)
}

// usageActivePluginIDsLocked 返回 Runner 已上报加载、未阻塞且无配置错误的插件；调用方需持有 h.mu
func (h *Host) usageActivePluginIDsLocked() []string {
	active := make(map[string]struct{})
	for group, state := range h.runners {
		if state == nil || !state.Started {
			continue
		}
		ready, ok := h.runnerReadyByGroup[group]
		if !ok {
			continue
		}
		for _, pluginID := range ready.LoadedPlugins {
			pluginID = strings.TrimSpace(pluginID)
			if pluginID == "" {
				continue
			}
			record, registered := h.plugins[pluginID]
			if !registered || record.ConfigError != "" {
				continue
			}
			if _, blocked := h.blockedReasons[pluginID]; blocked {
				continue
			}
			active[pluginID] = struct{}{}
		}
	}
	out := make([]string, 0, len(active))
	for pluginID := range active {
		out = append(out, pluginID)
	}
	return out
}

// SaveUsage 持久化使用时长快照
func (h *Host) SaveUsage() error {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	tracker := h.usage
	h.mu.RUnlock()
	if tracker == nil {
		return nil
	}
	return tracker.Save()
}
