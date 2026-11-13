package plugin

import (
	"context"
	"errors"
	"maibot-go/internal/webui/httputil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"maibot-go/internal/plugin_market"
	"maibot-go/internal/plugin_runtime"
)

// resolvePluginConfigPaths 校验插件 ID 并定位已安装插件目录与 config.toml
func (d Deps) resolvePluginConfigPaths(pluginID string) (struct{ pluginPath, configPath string }, error) {
	var paths struct{ pluginPath, configPath string }
	if err := pluginmarket.ValidatePluginID(pluginID); err != nil {
		return paths, err
	}
	root, err := pluginmarket.ResolvePluginsDir(d.InstanceDir)
	if err != nil {
		return paths, err
	}
	pluginPath, err := pluginmarket.FindInstalledPlugin(root, pluginID)
	if err != nil {
		return paths, err
	}
	if pluginPath == "" {
		return paths, pluginmarket.NewMarketError(http.StatusNotFound, "未找到插件: "+pluginID)
	}
	paths.pluginPath = pluginPath
	paths.configPath = filepath.Join(pluginPath, "config.toml")
	return paths, nil
}

// inspectPluginConfig 调用运行时解析配置；Runner 不可用时映射为 503
func (d Deps) inspectPluginConfig(ctx context.Context, pluginID string, configData map[string]any, useProvided bool) (pluginConfigSnapshot, error) {
	if d.PluginHost == nil {
		return pluginConfigSnapshot{}, pluginmarket.NewMarketError(http.StatusServiceUnavailable, "插件配置运行时不可用，无法读取配置定义")
	}
	result, err := d.PluginHost.InspectPluginConfig(ctx, pluginID, configData, useProvided)
	if err != nil {
		if runnerUnavailable(err) {
			return pluginConfigSnapshot{}, pluginmarket.NewMarketError(http.StatusServiceUnavailable, "插件配置运行时不可用，无法读取配置定义")
		}
		return pluginConfigSnapshot{}, pluginmarket.NewMarketError(http.StatusBadRequest, err.Error())
	}
	return pluginConfigSnapshot{
		ConfigSchema:     result.ConfigSchema,
		NormalizedConfig: result.NormalizedConfig,
		DefaultConfig:    result.DefaultConfig,
		ConfigError:      result.ConfigError,
		Enabled:          result.Enabled,
	}, nil
}

// validatePluginConfig 调用运行时校验并返回规范化配置
func (d Deps) validatePluginConfig(ctx context.Context, pluginID string, configData map[string]any) (map[string]any, error) {
	if d.PluginHost == nil {
		return nil, pluginmarket.NewMarketError(http.StatusServiceUnavailable, "插件配置运行时不可用，无法校验配置")
	}
	validated, err := d.PluginHost.ValidatePluginConfig(ctx, pluginID, configData)
	if err != nil {
		if runnerUnavailable(err) {
			return nil, pluginmarket.NewMarketError(http.StatusServiceUnavailable, "插件配置运行时不可用，无法校验配置")
		}
		return nil, pluginmarket.NewMarketError(http.StatusBadRequest, err.Error())
	}
	return validated, nil
}

// configWidgets 调用运行时按钮组件，错误按 400/503 映射
func (d Deps) configWidgets(ctx context.Context, pluginID, actionID string) (map[string]any, error) {
	if d.PluginHost == nil {
		return nil, pluginmarket.NewMarketError(http.StatusServiceUnavailable, "插件配置运行时不可用")
	}
	snapshot, err := d.PluginHost.ConfigWidgets(ctx, pluginID, actionID)
	if err != nil {
		if runnerUnavailable(err) {
			return nil, pluginmarket.NewMarketError(http.StatusServiceUnavailable, err.Error())
		}
		return nil, pluginmarket.NewMarketError(http.StatusBadRequest, err.Error())
	}
	return snapshot, nil
}

