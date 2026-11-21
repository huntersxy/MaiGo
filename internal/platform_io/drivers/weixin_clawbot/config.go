package weixin_clawbot

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/platform_io"
)

const (
	PluginID               = "maibot-team.weixin-clawbot-adapter"
	GatewayBaseName        = "weixin_clawbot_gateway"
	DirectoryName          = "weixin_clawbot_adapter"
	SupportedConfigVersion = "0.2.0"
	PlatformWeixin         = "weixin_clawbot"
	RouteTypeDuplex        = "duplex"
	ProtocolILink          = "weixin_ilink"
	MaxAccounts            = 16
	DefaultRetrySeconds    = 5
	DefaultMaxImageMB      = 5
	DefaultBaseURL         = "https://ilinkai.weixin.qq.com"
	DefaultCDNURL          = "https://novac2c.cdn.weixin.qq.com/c2c"
)

// PluginOptions 对齐 [plugin]
type PluginOptions struct {
	Enabled       bool
	ConfigVersion string
}

// WeixinOptions 对齐 [weixin]
type WeixinOptions struct {
	AllowedUsers []string
	RetrySeconds int
	MaxImageMB   int
}

// Settings 完整配置
type Settings struct {
	Plugin PluginOptions
	Weixin WeixinOptions
}

// GatewayName 返回槽位网关名：slot0 基名，slotN 带后缀
func GatewayName(slot int) string {
	if slot <= 0 {
		return GatewayBaseName
	}
	return fmt.Sprintf("%s_%d", GatewayBaseName, slot)
}

// AllGatewayNames 预声明 16 个网关名
func AllGatewayNames() []string {
	names := make([]string, MaxAccounts)
	for i := 0; i < MaxAccounts; i++ {
		names[i] = GatewayName(i)
	}
	return names
}

// DefaultSettings schema 默认值
func DefaultSettings() Settings {
	return Settings{
		Plugin: PluginOptions{
			Enabled:       false,
			ConfigVersion: SupportedConfigVersion,
		},
		Weixin: WeixinOptions{
			AllowedUsers: []string{},
			RetrySeconds: DefaultRetrySeconds,
			MaxImageMB:   DefaultMaxImageMB,
		},
	}
}

// DefaultConfigMap 供 NativeRegistration / Repair
func DefaultConfigMap() map[string]any {
	return SettingsToMap(DefaultSettings())
}

// SettingsToMap 序列化为嵌套 map（Validate / Inspect）
func SettingsToMap(s Settings) map[string]any {
	users := s.Weixin.AllowedUsers
	if users == nil {
		users = []string{}
	}
	return map[string]any{
		"plugin": map[string]any{
			"enabled":        s.Plugin.Enabled,
			"config_version": s.Plugin.ConfigVersion,
		},
		"weixin": map[string]any{
			"allowed_users": append([]string{}, users...),
			"retry_seconds": s.Weixin.RetrySeconds,
			"max_image_mb":  s.Weixin.MaxImageMB,
		},
	}
}

// ParseSettings 从配置 map 解析
func ParseSettings(config map[string]any) (Settings, error) {
	normalized, err := platformio.NormalizeNativeConfig(config, ConfigSchemaMap())
	if err != nil {
		return Settings{}, err
	}
	var settings Settings
	plugin := normalized["plugin"]
	settings.Plugin = PluginOptions{Enabled: plugin["enabled"].(bool), ConfigVersion: plugin["config_version"].(string)}
	wx := normalized["weixin"]
	settings.Weixin = WeixinOptions{
		AllowedUsers: wx["allowed_users"].([]string), RetrySeconds: wx["retry_seconds"].(int), MaxImageMB: wx["max_image_mb"].(int),
	}
	return settings, nil
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asString(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func asBool(v any) bool {
	switch typed := v.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on", "enabled":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func asInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		n := int(typed)
		return n, float64(n) == typed
	case json.Number:
		n, err := strconv.Atoi(string(typed))
		return n, err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		return n, err == nil
	default:
		return 0, false
	}
}

func driverID(gatewayName string) string {
	return "gateway:" + PluginID + ":" + gatewayName
}
