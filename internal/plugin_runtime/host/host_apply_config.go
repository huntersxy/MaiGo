package host

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// ApplyPluginConfig 串行应用插件配置到运行时，WebUI 与手工文件变更共用同一入口
// 对齐 Python integration.py:257-330 apply_plugin_config
// 返回 runtime_status：success / inactive / config_error / stopped
func (h *Host) ApplyPluginConfig(ctx context.Context, pluginID string) (string, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return "", errors.New("plugin_id 不能为空")
	}
	if h == nil {
		return "stopped", nil
	}
	if h.activeRPCServer() == nil {
		return "stopped", nil
	}
	// 对齐 Python integration.py:261 的 _plugin_file_update_lock：配置应用与源码重载串行
	h.pluginFileUpdateMu.Lock()
	defer h.pluginFileUpdateMu.Unlock()
	// 缺 plugin.config_version 的旧模板配置会让 Runner 拒绝加载，先按默认配置重建
	if repaired, repairErr := h.RepairVersionlessPluginConfig(ctx, pluginID); repairErr != nil {
		zap.L().Warn("插件配置自动修复失败", zap.String("plugin_id", pluginID), zap.Error(repairErr))
	} else if repaired {
		loaded := h.isPluginLoaded(pluginID)
		if loaded {
			if _, reloadErr := h.ReloadPlugins(ctx, []string{pluginID}, "config_repaired", nil); reloadErr != nil {
				zap.L().Warn("重建插件配置后重载失败", zap.String("plugin_id", pluginID), zap.Error(reloadErr))
			}
		}
	}
	loaded := h.isPluginLoaded(pluginID)
	if !loaded {
		// 配置解析会冷导入插件，启用前必须先补齐依赖，避免在检查配置之前就因缺包失败
		result, changed := h.SyncPluginDependencies(ctx, h.dependencyRunner, h.PluginDirs(), nil)
		if blocked := h.BlockedPluginReasons()[pluginID]; blocked != "" {
			return "", errors.New(blocked)
		}
		if result.EnvironmentChanged || len(changed) > 0 {
			restarted, err := h.RestartWithLastOptions(ctx)
			if err != nil {
				return "", fmt.Errorf("启用插件时同步依赖后重启运行时失败: %w", err)
			}
			if !restarted {
				return "", errors.New("启用插件时同步依赖后重启运行时失败")
			}
		}
	}
	snapshot, err := h.InspectPluginConfig(ctx, pluginID, nil, false)
	if err != nil {
		return "", err
	}
	loaded = h.isPluginLoaded(pluginID)
	if snapshot.ConfigError != "" {
		// 配置有错时定向重载该插件，让运行时记录配置错误状态
		if !loaded || h.pluginConfigError(pluginID) != snapshot.ConfigError {
			if _, reloadErr := h.ReloadPlugins(ctx, []string{pluginID}, "config_invalid", nil); reloadErr != nil {
				return "", fmt.Errorf("插件配置管理状态加载失败: %w", reloadErr)
			}
		}
		// 对齐 Python integration.py:295：配置有错时清除已应用缓存，修正后才会重新通知
		h.forgetAppliedConfig(pluginID)
		return "config_error", nil
	}
	if loaded && h.pluginConfigError(pluginID) != "" {
		// 配置从错误恢复时重新激活插件
		if _, reloadErr := h.ReloadPlugins(ctx, []string{pluginID}, "config_repaired", nil); reloadErr != nil {
			return "", fmt.Errorf("修正配置后插件激活失败: %w", reloadErr)
		}
		if snapshot.Enabled {
			return "success", nil
		}
		return "inactive", nil
	}
	// 对齐 Python integration.py:306-308：配置与加载状态都没变时直接返回，避免重复通知
	if h.appliedConfigMatches(pluginID, snapshot.NormalizedConfig) && loaded == snapshot.Enabled {
		if loaded {
			return "success", nil
		}
		return "inactive", nil
	}
	h.setPluginTransition(pluginID, snapshot.Enabled)
	defer h.clearPluginTransition(pluginID)
	if !snapshot.Enabled {
		if loaded {
			if _, reloadErr := h.ReloadPlugins(ctx, []string{pluginID}, "config_disabled", nil); reloadErr != nil {
				return "", fmt.Errorf("插件卸载失败: %w", reloadErr)
			}
		}
		h.rememberAppliedConfig(pluginID, snapshot.NormalizedConfig)
		return "inactive", nil
	}
	if loaded {
		acknowledged, notifyErr := h.NotifyPluginConfigUpdated(ctx, pluginID, snapshot.NormalizedConfig, "", "self")
		if notifyErr != nil {
			return "", fmt.Errorf("插件配置更新通知失败: %w", notifyErr)
		}
		if !acknowledged {
			return "", errors.New("插件配置更新通知失败")
		}
		h.rememberAppliedConfig(pluginID, snapshot.NormalizedConfig)
		return "success", nil
	}
	if _, loadErr := h.ReloadPlugins(ctx, []string{pluginID}, "config_enabled", nil); loadErr != nil {
		return "", fmt.Errorf("插件未能激活，请检查依赖插件及运行日志: %w", loadErr)
	}
	h.rememberAppliedConfig(pluginID, snapshot.NormalizedConfig)
	return "success", nil
}