// pluginConfigRuntimeStatus 近似 Python apply_plugin_config 返回的 runtime_status
func (d Deps) pluginConfigRuntimeStatus(ctx context.Context, pluginID string) (string, error) {
	if d.PluginHost == nil {
		return "stopped", nil
	}
	result, err := d.PluginHost.InspectPluginConfig(ctx, pluginID, nil, false)
	if err != nil {
		if runnerUnavailable(err) {
			return "stopped", nil
		}
		return "stopped", err
	}
	if result.ConfigError != "" {
		return "config_error", nil
	}
	return "running", nil
}

// syncPluginDependenciesOnEnable 启用插件前同步依赖，必要时重启 Runner
// 对齐 Python integration.py:278-292：依赖环境或阻止列表变化时以 config_enabled_dependencies 重启
func (d Deps) syncPluginDependenciesOnEnable(ctx context.Context, pluginID string) error {
	if d.PluginHost == nil {
		return nil
	}
	pipeline := pluginruntime.NewPluginDependencyPipeline("")
	result, changed := d.PluginHost.SyncPluginDependencies(ctx, pipeline, d.PluginHost.PluginDirs(), nil)
	if blocked := d.PluginHost.BlockedPluginReasons()[pluginID]; blocked != "" {
		return errors.New(blocked)
	}
	if !result.EnvironmentChanged && len(changed) == 0 {
		return nil
	}
	restarted, err := d.PluginHost.RestartWithLastOptions(ctx)
	if err != nil {
		return err
	}
	if !restarted {
		return nil
	}
	return nil
}

func pluginConfigStatusNote(status string) string {
	switch status {
	case "config_error":
		return "配置仍有错误，插件业务暂停"
	case "stopped":
		return "运行时尚未启动"
	default:
		return "配置已同步到插件运行时"
	}
}

func runnerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "未连接") || strings.Contains(message, "not connected")
}

func writePluginRouteFailure(w http.ResponseWriter, err error) {
	if marketErr, ok := pluginmarket.AsMarketError(err); ok {
		httputil.WriteJSON(w, marketErr.StatusCode, map[string]any{"detail": marketErr.Message})
		return
	}
	httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "服务器错误: " + err.Error()})
}

func readPluginConfigText(path string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

func writePluginConfigText(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// backupPluginConfigFile 对齐 support.backup_file：config_back/{name}.{action}.{timestamp}
func backupPluginConfigFile(path, action string, move bool) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	backupDir := filepath.Join(filepath.Dir(path), "config_back")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	backupPath := filepath.Join(backupDir, filepath.Base(path)+"."+action+"."+time.Now().Format("20060102150405"))
	if move {
		return backupPath, os.Rename(path, backupPath)
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return backupPath, os.WriteFile(backupPath, raw, 0o644)
}

func movePluginConfigBackup(path, action string) (string, error) {
	return backupPluginConfigFile(path, action, true)
}

func readPluginEnabledValue(raw string) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return true, nil
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(raw), &document); err != nil {
		return true, err
	}
	pluginSection, ok := document["plugin"].(map[string]any)
	if !ok {
		return true, nil
	}
	value, ok := pluginSection["enabled"]
	if !ok {
		return true, nil
	}
	return coerceEnabledValue(value), nil
}

func coerceEnabledValue(value any) bool {
	if text, ok := value.(string); ok {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "false", "0", "no", "off", "disabled":
			return false
		default:
			return true
		}
	}
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	return value != nil
}

var pluginSectionHeaderPattern = regexp.MustCompile(`^\s*\[\s*plugin\s*\]\s*(?:#.*)?$`)
var anySectionHeaderPattern = regexp.MustCompile(`^\s*\[`)
var enabledAssignmentPattern = regexp.MustCompile(`^(\s*enabled\s*=\s*)(\S+)(\s*(?:#.*)?)$`)

