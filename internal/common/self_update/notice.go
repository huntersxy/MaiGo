package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"MaiGo/internal/common/version"

	"github.com/bytedance/sonic"
)

// 更新检查状态
const (
	UpdateCheckChecking    = "checking"
	UpdateCheckAvailable   = "available"
	UpdateCheckUnavailable = "unavailable"
	UpdateCheckNotFound    = "not_found"
	UpdateCheckFailed      = "check_failed"
)

// IncompatiblePluginNotice 一次主程序更新导致的不兼容插件
type IncompatiblePluginNotice struct {
	PluginID         string  `json:"plugin_id"`
	Name             string  `json:"name"`
	InstalledVersion string  `json:"installed_version"`
	HostMinVersion   string  `json:"host_min_version"`
	HostMaxVersion   string  `json:"host_max_version"`
	UpdateStatus     string  `json:"update_status"`
	UpdateVersion    *string `json:"update_version,omitempty"`
}

// MarketplacePlugin 插件市场条目
type MarketplacePlugin struct {
	ID       string         `json:"id"`
	Manifest map[string]any `json:"manifest"`
}

// MarketplaceLoader 拉取插件市场目录
type MarketplaceLoader func(ctx context.Context) ([]MarketplacePlugin, error)

type pluginDescriptor struct {
	pluginID string
	name     string
	version  string
	hostMin  string
	hostMax  string
}

