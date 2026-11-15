package host

import (
	"context"
	"errors"
	"sort"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/protocol"
)

// RunnerRPCError 保留 Runner RPC 错误码，供恢复预算判断
type RunnerRPCError struct {
	Code    string
	Message string
}

func (e *RunnerRPCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// TrackPluginRecoveryTargets 记录本次重启周期需要恢复的插件
// 对齐 supervisor._restart_runner 的 recovery targets：已注册插件与 Runner 已加载插件的交集
func (h *Host) TrackPluginRecoveryTargets(loaded []string) {
	if h == nil {
		return
	}
	loadedSet := make(map[string]struct{}, len(loaded))
	for _, pluginID := range loaded {
		pluginID = strings.TrimSpace(pluginID)
		if pluginID != "" {
			loadedSet[pluginID] = struct{}{}
		}
	}
	h.mu.RLock()
	registered := make(map[string]struct{}, len(h.plugins))
	for pluginID := range h.plugins {
		registered[pluginID] = struct{}{}
	}
	h.mu.RUnlock()
	targets := make(map[string]struct{})
	for pluginID := range loadedSet {
		if _, ok := registered[pluginID]; ok {
			targets[pluginID] = struct{}{}
		}
	}
	h.recoveryMu.Lock()
	h.recoveryTargets = targets
	h.recoveryMu.Unlock()
}

// ValidatePluginRecovery 核对 Runner 就绪结果与恢复目标，返回仍未恢复的插件
// 对齐 supervisor._validate_plugin_recovery：主动禁用的插件不再自动恢复
func (h *Host) ValidatePluginRecovery(payload protocol.RunnerReadyPayload) []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	registered := make(map[string]struct{}, len(h.plugins))
	for pluginID := range h.plugins {
		registered[pluginID] = struct{}{}
	}
	h.mu.RUnlock()
	loadedSet := make(map[string]struct{}, len(payload.LoadedPlugins))
	for _, pluginID := range payload.LoadedPlugins {
		pluginID = strings.TrimSpace(pluginID)
		if pluginID == "" {
			continue
		}
		if _, ok := registered[pluginID]; ok {
			loadedSet[pluginID] = struct{}{}
		}
	}
	explicitlyDisabled := make(map[string]struct{}, len(payload.ExplicitlyDisabledPlugins))
	for _, pluginID := range payload.ExplicitlyDisabledPlugins {
		explicitlyDisabled[strings.TrimSpace(pluginID)] = struct{}{}
	}
	h.recoveryMu.Lock()
	for pluginID := range explicitlyDisabled {
		delete(h.recoveryTargets, pluginID)
		delete(h.recoveryAttempts, pluginID)
	}
	missing := make([]string, 0)
	for pluginID := range h.recoveryTargets {
		if _, ok := loadedSet[pluginID]; ok {
			continue
		}
		if _, disabled := explicitlyDisabled[pluginID]; disabled {
			continue
		}
		missing = append(missing, pluginID)
	}
	h.recoveryMu.Unlock()
	sort.Strings(missing)
	return missing
}

