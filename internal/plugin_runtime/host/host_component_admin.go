package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// hostComponentAdmin 基于 Host 的插件管理端口，供 component.* 能力在未注入外部端口时使用
type hostComponentAdmin struct {
	host *Host
}

// ComponentAdmin 返回 Host 内置的插件管理端口
func (h *Host) ComponentAdmin() PluginComponentAdminPort {
	if h == nil {
		return nil
	}
	return &hostComponentAdmin{host: h}
}

func (a *hostComponentAdmin) LoadPlugin(ctx context.Context, pluginID, reason string) (bool, error) {
	if a == nil || a.host == nil {
		return false, fmt.Errorf("Host 未初始化")
	}
	result, err := a.host.ReloadPlugin(ctx, pluginID, reason, nil)
	if err != nil {
		return false, err
	}
	return result.Success, nil
}

func (a *hostComponentAdmin) ReloadPlugins(ctx context.Context, pluginIDs []string, reason string) (bool, error) {
	if a == nil || a.host == nil {
		return false, fmt.Errorf("Host 未初始化")
	}
	ids := dedupePluginIDs(pluginIDs)
	if len(ids) == 0 {
		for _, rec := range a.host.ListPlugins() {
			ids = append(ids, rec.PluginID)
		}
		sort.Strings(ids)
	}
	for _, pluginID := range ids {
		if _, err := a.host.ReloadPlugin(ctx, pluginID, reason, nil); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (a *hostComponentAdmin) UpdatePluginConfig(ctx context.Context, pluginID, key string, value any) (map[string]any, error) {
	if a == nil || a.host == nil {
		return nil, fmt.Errorf("Host 未初始化")
	}
	pluginID = strings.TrimSpace(pluginID)
	key = strings.TrimSpace(key)
	if pluginID == "" {
		return nil, fmt.Errorf("缺少必要参数 plugin_name 或 key")
	}
	if key == "" {
		return nil, fmt.Errorf("缺少必要参数 plugin_name 或 key")
	}
	configPath, err := a.host.pluginConfigPath(pluginID)
	if err != nil {
		return nil, err
	}
	configData := map[string]any{}
	var originalRaw []byte
	if raw, readErr := os.ReadFile(configPath); readErr == nil {
		originalRaw = raw
		if len(strings.TrimSpace(string(raw))) > 0 {
			if unmarshalErr := toml.Unmarshal(raw, &configData); unmarshalErr != nil {
				return nil, fmt.Errorf("解析插件配置失败: %w", unmarshalErr)
			}
		}
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("读取插件配置失败: %w", readErr)
	}
	if err := setNestedPluginConfigValue(configData, key, value); err != nil {
		return nil, err
	}
	if normalized, validateErr := a.host.ValidatePluginConfig(ctx, pluginID, configData); validateErr == nil && normalized != nil {
		configData = normalized
	}
	encoded, err := toml.Marshal(configData)
	if err != nil {
		return nil, fmt.Errorf("配置写入失败: %w", err)
	}
	encoded = mergePluginTOMLPreservingComments(originalRaw, encoded)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return nil, fmt.Errorf("配置写入失败: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(configPath), ".config-*.toml")
	if err != nil {
		return nil, fmt.Errorf("配置写入失败: %w", err)
	}
	tempName := temp.Name()
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return nil, fmt.Errorf("配置写入失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return nil, fmt.Errorf("配置写入失败: %w", err)
	}
	if err := os.Rename(tempName, configPath); err != nil {
		_ = os.Remove(tempName)
		return nil, fmt.Errorf("配置写入失败: %w", err)
	}
	if pluginID == builtinPluginManagementID {
		a.host.loadPluginManagementAliases()
	}
	return configData, nil
}

// pluginConfigPath 按 Runner 插件目录定位 <plugin_dir>/<plugin_id>/config.toml
func (h *Host) pluginConfigPath(pluginID string) (string, error) {
	h.mu.RLock()
	var dirs []string
	for _, state := range h.runners {
		if state != nil {
			dirs = append(dirs, state.PluginDirs...)
			dirs = append(dirs, state.TrustedPluginDirs...)
		}
	}
	h.mu.RUnlock()
	if len(dirs) == 0 {
		for _, dir := range filepath.SplitList(os.Getenv(EnvPluginDirs)) {
			if strings.TrimSpace(dir) != "" {
				dirs = append(dirs, dir)
			}
		}
	}
	if len(dirs) == 0 {
		dirs = []string{filepath.Join("data", "plugins"), "plugins"}
	}
	for _, root := range dirs {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if candidate := filepath.Join(root, pluginID); isDirectory(candidate) {
			return filepath.Join(candidate, "config.toml"), nil
		}
		// 插件目录名不一定等于插件 ID（如 plugin_management 对应 builtin.plugin-management）
		if candidate := pluginDirByManifestID(root, pluginID); candidate != "" {
			return filepath.Join(candidate, "config.toml"), nil
		}
	}
	// WebUI/本地内置插件没有运行时目录时，使用插件数据目录承载配置
	if dataDir, err := h.PluginDataDir(pluginID); err == nil && strings.TrimSpace(dataDir) != "" {
		return filepath.Join(dataDir, "config.toml"), nil
	}
	if !h.hasPlugin(pluginID) {
		return "", fmt.Errorf("未找到插件: %s", pluginID)
	}
	return "", fmt.Errorf("未找到插件目录: %s", pluginID)
}

// pluginDirByManifestID 扫描插件根目录下的直接子目录，按 _manifest.json 的 id 匹配
func pluginDirByManifestID(root, pluginID string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		dir := filepath.Join(root, name)
		raw, err := os.ReadFile(filepath.Join(dir, "_manifest.json"))
		if err != nil {
			continue
		}
		var manifest struct {
			ID       string `json:"id"`
			PluginID string `json:"plugin_id"`
		}
		if json.Unmarshal(raw, &manifest) != nil {
			continue
		}
		if strings.TrimSpace(firstNonEmptyText(manifest.ID, manifest.PluginID)) == pluginID {
			return dir
		}
	}
	return ""
}

// readPluginConfig 读取插件 config.toml（不存在时返回空配置）
func (h *Host) readPluginConfig(pluginID string) (map[string]any, string, error) {
	configPath, err := h.pluginConfigPath(pluginID)
	if err != nil {
		return nil, "", err
	}
	configData := map[string]any{}
	raw, readErr := os.ReadFile(configPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return configData, configPath, nil
		}
		return nil, configPath, fmt.Errorf("读取插件配置失败: %w", readErr)
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := toml.Unmarshal(raw, &configData); err != nil {
			return nil, configPath, fmt.Errorf("解析插件配置失败: %w", err)
		}
	}
	return configData, configPath, nil
}

func (h *Host) hasPlugin(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.plugins[pluginID]
	return ok
}

func dedupePluginIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// setNestedPluginConfigValue 对齐 _set_nested_plugin_config_value
func setNestedPluginConfigValue(configData map[string]any, key string, value any) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("配置键不能为空")
	}
	if alias, ok := strings.CutPrefix(key, "aliases.shortcuts."); ok {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return fmt.Errorf("指令别名不能为空")
		}
		aliases, err := configSection(configData, "aliases")
		if err != nil {
			return err
		}
		shortcuts, err := configSection(aliases, "shortcuts")
		if err != nil {
			return err
		}
		shortcuts[alias] = value
		return nil
	}
	if commandKey, ok := strings.CutPrefix(key, "aliases.command_aliases."); ok {
		commandKey = strings.TrimSpace(commandKey)
		if commandKey == "" {
			return fmt.Errorf("别名目标命令不能为空")
		}
		aliases, err := configSection(configData, "aliases")
		if err != nil {
			return err
		}
		commandAliases, err := configSection(aliases, "command_aliases")
		if err != nil {
			return err
		}
		commandAliases[commandKey] = value
		return nil
	}
	parts := make([]string, 0, 4)
	for _, part := range strings.Split(key, ".") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("配置键不能为空")
	}
	current := configData
	for _, part := range parts[:len(parts)-1] {
		section, err := configSection(current, part)
		if err != nil {
			return err
		}
		current = section
	}
	current[parts[len(parts)-1]] = value
	return nil
}

func configSection(parent map[string]any, key string) (map[string]any, error) {
	if raw, ok := parent[key]; ok && raw != nil {
		section, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("配置路径 %s 已存在且不是配置节", key)
		}
		return section, nil
	}
	section := map[string]any{}
	parent[key] = section
	return section, nil
}
