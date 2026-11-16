package host

import (
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/plugin_runtime/protocol"
)

// ApplyRunnerReady 记录 runner.ready 上报的装载状态，并结算使用时长
func (h *Host) ApplyRunnerReady(payload protocol.RunnerReadyPayload) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.runnerReady = payload
	h.runnerReadySet = true
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
	h.startupLoadResult[group] = payload
	delete(h.startupPending, group)
	delete(h.startupStartedAt, group)
	h.mu.Unlock()
	h.ApplyRunnerReady(payload)
	h.markRunnerReadyForGroup(group)
}

// ApplyRunnerStartupProgressForGroup 记录整轮完成前的逐插件进度
// 最终 ready 之后或本组已失败时不接受迟到的进度覆盖
func (h *Host) ApplyRunnerStartupProgressForGroup(payload protocol.RunnerStartupProgressPayload, group RunnerGroup) bool {
	if h == nil {
		return false
	}
	group = runnerGroupOrDefault(group)
	h.mu.Lock()
	_, ready := h.runnerReadyByGroup[group]
	accepted := !h.shutdownRequested.Load() && !ready && h.startupFailure[group] == ""
	if accepted {
		h.startupLoadResult[group] = payload.ReadyPayload()
		pending := make(map[string]struct{}, len(payload.LoadingPlugins))
		for _, pluginID := range payload.LoadingPlugins {
			pending[pluginID] = struct{}{}
		}
		h.startupPending[group] = pending
	}
	h.mu.Unlock()
	if accepted {
		h.notifyStatusChanges()
	}
	return accepted
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

// LoadedPluginVersions 返回已完成初始化的可用插件版本映射
func (h *Host) LoadedPluginVersions() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	loaded := make(map[string]struct{}, len(h.plugins))
	for _, result := range h.startupLoadResult {
		for _, pluginID := range result.LoadedPlugins {
			loaded[pluginID] = struct{}{}
		}
	}
	out := make(map[string]string, len(h.plugins))
	for pluginID, rec := range h.plugins {
		if _, ok := loaded[pluginID]; ok && rec.ConfigError == "" {
			out[pluginID] = rec.PluginVersion
		}
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

// SetAdapterMigrationErrors 记录适配器迁移失败原因，只隔离相关插件
func (h *Host) SetAdapterMigrationErrors(reasons map[string]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.adapterMigrationErrors = make(map[string]string, len(reasons))
	for pluginID, reason := range reasons {
		h.adapterMigrationErrors[pluginID] = reason
	}
	h.mu.Unlock()
}

// adapterMigrationReasons 返回迁移失败原因副本
func (h *Host) adapterMigrationReasons() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]string, len(h.adapterMigrationErrors))
	for pluginID, reason := range h.adapterMigrationErrors {
		out[pluginID] = reason
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

// PluginLoadStatuses 返回 plugin_id → success/failed/inactive/loading/timed_out
func (h *Host) PluginLoadStatuses() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pluginLoadStatusesLocked()
}