// FindPluginsMadeIncompatible 查找旧版兼容、升级后不再兼容的已安装插件
func FindPluginsMadeIncompatible(fromVersion, currentVersion, pluginsDir string) ([]IncompatiblePluginNotice, error) {
	if pluginsDir == "" {
		return nil, nil
	}
	info, err := os.Stat(pluginsDir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	notices := make([]IncompatiblePluginNotice, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.EqualFold(name, "data") || strings.EqualFold(name, "__pycache__") {
			continue
		}
		path := filepath.Join(pluginsDir, name)
		if target, err := os.Readlink(path); err == nil && target != "" {
			continue
		}
		manifestPath := filepath.Join(path, "_manifest.json")
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		manifest := make(map[string]any)
		if err := sonic.ConfigStd.Unmarshal(raw, &manifest); err != nil {
			continue
		}
		descriptor, ok := parsePluginDescriptor(manifest, "")
		if !ok {
			continue
		}
		if !isHostCompatible(fromVersion, descriptor.hostMin, descriptor.hostMax) {
			continue
		}
		if isHostCompatible(currentVersion, descriptor.hostMin, descriptor.hostMax) {
			continue
		}
		notices = append(notices, IncompatiblePluginNotice{
			PluginID: descriptor.pluginID, Name: descriptor.name,
			InstalledVersion: descriptor.version, HostMinVersion: descriptor.hostMin,
			HostMaxVersion: descriptor.hostMax, UpdateStatus: UpdateCheckChecking,
		})
	}
	return notices, nil
}

// MarkCompatibleMarketplaceUpdates 根据市场当前清单标记兼容更新
func MarkCompatibleMarketplaceUpdates(plugins []IncompatiblePluginNotice, marketplace []MarketplacePlugin, currentVersion string) []IncompatiblePluginNotice {
	byID := make(map[string]pluginDescriptor)
	for _, item := range marketplace {
		if item.Manifest == nil {
			continue
		}
		descriptor, ok := parsePluginDescriptor(item.Manifest, item.ID)
		if !ok {
			continue
		}
		byID[strings.ToLower(descriptor.pluginID)] = descriptor
	}
	results := make([]IncompatiblePluginNotice, 0, len(plugins))
	for _, plugin := range plugins {
		descriptor, ok := byID[strings.ToLower(plugin.PluginID)]
		if !ok {
			plugin.UpdateStatus = UpdateCheckNotFound
			results = append(results, plugin)
			continue
		}
		newer, err := version.CompareSemVer(descriptor.version, plugin.InstalledVersion)
		if err != nil || newer <= 0 || !isHostCompatible(currentVersion, descriptor.hostMin, descriptor.hostMax) {
			plugin.UpdateStatus = UpdateCheckUnavailable
			results = append(results, plugin)
			continue
		}
		value := descriptor.version
		plugin.UpdateStatus = UpdateCheckAvailable
		plugin.UpdateVersion = &value
		results = append(results, plugin)
	}
	return results
}

// CollectUpdateIncompatiblePlugins 收集不兼容插件并检查市场更新
func CollectUpdateIncompatiblePlugins(ctx context.Context, fromVersion, currentVersion, pluginsDir string, loader MarketplaceLoader) ([]IncompatiblePluginNotice, error) {
	plugins, err := FindPluginsMadeIncompatible(fromVersion, currentVersion, pluginsDir)
	if err != nil {
		return nil, err
	}
	if len(plugins) == 0 {
		return []IncompatiblePluginNotice{}, nil
	}
	if loader == nil {
		for index := range plugins {
			plugins[index].UpdateStatus = UpdateCheckFailed
		}
		return plugins, nil
	}
	marketplace, err := loader(ctx)
	if err != nil {
		for index := range plugins {
			plugins[index].UpdateStatus = UpdateCheckFailed
		}
		return plugins, nil
	}
	return MarkCompatibleMarketplaceUpdates(plugins, marketplace, currentVersion), nil
}

// FormatTerminalCompatibilityNotice 生成终端兼容性提醒
func FormatTerminalCompatibilityNotice(fromVersion, currentVersion string, plugins []IncompatiblePluginNotice) string {
	lines := []string{
		"插件兼容性提醒",
		strings.Repeat("=", 48),
		"以下插件因 MaiBot 从 v" + fromVersion + " 更新到 v" + currentVersion + " 后不再兼容：",
	}
	for _, plugin := range plugins {
		updateText := "暂无兼容更新"
		switch plugin.UpdateStatus {
		case UpdateCheckAvailable:
			text := "可更新"
			if plugin.UpdateVersion != nil {
				text += "至 v" + *plugin.UpdateVersion
			}
			updateText = text + "（兼容当前版本）"
		case UpdateCheckFailed:
			updateText = "兼容更新检查失败"
		case UpdateCheckNotFound:
			updateText = "插件市场中未找到该插件"
		}
		lines = append(lines, "- "+plugin.Name+" ("+plugin.PluginID+") v"+plugin.InstalledVersion+"："+
			"支持 MaiBot v"+plugin.HostMinVersion+" - v"+plugin.HostMaxVersion+"；"+updateText)
	}
	lines = append(lines, strings.Repeat("=", 48))
	return strings.Join(lines, "\n")
}

func parsePluginDescriptor(manifest map[string]any, fallbackID string) (pluginDescriptor, bool) {
	pluginID := strings.TrimSpace(manifestString(manifest["id"]))
	if pluginID == "" {
		pluginID = strings.TrimSpace(fallbackID)
	}
	name := strings.TrimSpace(manifestString(manifest["name"]))
	if name == "" {
		name = pluginID
	}
	value := strings.TrimSpace(manifestString(manifest["version"]))
	host, ok := manifest["host_application"].(map[string]any)
	if !ok {
		return pluginDescriptor{}, false
	}
	hostMin := strings.TrimSpace(manifestString(host["min_version"]))
	hostMax := strings.TrimSpace(manifestString(host["max_version"]))
	if pluginID == "" || name == "" || !version.IsValidSemVer(value) ||
		!version.IsValidSemVer(hostMin) || !version.IsValidSemVer(hostMax) {
		return pluginDescriptor{}, false
	}
	if compared, err := version.CompareSemVer(hostMin, hostMax); err != nil || compared > 0 {
		return pluginDescriptor{}, false
	}
	return pluginDescriptor{pluginID: pluginID, name: name, version: value, hostMin: hostMin, hostMax: hostMax}, true
}

func manifestString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return ""
	}
}

func isHostCompatible(hostVersion, minVersion, maxVersion string) bool {
	if hostVersion == "" || minVersion == "" || maxVersion == "" {
		return false
	}
	if compared, err := version.CompareSemVer(hostVersion, minVersion); err != nil || compared < 0 {
		return false
	}
	comparedMax, err := version.CompareSemVer(hostVersion, maxVersion)
	if err != nil {
		return false
	}
	if comparedMax <= 0 {
		return true
	}
	current, err1 := version.ParseSemVer(hostVersion)
	maximum, err2 := version.ParseSemVer(maxVersion)
	return err1 == nil && err2 == nil && current.Major == maximum.Major && current.Minor == maximum.Minor
}
