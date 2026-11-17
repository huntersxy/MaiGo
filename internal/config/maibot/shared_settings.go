package maibot

// 宿主公共行为与保留策略，对齐 src/config/shared_settings.py。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SharedSettingsPath 公共设置文件；仅宿主下发目录时存在
func SharedSettingsPath() string {
	catalog := SharedCatalogPath()
	if catalog == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(catalog), "config.toml")
}

// MergeSharedSettingsDoc 把宿主公共保留策略与界面设置并入 Bot 配置映射
func MergeSharedSettingsDoc(local map[string]any) (map[string]any, error) {
	path := SharedSettingsPath()
	if path == "" {
		return cloneTOMLMap(local), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cloneTOMLMap(local), nil
		}
		return nil, err
	}
	var shared map[string]any
	if err := toml.Unmarshal(raw, &shared); err != nil {
		return nil, err
	}
	result := cloneTOMLMap(local)
	if retention, ok := shared["retention"].(map[string]any); ok {
		mergeSharedRetention(result, retention)
	}
	if webui, ok := shared["webui"].(map[string]any); ok {
		target, _ := result["webui"].(map[string]any)
		if target == nil {
			target = map[string]any{}
			result["webui"] = target
		}
		for _, key := range []string{"webui_style", "enforce_public_outbound_url"} {
			if value, exists := webui[key]; exists {
				target[key] = value
			}
		}
	}
	return result, nil
}

func mergeSharedRetention(local, retention map[string]any) {
	for _, section := range []struct {
		key    string
		parent string
		field  string
	}{
		{"images", "visual", "image_cache_cleanup"},
		{"emoji", "emoji", "cache_cleanup"},
	} {
		values, ok := retention[section.key].(map[string]any)
		if !ok {
			continue
		}
		parent, _ := local[section.parent].(map[string]any)
		if parent == nil {
			parent = map[string]any{}
			local[section.parent] = parent
		}
		parent[section.field] = cloneTOMLMap(values)
	}
	if logs, ok := retention["logs"].(map[string]any); ok {
		target, _ := local["log"].(map[string]any)
		if target == nil {
			target = map[string]any{}
			local["log"] = target
		}
		for key, value := range logs {
			target[key] = value
		}
	}
}

// StripSharedSettingsDoc 写回时移除宿主接管的公共字段
func StripSharedSettingsDoc(local map[string]any) {
	for _, section := range []struct {
		key    string
		parent string
		field  string
	}{
		{"images", "visual", "image_cache_cleanup"},
		{"emoji", "emoji", "cache_cleanup"},
	} {
		if parent, ok := local[section.parent].(map[string]any); ok {
			delete(parent, section.field)
		}
	}
	if log, ok := local["log"].(map[string]any); ok {
		for _, key := range []string{"log_file_max_bytes", "max_log_files", "log_cleanup_days"} {
			delete(log, key)
		}
	}
	if webui, ok := local["webui"].(map[string]any); ok {
		delete(webui, "webui_style")
		delete(webui, "enforce_public_outbound_url")
		if len(webui) == 0 {
			delete(local, "webui")
		}
	}
}

// SharedRetentionDefaults 公共保留策略默认值，供宿主生成公共设置
func SharedRetentionDefaults() map[string]any {
	return map[string]any{
		"images": map[string]any{
			"enabled": true, "check_interval_hours": 6.0,
			"image_file_retention_days": 14, "no_file_result_retention_days": 30,
		},
		"emoji": map[string]any{
			"enabled": true, "check_interval_hours": 6.0,
			"emoji_file_retention_days": 30, "no_file_record_retention_days": 30,
		},
		"logs": map[string]any{"log_file_max_bytes": 5242880, "max_log_files": 30, "log_cleanup_days": 30},
	}
}

// SharedWebUIDefaults 公共界面设置默认值
func SharedWebUIDefaults() map[string]any {
	return map[string]any{"webui_style": 2, "enforce_public_outbound_url": true}
}

// TrimSharedPath 便于调用方规范路径
func TrimSharedPath(path string) string { return strings.TrimSpace(path) }

// ValidateRetention 校验公共保留策略字段完整且取值有效
func ValidateRetention(data map[string]any) (map[string]any, error) {
	defaults := SharedRetentionDefaults()
	if len(data) != len(defaults) {
		return nil, errors.New("公共保留策略必须包含 images、emoji 和 logs")
	}
	for section, sectionDefaults := range defaults {
		values, ok := data[section].(map[string]any)
		if !ok || len(values) != len(sectionDefaults.(map[string]any)) {
			return nil, fmt.Errorf("公共保留策略 %s 字段不完整或含未知字段", section)
		}
		for key, value := range values {
			if key == "enabled" {
				if _, ok := value.(bool); !ok {
					return nil, fmt.Errorf("%s.%s 必须是布尔值", section, key)
				}
				continue
			}
			if key == "check_interval_hours" {
				number, ok := numberAsFloat(value)
				if !ok || number < 1.0/60.0 || number > 8760 {
					return nil, fmt.Errorf("%s.%s 必须在 1 分钟到 1 年之间", section, key)
				}
				continue
			}
			number, ok := integerValue(value)
			if !ok || number < 1 {
				return nil, fmt.Errorf("%s.%s 必须是正整数", section, key)
			}
			values[key] = number
		}
	}
	return cloneTOMLMap(data), nil
}

// ValidateSharedWebUI 校验公共界面设置
func ValidateSharedWebUI(data map[string]any) (map[string]any, error) {
	style, ok := integerValue(data["webui_style"])
	if !ok || style < 0 || style > 2 {
		return nil, errors.New("界面风格必须是 0、1 或 2")
	}
	enforce, ok := data["enforce_public_outbound_url"].(bool)
	if !ok {
		return nil, errors.New("公网出站 URL 校验必须是布尔值")
	}
	return map[string]any{"webui_style": style, "enforce_public_outbound_url": enforce}, nil
}

func integerValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}

func numberAsFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}
