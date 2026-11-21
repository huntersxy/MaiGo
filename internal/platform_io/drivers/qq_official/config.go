package qq_official

import (
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/platform_io"
)

const (
	PluginID               = "Mai-with-u.qq-adapter"
	GatewayName            = "qq_official_gateway"
	DirectoryName          = "qq_official_adapter"
	SupportedConfigVersion = "0.4.16"
	PlatformQQ             = "qq"
	RouteTypeDuplex        = "duplex"
	ProtocolOfficial       = "qq_official"
	DefaultAPIBaseURL      = "https://api.bot.qq.com"
	DefaultReconnectSec    = 5.0
	MessageIntents         = 1 << 25
)

// PluginOptions 对齐 [plugin]
type PluginOptions struct {
	Enabled       bool
	ConfigVersion string
}

// QQOfficialOptions 对齐 [qq_official]
type QQOfficialOptions struct {
	AppID                string
	AppSecret            string
	APIBaseURL           string
	UnifiedAccountID     string
	SilentUserSync       bool
	SnowlumaIdentityOnly bool
}

// FilterOptions 对齐 [filter]（注意不是 filters）
type FilterOptions struct {
	IgnoreOtherBots bool
}

// AdvancedOptions 对齐 [advanced]
type AdvancedOptions struct {
	ShowDebugInfo     bool
	ReconnectDelaySec float64
}

// MuteOptions 对齐 [mute]
type MuteOptions struct {
	Enabled       bool
	AllowedGroups []string
	AdminUsers    []string
	MinDuration   int
	MaxDuration   int
}

// RecallOptions 对齐 [recall]
type RecallOptions struct {
	Enabled       bool
	AllowedGroups []string
}

// KickOptions 对齐 [kick]
type KickOptions struct {
	Enabled       bool
	AllowedGroups []string
	AdminUsers    []string
}

// Settings 完整配置
type Settings struct {
	Plugin     PluginOptions
	QQOfficial QQOfficialOptions
	Mute       MuteOptions
	Recall     RecallOptions
	Kick       KickOptions
	Filter     FilterOptions
	Advanced   AdvancedOptions
}

// DefaultSettings 返回 schema 默认值（enabled=false, config_version=0.4.16）
func DefaultSettings() Settings {
	return Settings{
		Plugin: PluginOptions{
			Enabled:       false,
			ConfigVersion: SupportedConfigVersion,
		},
		QQOfficial: QQOfficialOptions{
			AppID:                "",
			AppSecret:            "",
			APIBaseURL:           DefaultAPIBaseURL,
			UnifiedAccountID:     "",
			SilentUserSync:       true,
			SnowlumaIdentityOnly: false,
		},
		Mute: MuteOptions{
			Enabled:       true,
			AllowedGroups: []string{},
			AdminUsers:    []string{},
			MinDuration:   60,
			MaxDuration:   2592000,
		},
		Recall: RecallOptions{
			Enabled:       true,
			AllowedGroups: []string{},
		},
		Kick: KickOptions{
			Enabled:       false,
			AllowedGroups: []string{},
			AdminUsers:    []string{},
		},
		Filter: FilterOptions{
			IgnoreOtherBots: true,
		},
		Advanced: AdvancedOptions{
			ShowDebugInfo:     false,
			ReconnectDelaySec: DefaultReconnectSec,
		},
	}
}

// DefaultConfigMap 供 NativeRegistration / Repair 落盘
func DefaultConfigMap() map[string]any {
	return SettingsToMap(DefaultSettings())
}

// SettingsToMap 序列化为嵌套 map（Validate / Inspect）
func SettingsToMap(s Settings) map[string]any {
	return map[string]any{
		"plugin": map[string]any{
			"enabled":        s.Plugin.Enabled,
			"config_version": s.Plugin.ConfigVersion,
		},
		"qq_official": map[string]any{
			"app_id":                 s.QQOfficial.AppID,
			"app_secret":             s.QQOfficial.AppSecret,
			"api_base_url":           s.QQOfficial.APIBaseURL,
			"unified_account_id":     s.QQOfficial.UnifiedAccountID,
			"silent_user_sync":       s.QQOfficial.SilentUserSync,
			"snowluma_identity_only": s.QQOfficial.SnowlumaIdentityOnly,
		},
		"mute": map[string]any{
			"enabled":        s.Mute.Enabled,
			"allowed_groups": append([]string{}, s.Mute.AllowedGroups...),
			"admin_users":    append([]string{}, s.Mute.AdminUsers...),
			"min_duration":   s.Mute.MinDuration,
			"max_duration":   s.Mute.MaxDuration,
		},
		"recall": map[string]any{
			"enabled":        s.Recall.Enabled,
			"allowed_groups": append([]string{}, s.Recall.AllowedGroups...),
		},
		"kick": map[string]any{
			"enabled":        s.Kick.Enabled,
			"allowed_groups": append([]string{}, s.Kick.AllowedGroups...),
			"admin_users":    append([]string{}, s.Kick.AdminUsers...),
		},
		"filter": map[string]any{
			"ignore_other_bots": s.Filter.IgnoreOtherBots,
		},
		"advanced": map[string]any{
			"show_debug_info":     s.Advanced.ShowDebugInfo,
			"reconnect_delay_sec": s.Advanced.ReconnectDelaySec,
		},
	}
}

// ParseSettings 从配置 map 解析；缺键走默认
func ParseSettings(config map[string]any) (Settings, error) {
	normalized, err := platformio.NormalizeNativeConfig(config, ConfigSchemaMap())
	if err != nil {
		return Settings{}, err
	}
	var settings Settings
	plugin := normalized["plugin"]
	settings.Plugin = PluginOptions{Enabled: plugin["enabled"].(bool), ConfigVersion: plugin["config_version"].(string)}
	qq := normalized["qq_official"]
	settings.QQOfficial = QQOfficialOptions{
		AppID: qq["app_id"].(string), AppSecret: qq["app_secret"].(string), APIBaseURL: qq["api_base_url"].(string),
		UnifiedAccountID: qq["unified_account_id"].(string), SilentUserSync: qq["silent_user_sync"].(bool),
		SnowlumaIdentityOnly: qq["snowluma_identity_only"].(bool),
	}
	mute, recall, kick := normalized["mute"], normalized["recall"], normalized["kick"]
	settings.Mute = MuteOptions{
		Enabled: mute["enabled"].(bool), AllowedGroups: mute["allowed_groups"].([]string), AdminUsers: mute["admin_users"].([]string),
		MinDuration: mute["min_duration"].(int), MaxDuration: mute["max_duration"].(int),
	}
	settings.Recall = RecallOptions{Enabled: recall["enabled"].(bool), AllowedGroups: recall["allowed_groups"].([]string)}
	settings.Kick = KickOptions{Enabled: kick["enabled"].(bool), AllowedGroups: kick["allowed_groups"].([]string), AdminUsers: kick["admin_users"].([]string)}
	settings.Filter.IgnoreOtherBots = normalized["filter"]["ignore_other_bots"].(bool)
	settings.Advanced = AdvancedOptions{ShowDebugInfo: normalized["advanced"]["show_debug_info"].(bool),
		ReconnectDelaySec: normalized["advanced"]["reconnect_delay_sec"].(float64)}
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
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case bool:
		if typed {
			return "true"
		}
		return "false"
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

func asFloat(v any) (float64, bool) {
	switch typed := v.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
