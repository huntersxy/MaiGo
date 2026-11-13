package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// builtinPluginManagementDir 内置插件管理的相对目录，对齐 MaiBot
// src/plugins/built_in/plugin_management/。
const builtinPluginManagementDir = "plugins/built_in/plugin_management"

// builtinPluginManifestInfo 内置插件清单中的展示字段
type builtinPluginManifestInfo struct {
	Version      string
	Name         string
	Description  string
	Capabilities []string
}

// verifyBuiltinPluginManagementManifest 启动期校验内置插件清单是否随仓分发
//
// 宿主对 /pm 的命令实现是原生组件（见 installPluginManagementCommand），
// 但插件身份、名称描述与能力声明来自 _manifest.json。注意
// loadPluginDependencyManifest 要求目录内存在 plugin.py（Go 侧没有，命令逻辑在
// 代码里），因此不能复用它来校验，否则内置插件清单永远读不到。
// 这里缺失只告警不阻断，与 Runner 未加载插件时的兜底路径一致。
func verifyBuiltinPluginManagementManifest() {
	info, err := readBuiltinPluginManifest(builtinPluginManagementDir)
	if err != nil {
		zap.L().Warn(
			"内置插件清单不可用，插件管理将以宿主内置组件运行",
			zap.String("path", builtinPluginManagementDir),
			zap.Error(err),
		)
		return
	}
	if info.Version == "" {
		zap.L().Warn("内置插件清单缺少 version", zap.String("path", builtinPluginManagementDir))
	}
}

// readBuiltinPluginManifest 读取内置插件的展示元数据
//
// 与 loadPluginDependencyManifest 的区别：不要求 plugin.py，只做元数据解析。
func readBuiltinPluginManifest(pluginDir string) (builtinPluginManifestInfo, error) {
	raw, err := os.ReadFile(filepath.Join(pluginDir, "_manifest.json"))
	if err != nil {
		return builtinPluginManifestInfo{}, err
	}
	var payload struct {
		ManifestVersion int      `json:"manifest_version"`
		Version         string   `json:"version"`
		Name            string   `json:"name"`
		Description     string   `json:"description"`
		ID              string   `json:"id"`
		Capabilities    []string `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return builtinPluginManifestInfo{}, err
	}
	if payload.ManifestVersion != 2 {
		return builtinPluginManifestInfo{}, errors.New("manifest_version 必须为 2")
	}
	if id := strings.TrimSpace(payload.ID); id != "" && id != builtinPluginManagementID {
		return builtinPluginManifestInfo{}, errors.New("清单 ID 与宿主不一致: " + id)
	}
	return builtinPluginManifestInfo{
		Version:      strings.TrimSpace(payload.Version),
		Name:         strings.TrimSpace(payload.Name),
		Description:  strings.TrimSpace(payload.Description),
		Capabilities: payload.Capabilities,
	}, nil
}
