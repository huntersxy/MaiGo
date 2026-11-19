package host

import (
	"context"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
)

// adapterTransitionMu 串行化适配器上下线，对齐 Python _adapter_transition_lock
var adapterTransitionMu sync.Mutex

// offlineAdapterPluginIDs 记录被 take_adapters_offline 卸载的适配器插件
var (
	offlineAdapterMu        sync.Mutex
	offlineAdapterPluginIDs = map[string]struct{}{}
)

// AdapterTransitionResult 适配器上下线结果，对齐 Python AdapterRuntimeTransitionResult
type AdapterTransitionResult struct {
	Success          bool
	ChangedPluginIDs []string
	PendingPluginIDs []string
	FailedPlugins    map[string]string
}

// adapterPolicyBlockedCloser 卸载适配器后停止失去发送链路的聊天流
// 对齐 Python integration.py:1349-1352 的 heartflow_manager.close_policy_blocked_chats
var (
	adapterPolicyBlockedMu     sync.RWMutex
	adapterPolicyBlockedCloser func(ctx context.Context) error
)

// SetAdapterPolicyBlockedCloser 注入策略阻塞聊天流清理回调
func SetAdapterPolicyBlockedCloser(closer func(ctx context.Context) error) {
	adapterPolicyBlockedMu.Lock()
	adapterPolicyBlockedCloser = closer
	adapterPolicyBlockedMu.Unlock()
}

// closePolicyBlockedChats 调用注入的清理回调
func closePolicyBlockedChats(ctx context.Context) {
	adapterPolicyBlockedMu.RLock()
	closer := adapterPolicyBlockedCloser
	adapterPolicyBlockedMu.RUnlock()
	if closer == nil {
		return
	}
	if err := closer(ctx); err != nil {
		zap.L().Warn("适配器下线后清理策略阻塞聊天流失败", zap.Error(err))
	}
}

// TakeAdaptersOffline 卸载所有适配器插件并记录可恢复集合
// 对齐 Python integration.py:1319-1358 take_adapters_offline
func (h *Host) TakeAdaptersOffline(ctx context.Context) (AdapterTransitionResult, error) {
	if h == nil {
		return AdapterTransitionResult{}, nil
	}
	adapterTransitionMu.Lock()
	defer adapterTransitionMu.Unlock()

	changed := make([]string, 0)
	failed := map[string]string{}

	// 独立枚举原生：不靠 LoadedPluginIDsByType；完整 NativeTeardown
	for _, pluginID := range platformio.DefaultNativeRuntime().ListRegistered() {
		if !platformio.DefaultNativeRuntime().IsStarted(pluginID) && len(h.gatewayStatesFor(pluginID)) == 0 {
			continue
		}
		if err := h.NativeTeardown(ctx, pluginID); err != nil {
			failed[pluginID] = err.Error()
			continue
		}
		changed = append(changed, pluginID)
		recordOfflineAdapterID(pluginID)
	}
	h.SyncPluginDependencies(ctx, h.dependencyRunner, h.PluginDirs(), nil)

	if h.activeRPCServer() == nil {
		sort.Strings(changed)
		closePolicyBlockedChats(ctx)
		h.notifyStatusChanges()
		if len(failed) == 0 && len(changed) == 0 {
			failed["plugin_runtime"] = "插件运行时尚未启动"
		}
		return AdapterTransitionResult{
			Success:          len(failed) == 0,
			ChangedPluginIDs: changed,
			PendingPluginIDs: pendingOfflineAdapterIDs(),
			FailedPlugins:    failed,
		}, nil
	}

	for _, pluginID := range h.LoadedPluginIDsByType("adapter") {
		if IsNativeAdapter(pluginID) {
			continue
		}
		result, err := h.UnloadPlugins(ctx, []string{pluginID}, "local_operator_offline")
		if err != nil {
			failed[pluginID] = err.Error()
			continue
		}
		for _, unloaded := range result.UnloadedPlugins {
			changed = append(changed, unloaded)
			recordOfflineAdapterID(unloaded)
		}
		for failedID, reason := range result.FailedPlugins {
			failed[failedID] = reason
		}
	}
	sort.Strings(changed)
	closePolicyBlockedChats(ctx)
	h.notifyStatusChanges()
	return AdapterTransitionResult{
		Success:          len(failed) == 0,
		ChangedPluginIDs: changed,
		PendingPluginIDs: pendingOfflineAdapterIDs(),
		FailedPlugins:    failed,
	}, nil
}