// setPluginEnabledText 只改写 [plugin].enabled，保留注释与其余键（对齐 toml_edit.setPluginEnabled）
func setPluginEnabledText(raw string, enabled bool) string {
	value := "false"
	if enabled {
		value = "true"
	}
	newline := "\n"
	if strings.Contains(raw, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(raw, "\n")
	header := -1
	for index, line := range lines {
		if pluginSectionHeaderPattern.MatchString(strings.TrimSuffix(line, "\r")) {
			header = index
			break
		}
	}
	if header < 0 {
		if raw != "" && !strings.HasSuffix(raw, newline) {
			raw += newline
		}
		if raw != "" && !strings.HasSuffix(raw, newline+newline) {
			raw += newline
		}
		return raw + "[plugin]" + newline + "enabled = " + value + newline
	}
	end := len(lines)
	for index := header + 1; index < len(lines); index++ {
		if anySectionHeaderPattern.MatchString(strings.TrimSuffix(lines[index], "\r")) {
			end = index
			break
		}
	}
	for index := header + 1; index < end; index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		matches := enabledAssignmentPattern.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		updated := matches[1] + value + matches[3]
		if strings.HasSuffix(lines[index], "\r") {
			updated += "\r"
		}
		lines[index] = updated
		return strings.Join(lines, "\n")
	}
	insertAt := end
	carriageReturn := ""
	if insertAt > header+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	if insertAt < len(lines) && strings.HasSuffix(lines[insertAt], "\r") {
		carriageReturn = "\r"
	}
	lines = append(lines[:insertAt], append([]string{"enabled = " + value + carriageReturn}, lines[insertAt:]...)...)
	return strings.Join(lines, "\n")
}

func normalizeDottedKeys(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	type dottedItem struct {
		key   string
		value any
	}
	dotted := make([]dottedItem, 0)
	for key, value := range input {
		if strings.Contains(key, ".") {
			dotted = append(dotted, dottedItem{key: key, value: value})
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			result[key] = normalizeDottedKeys(nested)
		} else {
			result[key] = value
		}
	}
	for _, item := range dotted {
		normalizedValue := item.value
		if nested, ok := item.value.(map[string]any); ok {
			normalizedValue = normalizeDottedKeys(nested)
		}
		parts := strings.Split(item.key, ".")
		filtered := parts[:0]
		for _, part := range parts {
			if part != "" {
				filtered = append(filtered, part)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		current := result
		for _, part := range filtered[:len(filtered)-1] {
			next, ok := current[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				current[part] = next
			}
			current = next
		}
		last := filtered[len(filtered)-1]
		if existing, ok := current[last].(map[string]any); ok {
			if incoming, ok := normalizedValue.(map[string]any); ok {
				deepMergeMaps(existing, incoming)
				continue
			}
		}
		current[last] = normalizedValue
	}
	return result
}

func deepMergeMaps(destination, source map[string]any) {
	for key, value := range source {
		if existing, ok := destination[key].(map[string]any); ok {
			if incoming, ok := value.(map[string]any); ok {
				deepMergeMaps(existing, incoming)
				continue
			}
		}
		destination[key] = value
	}
}

func copyStringAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// virtualGeneralFieldNames 对齐 _get_virtual_general_field_names
func virtualGeneralFieldNames(snapshot pluginConfigSnapshot) []string {
	sections, ok := snapshot.ConfigSchema["sections"].(map[string]any)
	if !ok {
		return nil
	}
	general, ok := sections["general"].(map[string]any)
	if !ok {
		return nil
	}
	fields, ok := general["fields"].(map[string]any)
	if !ok {
		return nil
	}
	if _, isTable := snapshot.DefaultConfig["general"].(map[string]any); isTable {
		return nil
	}
	if _, isTable := snapshot.NormalizedConfig["general"].(map[string]any); isTable {
		return nil
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	for _, name := range names {
		if _, exists := snapshot.DefaultConfig[name]; !exists {
			return nil
		}
	}
	return names
}

func foldRootFieldsIntoVirtualGeneral(config map[string]any, fieldNames []string) map[string]any {
	folded := copyStringAnyMap(config)
	general := map[string]any{}
	for _, name := range fieldNames {
		value, exists := folded[name]
		if !exists {
			continue
		}
		if name == "plugin" {
			general[name] = value
			continue
		}
		general[name] = value
		delete(folded, name)
	}
	folded["general"] = general
	return folded
}

func unfoldVirtualGeneralIntoRoot(patch map[string]any) {
	general, ok := patch["general"].(map[string]any)
	if !ok {
		return
	}
	delete(patch, "general")
	for key, value := range general {
		patch[key] = value
	}
}
