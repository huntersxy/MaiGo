package host

import (
	"context"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
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
	if h.activeRPCServer() == nil {
		return AdapterTransitionResult{
			Success:          false,
			ChangedPluginIDs: []string{},
			PendingPluginIDs: pendingOfflineAdapterIDs(),
			FailedPlugins:    map[string]string{"plugin_runtime": "插件运行时尚未启动"},
		}, nil
	}
	adapterTransitionMu.Lock()
	defer adapterTransitionMu.Unlock()

	changed := make([]string, 0)
	failed := map[string]string{}
	for _, pluginID := range h.LoadedPluginIDsByType("adapter") {
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
	// 对齐 Python integration.py:1349-1352：卸载完成后按实际剩余链路清理聊天流
	closePolicyBlockedChats(ctx)
	h.notifyStatusChanges()
	sort.Strings(changed)
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
	if h.activeRPCServer() == nil {
		return AdapterTransitionResult{
			Success:          false,
			ChangedPluginIDs: []string{},
			PendingPluginIDs: pendingOfflineAdapterIDs(),
			FailedPlugins:    map[string]string{"plugin_runtime": "插件运行时尚未启动"},
		}, nil
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
	result, err := h.ReloadPlugins(ctx, pending, "local_operator_online", nil)
	if err != nil {
		zap.L().Error("适配器插件重新加载失败", zap.Strings("plugins", pending), zap.Error(err))
		return AdapterTransitionResult{
			Success:          false,
			ChangedPluginIDs: []string{},
			PendingPluginIDs: pending,
			FailedPlugins:    map[string]string{"plugin_runtime": err.Error()},
		}, nil
	}
	changed := make([]string, 0, len(result.ReloadedPlugins))
	for _, pluginID := range result.ReloadedPlugins {
		changed = append(changed, pluginID)
		clearOfflineAdapterID(pluginID)
	}
	sort.Strings(changed)
	return AdapterTransitionResult{
		Success:          len(result.FailedPlugins) == 0,
		ChangedPluginIDs: changed,
		PendingPluginIDs: pendingOfflineAdapterIDs(),
		FailedPlugins:    result.FailedPlugins,
	}, nil
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