func (h *Host) pluginLoadStatusesLocked() map[string]string {
	out := make(map[string]string, len(h.plugins))
	for _, result := range h.startupLoadResult {
		for _, pluginID := range result.LoadedPlugins {
			out[pluginID] = "success"
		}
		for _, pluginID := range result.FailedPlugins {
			out[pluginID] = "failed"
		}
		for _, pluginID := range result.InactivePlugins {
			if _, exists := out[pluginID]; !exists {
				out[pluginID] = "inactive"
			}
		}
	}
	for pluginID := range h.plugins {
		if _, exists := out[pluginID]; !exists {
			out[pluginID] = "loading"
		}
	}
	for _, pending := range h.startupPending {
		for pluginID := range pending {
			out[pluginID] = "loading"
		}
	}
	if h.loading {
		loadingStatus := h.startupLoadStatusLocked()
		for pluginID, status := range out {
			if status == "loading" {
				out[pluginID] = loadingStatus
			}
		}
	}
	for pluginID, rec := range h.plugins {
		if rec.ConfigError != "" {
			out[pluginID] = "failed"
		}
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
	out := make(map[string]string, len(h.plugins))
	for _, result := range h.startupLoadResult {
		for pluginID, reason := range result.FailedPluginReasons {
			if strings.TrimSpace(pluginID) != "" && strings.TrimSpace(reason) != "" {
				out[pluginID] = reason
			}
		}
	}
	for pluginID, rec := range h.plugins {
		if rec.ConfigError != "" {
			out[pluginID] = rec.ConfigError
		}
	}
	for pluginID, status := range h.pluginLoadStatusesLocked() {
		if status == "timed_out" {
			out[pluginID] = h.startupLoadReasonLocked()
		}
	}
	return out
}

// StartupLoadStatus 为尚未进入注册表的插件提供本组启动状态
func (h *Host) StartupLoadStatus() string {
	if h == nil {
		return "not_loaded"
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.startupLoadStatusLocked()
}

func (h *Host) startupLoadStatusLocked() string {
	if h.startupFailureReasonLocked() != "" {
		return "failed"
	}
	if !h.loading {
		return "not_loaded"
	}
	if h.startupTimedOutLocked() {
		return "timed_out"
	}
	return "loading"
}

// StartupLoadReason 返回本组启动失败或持续等待的具体原因
func (h *Host) StartupLoadReason() string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.startupLoadReasonLocked()
}

func (h *Host) startupFailureReasonLocked() string {
	for _, reason := range h.startupFailure {
		if reason != "" {
			return reason
		}
	}
	return ""
}

func (h *Host) startupTimeoutValue() time.Duration {
	if h != nil && h.runnerSpawnTimeout > 0 {
		return h.runnerSpawnTimeout
	}
	return DefaultRunnerSpawnWait
}

func (h *Host) startupTimedOutLocked() bool {
	timeout := h.startupTimeoutValue()
	for _, startedAt := range h.startupStartedAt {
		if !startedAt.IsZero() && time.Since(startedAt) >= timeout {
			return true
		}
	}
	return false
}

func (h *Host) startupLoadReasonLocked() string {
	if reason := h.startupFailureReasonLocked(); reason != "" {
		return reason
	}
	if !h.startupTimedOutLocked() {
		return ""
	}
	timeout := h.startupTimeoutValue()
	elapsed := 0.0
	for _, startedAt := range h.startupStartedAt {
		if startedAt.IsZero() {
			continue
		}
		if value := time.Since(startedAt).Seconds(); value > elapsed {
			elapsed = value
		}
	}
	return fmt.Sprintf(
		"本组插件加载已等待 %.1f 秒，超过 %g 秒的等待阈值；尚未完成的插件仍在后台加载，Runner 未被停止。",
		elapsed, timeout.Seconds(),
	)
}

// recordStartupFailure 记录本组启动失败原因，对齐 _startup_failure_reason
func (h *Host) recordStartupFailure(group RunnerGroup, reason string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.startupFailure[group] = reason
	h.mu.Unlock()
	h.notifyStatusChanges()
}

// markStartupWait 记录本轮开始等待的时刻；启动状态按等待时长派生
func (h *Host) markStartupWait(group RunnerGroup) {
	if h == nil {
		return
	}
	group = runnerGroupOrDefault(group)
	h.mu.Lock()
	if h.startupStartedAt[group].IsZero() {
		h.startupStartedAt[group] = time.Now()
	}
	h.mu.Unlock()
	h.notifyStatusChanges()
}

// clearStartupState 清理本组启动进度与原因，对齐 _clear_runner_state
func (h *Host) clearStartupState(group RunnerGroup) {
	if h == nil {
		return
	}
	group = runnerGroupOrDefault(group)
	h.mu.Lock()
	delete(h.startupLoadResult, group)
	delete(h.startupPending, group)
	delete(h.startupStartedAt, group)
	h.startupFailure[group] = ""
	delete(h.runnerReadyByGroup, group)
	h.runnerReadySet = len(h.runnerReadyByGroup) > 0
	h.mu.Unlock()
	if h.statusBadges != nil {
		h.statusBadges.Clear()
	}
	h.notifyStatusChanges()
}

// clearStartupTimeout 本轮 ready 到达后清除等待起点
func (h *Host) clearStartupTimeout(group RunnerGroup) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.startupStartedAt, runnerGroupOrDefault(group))
	h.mu.Unlock()
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