// RecoverMissingPlugins 按插件分别计数恢复缺失插件，达到上限后停止重试该插件
// 对齐 supervisor._recover_missing_plugins；E_RELOAD_IN_PROGRESS 不消耗恢复预算
func (h *Host) RecoverMissingPlugins(ctx context.Context, loaded []string) {
	if h == nil {
		return
	}
	payload := protocol.RunnerReadyPayload{LoadedPlugins: loaded}
	if ready, ok := h.runnerReadySnapshot(); ok {
		payload.ExplicitlyDisabledPlugins = ready.ExplicitlyDisabledPlugins
	}
	missing := h.ValidatePluginRecovery(payload)
	if len(missing) == 0 {
		return
	}
	maxAttempts := h.recoveryMaxAttempts()
	h.recoveryMu.Lock()
	missingSet := make(map[string]struct{}, len(missing))
	for _, pluginID := range missing {
		missingSet[pluginID] = struct{}{}
	}
	for pluginID := range h.recoveryTargets {
		if _, ok := missingSet[pluginID]; !ok {
			delete(h.recoveryTargets, pluginID)
			delete(h.recoveryAttempts, pluginID)
		}
	}
	retry := make([]string, 0, len(missing))
	for _, pluginID := range missing {
		if h.recoveryAttempts[pluginID] >= maxAttempts {
			zap.L().Error("插件自动恢复次数已达上限，停止重试；其他插件继续运行",
				zap.String("plugin_id", pluginID),
				zap.Int("attempts", h.recoveryAttempts[pluginID]),
				zap.Int("max_attempts", maxAttempts),
			)
			continue
		}
		h.recoveryAttempts[pluginID]++
		retry = append(retry, pluginID)
	}
	h.recoveryMu.Unlock()
	if len(retry) == 0 {
		return
	}
	result, err := h.RecoverPlugins(ctx, retry, "automatic_recovery", nil)
	if err != nil {
		if isReloadInProgressError(err) {
			h.recoveryMu.Lock()
			for _, pluginID := range retry {
				if h.recoveryAttempts[pluginID] > 0 {
					h.recoveryAttempts[pluginID]--
				}
			}
			h.recoveryMu.Unlock()
			zap.L().Info("插件自动恢复被已有重载任务拒绝，不消耗恢复预算",
				zap.Strings("plugins", retry),
			)
			return
		}
		zap.L().Error("插件自动恢复请求失败", zap.Strings("plugins", retry), zap.Error(err))
		return
	}
	h.applyPluginRecoveryResult(result)
	remaining := h.ValidatePluginRecovery(protocol.RunnerReadyPayload{
		LoadedPlugins:             h.runnerReadyLoadedPlugins(),
		ExplicitlyDisabledPlugins: payload.ExplicitlyDisabledPlugins,
	})
	remainingSet := make(map[string]struct{}, len(remaining))
	for _, pluginID := range remaining {
		remainingSet[pluginID] = struct{}{}
	}
	h.recoveryMu.Lock()
	for pluginID := range h.recoveryTargets {
		if _, ok := remainingSet[pluginID]; !ok {
			delete(h.recoveryTargets, pluginID)
			delete(h.recoveryAttempts, pluginID)
		}
	}
	h.recoveryMu.Unlock()
}

func (h *Host) applyPluginRecoveryResult(result ReloadPluginsResult) {
	h.recoveryMu.Lock()
	defer h.recoveryMu.Unlock()
	for _, pluginID := range result.ReloadedPlugins {
		delete(h.recoveryTargets, pluginID)
		delete(h.recoveryAttempts, pluginID)
	}
	for _, pluginID := range result.ExplicitlyDisabledPlugins {
		delete(h.recoveryTargets, pluginID)
		delete(h.recoveryAttempts, pluginID)
	}
}

func (h *Host) recoveryMaxAttempts() int {
	if h == nil {
		return DefaultMaxRestartAttempt
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.maxRestartAttempts < 0 {
		return 0
	}
	return h.maxRestartAttempts
}

func (h *Host) runnerReadySnapshot() (protocol.RunnerReadyPayload, bool) {
	if h == nil {
		return protocol.RunnerReadyPayload{}, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.runnerReady, true
}

func (h *Host) runnerReadyLoadedPlugins() []string {
	ready, _ := h.runnerReadySnapshot()
	return ready.LoadedPlugins
}

func (h *Host) dropPluginRecoveryTarget(pluginID string) {
	if h == nil {
		return
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return
	}
	h.recoveryMu.Lock()
	delete(h.recoveryTargets, pluginID)
	delete(h.recoveryAttempts, pluginID)
	h.recoveryMu.Unlock()
}

func isReloadInProgressError(err error) bool {
	var rpcErr *RunnerRPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == string(protocol.ErrorCodeReloadInProgress)
	}
	return false
}
