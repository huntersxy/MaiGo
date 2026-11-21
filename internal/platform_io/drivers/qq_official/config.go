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
	SupportedConfigVersion = "0.5.3"
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
	EnableMapping        bool
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

type ConfigTarget struct {
	Platform string
	Type     string
	ID       string
	Name     string
}

// MuteOptions 对齐 [mute]
type MuteOptions struct {
	Enabled       bool
	AllowedGroups []ConfigTarget
	AdminUsers    []ConfigTarget
	MinDuration   int
	MaxDuration   int
}

// RecallOptions 对齐 [recall]
type RecallOptions struct {
	Enabled       bool
	AllowedGroups []ConfigTarget
}

// KickOptions 对齐 [kick]
type KickOptions struct {
	Enabled       bool
	AllowedGroups []ConfigTarget
	AdminUsers    []ConfigTarget
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

// DefaultSettings 返回原生配置默认值。
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
			AllowedGroups: []ConfigTarget{},
			AdminUsers:    []ConfigTarget{},
			MinDuration:   60,
			MaxDuration:   2592000,
		},
		Recall: RecallOptions{
			Enabled:       true,
			AllowedGroups: []ConfigTarget{},
		},
		Kick: KickOptions{
			Enabled:       false,
			AllowedGroups: []ConfigTarget{},
			AdminUsers:    []ConfigTarget{},
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
			"enable_mapping":         s.QQOfficial.EnableMapping,
			"silent_user_sync":       s.QQOfficial.SilentUserSync,
			"snowluma_identity_only": s.QQOfficial.SnowlumaIdentityOnly,
		},
		"mute": map[string]any{
			"enabled":        s.Mute.Enabled,
			"allowed_groups": configTargetMaps(s.Mute.AllowedGroups),
			"admin_users":    configTargetMaps(s.Mute.AdminUsers),
			"min_duration":   s.Mute.MinDuration,
			"max_duration":   s.Mute.MaxDuration,
		},
		"recall": map[string]any{
			"enabled":        s.Recall.Enabled,
			"allowed_groups": configTargetMaps(s.Recall.AllowedGroups),
		},
		"kick": map[string]any{
			"enabled":        s.Kick.Enabled,
			"allowed_groups": configTargetMaps(s.Kick.AllowedGroups),
			"admin_users":    configTargetMaps(s.Kick.AdminUsers),
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
		EnableMapping:        qq["enable_mapping"].(bool),
		SnowlumaIdentityOnly: qq["snowluma_identity_only"].(bool),
	}
	mute, recall, kick := normalized["mute"], normalized["recall"], normalized["kick"]
	settings.Mute = MuteOptions{
		Enabled:     mute["enabled"].(bool),
		MinDuration: mute["min_duration"].(int), MaxDuration: mute["max_duration"].(int),
	}
	settings.Recall.Enabled = recall["enabled"].(bool)
	settings.Kick.Enabled = kick["enabled"].(bool)
	for _, list := range []struct {
		kind   string
		rows   []map[string]any
		target *[]ConfigTarget
	}{
		{"group", mute["allowed_groups"].([]map[string]any), &settings.Mute.AllowedGroups},
		{"user", mute["admin_users"].([]map[string]any), &settings.Mute.AdminUsers},
		{"group", recall["allowed_groups"].([]map[string]any), &settings.Recall.AllowedGroups},
		{"group", kick["allowed_groups"].([]map[string]any), &settings.Kick.AllowedGroups},
		{"user", kick["admin_users"].([]map[string]any), &settings.Kick.AdminUsers},
	} {
		*list.target = make([]ConfigTarget, 0, len(list.rows))
		for _, row := range list.rows {
			entry := ConfigTarget{Platform: asString(row["platform"]), Type: asString(row["type"]), ID: asString(row["id"]), Name: asString(row["name"])}
			if entry.Platform != PlatformQQ || entry.Type != list.kind || strings.TrimSpace(entry.ID) == "" {
				return Settings{}, fmt.Errorf("目标必须是有效的 QQ %s 身份", list.kind)
			}
			*list.target = append(*list.target, entry)
		}
	}
	settings.Filter.IgnoreOtherBots = normalized["filter"]["ignore_other_bots"].(bool)
	settings.Advanced = AdvancedOptions{ShowDebugInfo: normalized["advanced"]["show_debug_info"].(bool),
		ReconnectDelaySec: normalized["advanced"]["reconnect_delay_sec"].(float64)}
	return settings, nil
}

func configTargetMaps(targets []ConfigTarget) []map[string]any {
	entries := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		entries = append(entries, map[string]any{"platform": target.Platform, "type": target.Type, "id": target.ID, "name": target.Name})
	}
	return entries
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
