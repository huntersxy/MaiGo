package host

import (
	"context"
	"fmt"
	"os"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
)

// pythonStringValue 对齐 Python str() 对 None 与布尔值的字符串化
// 其余类型沿用 Go 的默认格式化，数字精度差异属已知边界
func pythonStringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(value)
	}
}

// configPluginName 对齐 Python str(args.get("plugin_name", plugin_id)) 的取值语义
// 显式传入时按字符串原样使用（不裁剪、不回退），缺省时才回退当前插件 ID
func configPluginName(args map[string]any, fallback string) string {
	if raw, ok := args["plugin_name"]; ok {
		return pythonStringValue(raw)
	}
	return fallback
}

// configKeyArg 对齐 Python str(args.get("key", "")) 的取值语义
func configKeyArg(args map[string]any) string {
	if raw, ok := args["key"]; ok {
		return pythonStringValue(raw)
	}
	return ""
}

// nestedConfigValueExact 对齐 Python _get_nested_config_value（capabilities/core.py:54-77）
// 与 nestedConfigValue 的差别是不裁剪路径分段：Python 直接按 "." 切分后逐段取值
func nestedConfigValueExact(source any, key string, defaultValue any) any {
	root, err := valueAsStringKeyedMap(source)
	if err != nil {
		return defaultValue
	}
	current := any(root)
	for _, part := range strings.Split(key, ".") {
		node, ok := current.(map[string]any)
		if !ok {
			return defaultValue
		}
		next, exists := node[part]
		if !exists {
			return defaultValue
		}
		current = next
	}
	return current
}

// pluginActualConfig 读取插件目录下的实际配置文件，对齐 Python component_query.get_plugin_config
// 第二个返回值为 false 表示插件未注册或配置读取失败，对应 Python 侧返回 None
func pluginActualConfig(host *Host, pluginName string) (map[string]any, bool) {
	if host == nil || !host.hasPlugin(pluginName) {
		return nil, false
	}
	configPath, err := host.pluginConfigPath(pluginName)
	if err != nil {
		// 插件已注册但定位不到目录时 Python 的 _get_plugin_path_for_supervisor 返回 None，按空配置处理
		return map[string]any{}, true
	}
	configData := map[string]any{}
	raw, readErr := os.ReadFile(configPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return configData, true
		}
		zap.L().Error("读取插件配置失败", zap.String("plugin_id", pluginName), zap.Error(readErr))
		return nil, false
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if unmarshalErr := toml.Unmarshal(raw, &configData); unmarshalErr != nil {
			zap.L().Error("解析插件配置失败", zap.String("plugin_id", pluginName), zap.Error(unmarshalErr))
			return nil, false
		}
	}
	return configData, true
}

// makeConfigGetPluginHandler 对齐 Python _cap_config_get_plugin（capabilities/core.py:914-936）
func makeConfigGetPluginHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, pluginID, _ string, args map[string]any) (any, error) {
		name := configPluginName(args, pluginID)
		key := configKeyArg(args)
		defaultValue := args["default"]
		configData, ok := pluginActualConfig(host, name)
		if !ok {
			return map[string]any{
				"success": false,
				"value":   defaultValue,
				"error":   fmt.Sprintf("未找到插件 %s 的配置", name),
			}, nil
		}
		if key != "" {
			return map[string]any{"success": true, "value": nestedConfigValueExact(configData, key, defaultValue)}, nil
		}
		return map[string]any{"success": true, "value": configData}, nil
	}
}

// makeConfigGetAllHandler 对齐 Python _cap_config_get_all（capabilities/core.py:947-963）
// 语义是「单个插件的全部配置」，不是全部插件的配置
func makeConfigGetAllHandler(host *Host) CapabilityHandler {
	return func(_ context.Context, pluginID, _ string, args map[string]any) (any, error) {
		name := configPluginName(args, pluginID)
		configData, ok := pluginActualConfig(host, name)
		if !ok {
			return map[string]any{"success": true, "value": map[string]any{}}, nil
		}
		return map[string]any{"success": true, "value": configData}, nil
	}
}
