package capabilities

import (
	"encoding/json"
	"fmt"
	"strings"

	"maibot-go/internal/config"
)

// nestedConfigValue 从 map 或 JSON 可序列化对象按点分路径读取配置值
func nestedConfigValue(source any, key string, defaultValue any) any {
	key = strings.TrimSpace(key)
	if key == "" {
		return defaultValue
	}
	root, err := valueAsStringKeyedMap(source)
	if err != nil {
		return defaultValue
	}
	current := any(root)
	for _, part := range strings.Split(key, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return defaultValue
		}
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return defaultValue
			}
			current = next
		default:
			return defaultValue
		}
	}
	return current
}

// ValueAsStringKeyedMap 把任意结构转成字符串键的 map
//
// 导出供父包使用：插件配置可能来自 JSON 反序列化的多种形态，
// 统一recover 成 map[string]any 后再按点分路径取值。
func ValueAsStringKeyedMap(source any) (map[string]any, error) {
	return valueAsStringKeyedMap(source)
}

func valueAsStringKeyedMap(source any) (map[string]any, error) {
	if source == nil {
		return map[string]any{}, nil
	}
	if m, ok := source.(map[string]any); ok {
		return m, nil
	}
	b, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// configRuntimeSlice 把当前运行配置序列化为配置根，供 config.get runtime.* 使用
func configRuntimeSlice() map[string]any {
	snapshot := config.Snapshot()
	if snapshot == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func configLookupRoot(instance BotConfigSource) map[string]any {
	root := map[string]any{
		"runtime": configRuntimeSlice(),
	}
	if instance != nil {
		if slice := instance.BotConfigSlice(); slice != nil {
			root["bot"] = slice
		}
	}
	return root
}

// LookupConfigValue 按点分路径读取配置值（导出给父包使用）
func LookupConfigValue(instance BotConfigSource, key string, defaultValue any) (any, error) {
	return lookupConfigValue(instance, key, defaultValue)
}

func lookupConfigValue(instance BotConfigSource, key string, defaultValue any) (any, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("缺少必要参数 key")
	}
	root := configLookupRoot(instance)
	if strings.HasPrefix(key, "bot.") {
		return nestedConfigValue(root["bot"], strings.TrimPrefix(key, "bot."), defaultValue), nil
	}
	if strings.HasPrefix(key, "runtime.") {
		return nestedConfigValue(root["runtime"], strings.TrimPrefix(key, "runtime."), defaultValue), nil
	}
	if v := nestedConfigValue(root["runtime"], key, nil); v != nil {
		return v, nil
	}
	if bot := root["bot"]; bot != nil {
		if v := nestedConfigValue(bot, key, nil); v != nil {
			return v, nil
		}
	}
	if defaultValue != nil {
		return defaultValue, nil
	}
	return nestedConfigValue(root, key, defaultValue), nil
}