// appliedConfigMatches 判断配置是否与上次应用的一致
func (h *Host) appliedConfigMatches(pluginID string, config map[string]any) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	previous, ok := h.appliedPluginConfigs[pluginID]
	if !ok {
		return false
	}
	return configMapsEqual(previous, config)
}

// rememberAppliedConfig 记录已应用的配置快照
func (h *Host) rememberAppliedConfig(pluginID string, config map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.appliedPluginConfigs == nil {
		h.appliedPluginConfigs = map[string]map[string]any{}
	}
	h.appliedPluginConfigs[pluginID] = cloneConfigMap(config)
}

// forgetAppliedConfig 清除已应用配置，对齐 Python 的 pop(plugin_id, None)
func (h *Host) forgetAppliedConfig(pluginID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.appliedPluginConfigs, pluginID)
}

// setPluginTransition 记录插件过渡状态，对齐 Python _plugin_transitions
func (h *Host) setPluginTransition(pluginID string, enabled bool) {
	state := "stopping"
	if enabled {
		state = "loading"
	}
	h.mu.Lock()
	if h.pluginTransitions == nil {
		h.pluginTransitions = map[string]string{}
	}
	h.pluginTransitions[pluginID] = state
	h.mu.Unlock()
	h.notifyStatusChanges()
}

// clearPluginTransition 清除过渡状态
func (h *Host) clearPluginTransition(pluginID string) {
	h.mu.Lock()
	delete(h.pluginTransitions, pluginID)
	h.mu.Unlock()
	h.notifyStatusChanges()
}

// SetPluginTransitionForTest 供测试设置过渡状态
func (h *Host) SetPluginTransitionForTest(pluginID, state string) {
	h.mu.Lock()
	if h.pluginTransitions == nil {
		h.pluginTransitions = map[string]string{}
	}
	h.pluginTransitions[pluginID] = state
	h.mu.Unlock()
}

// PluginTransition 返回插件当前过渡状态，空串表示无过渡
func (h *Host) PluginTransition(pluginID string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pluginTransitions[pluginID]
}

// cloneConfigMap 浅拷贝配置对象
func cloneConfigMap(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	cloned := make(map[string]any, len(config))
	for key, value := range config {
		cloned[key] = value
	}
	return cloned
}

// configMapsEqual 比较两个配置对象是否等价
func configMapsEqual(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		rightValue, ok := right[key]
		if !ok || !configValuesEqual(leftValue, rightValue) {
			return false
		}
	}
	return true
}

// configValuesEqual 递归比较配置值
func configValuesEqual(left, right any) bool {
	leftMap, leftIsMap := left.(map[string]any)
	rightMap, rightIsMap := right.(map[string]any)
	if leftIsMap || rightIsMap {
		if !leftIsMap || !rightIsMap {
			return false
		}
		return configMapsEqual(leftMap, rightMap)
	}
	leftList, leftIsList := left.([]any)
	rightList, rightIsList := right.([]any)
	if leftIsList || rightIsList {
		if !leftIsList || !rightIsList || len(leftList) != len(rightList) {
			return false
		}
		for index := range leftList {
			if !configValuesEqual(leftList[index], rightList[index]) {
				return false
			}
		}
		return true
	}
	return left == right
}

// isPluginLoaded 判断插件是否已由 Runner 加载
func (h *Host) isPluginLoaded(pluginID string) bool {
	if h == nil {
		return false
	}
	ready, ok := h.runnerReadySnapshot()
	if !ok {
		return false
	}
	for _, loaded := range ready.LoadedPlugins {
		if strings.TrimSpace(loaded) == pluginID {
			return true
		}
	}
	return false
}

// pluginConfigError 返回 Runner 记录的插件配置错误
func (h *Host) pluginConfigError(pluginID string) string {
	if h == nil {
		return ""
	}
	ready, ok := h.runnerReadySnapshot()
	if !ok {
		return ""
	}
	return strings.TrimSpace(ready.FailedPluginReasons[pluginID])
}
