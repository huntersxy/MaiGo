package qq_official

import (
	"fmt"
	"strconv"
	"strings"
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
	AppID               string
	AppSecret           string
	APIBaseURL          string
	UnifiedAccountID    string
	SilentUserSync      bool
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
	settings := DefaultSettings()
	if config == nil {
		return settings, nil
	}
	if plugin, ok := asMap(config["plugin"]); ok {
		if v, ok := plugin["enabled"]; ok {
			settings.Plugin.Enabled = asBool(v)
		}
		if v := strings.TrimSpace(asString(plugin["config_version"])); v != "" {
			settings.Plugin.ConfigVersion = v
		}
	}
	if qq, ok := asMap(config["qq_official"]); ok {
		settings.QQOfficial.AppID = strings.TrimSpace(asString(qq["app_id"]))
		settings.QQOfficial.AppSecret = strings.TrimSpace(asString(qq["app_secret"]))
		if v := strings.TrimSpace(asString(qq["api_base_url"])); v != "" {
			settings.QQOfficial.APIBaseURL = strings.TrimRight(v, "/")
		}
		settings.QQOfficial.UnifiedAccountID = strings.TrimSpace(asString(qq["unified_account_id"]))
		if v, ok := qq["silent_user_sync"]; ok {
			settings.QQOfficial.SilentUserSync = asBool(v)
		}
		if v, ok := qq["snowluma_identity_only"]; ok {
			settings.QQOfficial.SnowlumaIdentityOnly = asBool(v)
		}
	}
	if filter, ok := asMap(config["filter"]); ok {
		if v, ok := filter["ignore_other_bots"]; ok {
			settings.Filter.IgnoreOtherBots = asBool(v)
		}
	}
if adv, ok := asMap(config["advanced"]); ok {
			if v, ok := adv["show_debug_info"]; ok {
				settings.Advanced.ShowDebugInfo = asBool(v)
			}
			if f, ok := asFloat(adv["reconnect_delay_sec"]); ok {
				settings.Advanced.ReconnectDelaySec = clampFloat(f, 1, 300, DefaultReconnectSec)
			}
		}
		if mute, ok := asMap(config["mute"]); ok {
			if v, ok := mute["enabled"]; ok {
				settings.Mute.Enabled = asBool(v)
			}
			settings.Mute.AllowedGroups = asStringSlice(mute["allowed_groups"])
			settings.Mute.AdminUsers = asStringSlice(mute["admin_users"])
			if n, ok := asInt(mute["min_duration"]); ok {
				settings.Mute.MinDuration = clampInt(n, 1, 2592000, 60)
			}
			if n, ok := asInt(mute["max_duration"]); ok {
				settings.Mute.MaxDuration = clampInt(n, 1, 2592000, 2592000)
			}
		}
		if recall, ok := asMap(config["recall"]); ok {
			if v, ok := recall["enabled"]; ok {
				settings.Recall.Enabled = asBool(v)
			}
			settings.Recall.AllowedGroups = asStringSlice(recall["allowed_groups"])
		}
		if kick, ok := asMap(config["kick"]); ok {
			if v, ok := kick["enabled"]; ok {
				settings.Kick.Enabled = asBool(v)
			}
			settings.Kick.AllowedGroups = asStringSlice(kick["allowed_groups"])
			settings.Kick.AdminUsers = asStringSlice(kick["admin_users"])
		}
		if settings.Plugin.ConfigVersion == "" {
			settings.Plugin.ConfigVersion = SupportedConfigVersion
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

func asStringSlice(v any) []string {
		switch typed := v.(type) {
		case []string:
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				if s := strings.TrimSpace(item); s != "" {
					out = append(out, s)
				}
			}
			return out
		case []any:
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				if s := strings.TrimSpace(asString(item)); s != "" {
					out = append(out, s)
				}
			}
			return out
		default:
			return []string{}
		}
	}

	func clampInt(v, lo, hi, def int) int {
		if v < lo || v > hi {
			return def
		}
		return v
	}

	func clampFloat(v, lo, hi, def float64) float64 {
	if v < lo || v > hi {
		return def
	}
	return v
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