// BringAdaptersOnline 重新加载由 TakeAdaptersOffline 卸载的适配器插件
// 对齐 Python integration.py:1364-1400 bring_adapters_online
func (h *Host) BringAdaptersOnline(ctx context.Context) (AdapterTransitionResult, error) {
	if h == nil {
		return AdapterTransitionResult{}, nil
	}
	adapterTransitionMu.Lock()
	defer adapterTransitionMu.Unlock()

	pending := pendingOfflineAdapterIDs()
	if len(pending) == 0 {
		return AdapterTransitionResult{
			Success:          true,
			ChangedPluginIDs: []string{},
			PendingPluginIDs: []string{},
			FailedPlugins:    map[string]string{},
		}, nil
	}

	changed := make([]string, 0)
	failed := map[string]string{}
	pythonPending := make([]string, 0)

	for _, pluginID := range pending {
		if IsNativeAdapter(pluginID) || platformio.DefaultNativeRuntime().IsRegistered(pluginID) {
			path, err := h.resolveNativePluginConfigPath(pluginID)
			if err != nil {
				failed[pluginID] = err.Error()
				continue
			}
			enabled, err := readNativePluginEnabled(path)
			if err != nil {
				failed[pluginID] = err.Error()
				continue
			}
			if !enabled {
				clearOfflineAdapterID(pluginID)
				continue
			}
			if err := h.StartNativeRuntimeIfEnabled(ctx, pluginID); err != nil {
				failed[pluginID] = err.Error()
				continue
			}
			h.SetNativeInProcessReason(pluginID, "已由进程内 Go 适配器承接")
			changed = append(changed, pluginID)
			clearOfflineAdapterID(pluginID)
			continue
		}
		pythonPending = append(pythonPending, pluginID)
	}
	h.SyncPluginDependencies(ctx, h.dependencyRunner, h.PluginDirs(), nil)

	if len(pythonPending) > 0 {
		if h.activeRPCServer() == nil {
			failed["plugin_runtime"] = "插件运行时尚未启动"
		} else {
			result, err := h.ReloadPlugins(ctx, pythonPending, "local_operator_online", nil)
			if err != nil {
				zap.L().Error("适配器插件重新加载失败", zap.Strings("plugins", pythonPending), zap.Error(err))
				failed["plugin_runtime"] = err.Error()
			} else {
				for _, pluginID := range result.ReloadedPlugins {
					changed = append(changed, pluginID)
					clearOfflineAdapterID(pluginID)
				}
				for failedID, reason := range result.FailedPlugins {
					failed[failedID] = reason
				}
			}
		}
	}
	sort.Strings(changed)
	return AdapterTransitionResult{
		Success:          len(failed) == 0,
		ChangedPluginIDs: changed,
		PendingPluginIDs: pendingOfflineAdapterIDs(),
		FailedPlugins:    failed,
	}, nil
}

func (h *Host) gatewayStatesFor(pluginID string) map[string]MessageGatewayRuntimeState {
	if h == nil {
		return nil
	}
	h.gatewayStateMu.RLock()
	defer h.gatewayStateMu.RUnlock()
	states := h.gatewayStates[pluginID]
	if len(states) == 0 {
		return nil
	}
	out := make(map[string]MessageGatewayRuntimeState, len(states))
	for name, state := range states {
		out[name] = state
	}
	return out
}

// recordOfflineAdapterID 记录已卸载的适配器插件
func recordOfflineAdapterID(pluginID string) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return
	}
	offlineAdapterMu.Lock()
	offlineAdapterPluginIDs[pluginID] = struct{}{}
	offlineAdapterMu.Unlock()
}

// clearOfflineAdapterID 清除已恢复的适配器插件
func clearOfflineAdapterID(pluginID string) {
	offlineAdapterMu.Lock()
	delete(offlineAdapterPluginIDs, pluginID)
	offlineAdapterMu.Unlock()
}

// pendingOfflineAdapterIDs 返回待恢复的适配器插件 ID
func pendingOfflineAdapterIDs() []string {
	offlineAdapterMu.Lock()
	defer offlineAdapterMu.Unlock()
	ids := make([]string, 0, len(offlineAdapterPluginIDs))
	for pluginID := range offlineAdapterPluginIDs {
		ids = append(ids, pluginID)
	}
	sort.Strings(ids)
	return ids
}
