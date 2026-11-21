package host

import (
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"MaiGo/internal/platform_io"
	chinesetypo "MaiGo/plugins/built_in/chinese_typo"
)

// HasEnabledNonNativePythonPlugins 扫描插件目录：存在启用中的非原生社区插件则需保留 Runner
func (h *Host) HasEnabledNonNativePythonPlugins() bool {
	dirs := h.PluginDirs()
	if len(dirs) == 0 {
		for _, dir := range filepath.SplitList(os.Getenv(EnvPluginDirs)) {
			if strings.TrimSpace(dir) != "" {
				dirs = append(dirs, dir)
			}
		}
	}
	for _, root := range dirs {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			pluginPath := filepath.Join(root, entry.Name())
			pluginID, ok := readManifestPluginID(pluginPath)
			if !ok {
				continue
			}
			if pluginID == chinesetypo.PluginID || IsNativeAdapter(pluginID) || platformio.DefaultNativeRuntime().IsRegistered(pluginID) {
				continue
			}
			configPath := ResolvePluginConfigPath(pluginPath)
			enabled, err := readCommunityPluginEnabled(configPath)
			if err != nil || !enabled {
				continue
			}
			return true
		}
	}
	return false
}

func readManifestPluginID(pluginPath string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(pluginPath, "_manifest.json"))
	if err != nil {
		return "", false
	}
	// 轻量解析，避免引入完整 validator
	text := string(raw)
	const key = `"id"`
	idx := strings.Index(text, key)
	if idx < 0 {
		return "", false
	}
	rest := text[idx+len(key):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if !strings.HasPrefix(rest, "\"") {
		return "", false
	}
	rest = rest[1:]
	end := strings.Index(rest, "\"")
	if end < 0 {
		return "", false
	}
	id := strings.TrimSpace(rest[:end])
	return id, id != ""
}

func readCommunityPluginEnabled(configPath string) (bool, error) {
	raw, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return false, err
	}
	section, ok := document["plugin"].(map[string]any)
	if !ok {
		return true, nil
	}
	value, ok := section["enabled"]
	if !ok {
		return true, nil
	}
	if typed, ok := value.(bool); ok {
		return typed, nil
	}
	return true, nil
}

// NativePluginConfigRoots 返回已登记原生适配器的配置父目录，供 watcher 扫描
func (h *Host) NativePluginConfigRoots() []string {
	roots := make([]string, 0)
	seen := map[string]struct{}{}
	for _, pluginID := range platformio.DefaultNativeRuntime().ListRegistered() {
		path, err := h.resolveNativePluginConfigPath(pluginID)
		if err != nil || strings.TrimSpace(path) == "" {
			continue
		}
		parent := filepath.Dir(filepath.Dir(path))
		// config.toml 的父目录是插件配置根；再上一级是扫描根
		// builtin: .../config/builtin_plugins/<dir>/config.toml → 扫描 builtin_plugins
		// snowluma: .../plugins/maibot-team_snowluma-adapter/config.toml → 扫描 plugins
		if reg, ok := platformio.DefaultNativeRuntime().Get(pluginID); ok && strings.TrimSpace(reg.ConfigRoot) != "" {
			parent = filepath.Dir(reg.ConfigRoot)
			if parent == "." || parent == "" {
				parent = reg.ConfigRoot
			}
		}
		if _, ok := seen[parent]; ok {
			continue
		}
		seen[parent] = struct{}{}
		roots = append(roots, parent)
	}
	return roots
}
