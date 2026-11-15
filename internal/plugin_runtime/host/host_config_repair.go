package host

// 插件配置版本修复。
//
// Runner 的版本策略不接受缺少 plugin.config_version 的 config.toml，而市场上
// 部分插件仍以「只有 [plugin] enabled」的模板分发。宿主在这里按插件自身的
// 默认配置模型重建一次，并保留原有的 [plugin].enabled。

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
)

// PluginConfigHasVersion 判断 config.toml 内容是否声明了 plugin.config_version
func PluginConfigHasVersion(raw []byte) bool {
	var config map[string]any
	if err := toml.Unmarshal(raw, &config); err != nil {
		return false
	}
	return pluginConfigVersion(config) != ""
}

// pluginConfigVersion 读取配置里的 plugin.config_version
func pluginConfigVersion(config map[string]any) string {
	section, ok := config["plugin"].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := section["config_version"].(string)
	return strings.TrimSpace(value)
}

// RepairVersionlessPluginConfig 用插件默认配置重建缺少 config_version 的 config.toml。
//
// 调用 Runner 的 inspect（use_provided_config=true 会跳过版本校验）取得按
// 插件配置模型归一化后的完整配置，写入前备份原文件为 config.toml.bak。
// 返回 true 表示发生了重建。
func (h *Host) RepairVersionlessPluginConfig(ctx context.Context, pluginID string) (bool, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return false, nil
	}
	path, err := h.pluginConfigPath(pluginID)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 没有配置文件时 Runner 会用默认配置生成，不需要修复
			return false, nil
		}
		return false, err
	}
	var current map[string]any
	if err := toml.Unmarshal(raw, &current); err != nil {
		return false, fmt.Errorf("解析插件配置失败: %w", err)
	}
	if pluginConfigVersion(current) != "" {
		return false, nil
	}

	enabled, hasEnabled := pluginConfigBool(current, "enabled")

	result, err := h.InspectPluginConfig(ctx, pluginID, current, true)
	if err != nil {
		return false, fmt.Errorf("读取插件默认配置失败: %w", err)
	}
	rebuilt := result.NormalizedConfig
	if pluginConfigVersion(rebuilt) == "" {
		rebuilt = result.DefaultConfig
	}
	if pluginConfigVersion(rebuilt) == "" {
		return false, fmt.Errorf("Runner 未返回带 config_version 的插件配置")
	}
	section, ok := rebuilt["plugin"].(map[string]any)
	if !ok {
		section = map[string]any{}
		rebuilt["plugin"] = section
	}
	// 保留用户在旧文件里写的启停状态；默认配置里没有时按启用处理
	if hasEnabled {
		section["enabled"] = enabled
	} else if _, exists := section["enabled"]; !exists {
		section["enabled"] = true
	}

	encoded, err := toml.Marshal(rebuilt)
	if err != nil {
		return false, fmt.Errorf("序列化插件配置失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path+".bak", raw, 0o644); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return false, err
	}
	zap.L().Info("插件配置缺少 config_version，已按插件默认配置重建",
		zap.String("plugin_id", pluginID), zap.String("path", path))
	return true, nil
}

// RepairVersionlessPluginConfigs 批量修复已注册插件的配置，返回被重建的插件 ID
func (h *Host) RepairVersionlessPluginConfigs(ctx context.Context) []string {
	if h == nil {
		return nil
	}
	repaired := make([]string, 0, 1)
	for _, record := range h.ListPlugins() {
		ok, err := h.RepairVersionlessPluginConfig(ctx, record.PluginID)
		if err != nil {
			zap.L().Warn("插件配置自动修复失败",
				zap.String("plugin_id", record.PluginID), zap.Error(err))
			continue
		}
		if ok {
			repaired = append(repaired, record.PluginID)
		}
	}
	return repaired
}

func pluginConfigBool(config map[string]any, key string) (bool, bool) {
	section, ok := config["plugin"].(map[string]any)
	if !ok {
		return false, false
	}
	value, ok := section[key].(bool)
	return value, ok
}
