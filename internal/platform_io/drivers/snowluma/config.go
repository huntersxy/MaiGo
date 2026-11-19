package snowluma

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	PluginID               = "maibot-team.snowluma-adapter"
	GatewayName            = "snowluma_gateway"
	DirectoryName          = "maibot-team_snowluma-adapter"
	SupportedConfigVersion = "2.8.0"
	DefaultClientHost      = "127.0.0.1"
	DefaultClientPort      = 3001
	DefaultReconnectSec    = 5.0
	DefaultHeartbeatSec    = 30.0
	DefaultActionTimeout   = 30.0
	PlatformQQ             = "qq"
	RouteTypeDuplex        = "duplex"
)

// PluginOptions 对齐 [plugin]
type PluginOptions struct {
	Enabled                 bool
	QQFaceParseMode         string
	EnableTextFaceConversion bool
	EnableTextAtConversion  bool
	ConfigVersion           string
}

// ChatAbilities 对齐 [chat_abilities]
type ChatAbilities struct {
	EnablePrivateChatTool bool
	EnablePokeTool        bool
	EnableRecallTool      bool
	EnableMuteTool        bool
	EnableEmojiLikeTool   bool
	EnableForwardTool     bool
}

// ClientConfig 对齐 [client]
type ClientConfig struct {
	ClientType        string
	Server            string
	Port              int
	Token             string
	HeartbeatInterval float64
	ReconnectDelaySec float64
	ActionTimeoutSec  float64
	ConnectionID      string
}

// NoticeConfig 对齐 [notice]
type NoticeConfig struct {
	Enabled                 bool
	EnablePoke              bool
	EnableFriendRecall      bool
	EnableGroupRecall       bool
	EnableGroupBan          bool
	EnableGroupMsgEmojiLike bool
	EnableGroupUpload       bool
	EnableGroupIncrease     bool
	EnableGroupDecrease     bool
	EnableGroupAdmin        bool
	EnableEssence           bool
	EnableGroupName         bool
}

// FiltersConfig 对齐 [filters]
type FiltersConfig struct {
	BanUserID              []string
	BanQQBot               bool
	RegexFilterEnabled     bool
	RegexFilterMode        string
	RegexFilterPatterns    []string
	RegexFilterShowDropped bool
}

// DebugConfig 对齐 [debug]
type DebugConfig struct {
	EnableAdaDebugRawMessageLog         bool
	EnableAdaDebugRawOutboundMessageLog bool
	IgnoreSelfMessage                   bool
}

// MuteConfig 对齐 [mute]
type MuteConfig struct {
	AdminUsers     []string
	AllowedGroups  []string
	MinDuration    int
	MaxDuration    int
}

// Settings 完整配置
type Settings struct {
	Plugin        PluginOptions
	ChatAbilities ChatAbilities
	Client        ClientConfig
	Notice        NoticeConfig
	Filters       FiltersConfig
	Debug         DebugConfig
	Mute          MuteConfig
}

// DefaultSettings 返回对齐 2.8.0 的默认配置（plugin.enabled=false）
func DefaultSettings() Settings {
	return Settings{
		Plugin: PluginOptions{
			Enabled:         false,
			QQFaceParseMode: "description",
			ConfigVersion:   SupportedConfigVersion,
		},
		Client: ClientConfig{
			ClientType:        "auto",
			Server:            DefaultClientHost,
			Port:              DefaultClientPort,
			HeartbeatInterval: DefaultHeartbeatSec,
			ReconnectDelaySec: DefaultReconnectSec,
			ActionTimeoutSec:  DefaultActionTimeout,
		},
		Notice: NoticeConfig{
			Enabled:                 true,
			EnablePoke:              true,
			EnableFriendRecall:      true,
			EnableGroupRecall:       true,
			EnableGroupBan:          true,
			EnableGroupMsgEmojiLike: true,
			EnableGroupUpload:       true,
			EnableGroupIncrease:     true,
			EnableGroupDecrease:     true,
			EnableGroupAdmin:        true,
			EnableEssence:           true,
			EnableGroupName:         true,
		},
		Filters: FiltersConfig{
			BanUserID:           []string{},
			RegexFilterMode:     "blacklist",
			RegexFilterPatterns: []string{},
		},
		Mute: MuteConfig{
			AdminUsers:    []string{},
			AllowedGroups: []string{},
			MinDuration:   60,
			MaxDuration:   2592000,
		},
	}
}

// DefaultConfigMap 供 NativeRegistration.DefaultConfig
func DefaultConfigMap() map[string]any {
	return SettingsToMap(DefaultSettings())
}

// SettingsToMap 序列化为嵌套 map（落盘/Inspect）
func SettingsToMap(s Settings) map[string]any {
	return map[string]any{
		"plugin": map[string]any{
			"enabled":                     s.Plugin.Enabled,
			"qq_face_parse_mode":          s.Plugin.QQFaceParseMode,
			"enable_text_face_conversion": s.Plugin.EnableTextFaceConversion,
			"enable_text_at_conversion":   s.Plugin.EnableTextAtConversion,
			"config_version":              s.Plugin.ConfigVersion,
		},
		"chat_abilities": map[string]any{
			"enable_private_chat_tool": s.ChatAbilities.EnablePrivateChatTool,
			"enable_poke_tool":         s.ChatAbilities.EnablePokeTool,
			"enable_recall_tool":       s.ChatAbilities.EnableRecallTool,
			"enable_mute_tool":         s.ChatAbilities.EnableMuteTool,
			"enable_emoji_like_tool":   s.ChatAbilities.EnableEmojiLikeTool,
			"enable_forward_tool":      s.ChatAbilities.EnableForwardTool,
		},
		"client": map[string]any{
			"client_type":         s.Client.ClientType,
			"server":              s.Client.Server,
			"port":                s.Client.Port,
			"token":               s.Client.Token,
			"heartbeat_interval":  s.Client.HeartbeatInterval,
			"reconnect_delay_sec": s.Client.ReconnectDelaySec,
			"action_timeout_sec":  s.Client.ActionTimeoutSec,
			"connection_id":       s.Client.ConnectionID,
		},
		"notice": map[string]any{
			"enabled":                    s.Notice.Enabled,
			"enable_poke":                 s.Notice.EnablePoke,
			"enable_friend_recall":        s.Notice.EnableFriendRecall,
			"enable_group_recall":         s.Notice.EnableGroupRecall,
			"enable_group_ban":            s.Notice.EnableGroupBan,
			"enable_group_msg_emoji_like": s.Notice.EnableGroupMsgEmojiLike,
			"enable_group_upload":         s.Notice.EnableGroupUpload,
			"enable_group_increase":       s.Notice.EnableGroupIncrease,
			"enable_group_decrease":       s.Notice.EnableGroupDecrease,
			"enable_group_admin":          s.Notice.EnableGroupAdmin,
			"enable_essence":              s.Notice.EnableEssence,
			"enable_group_name":           s.Notice.EnableGroupName,
		},
		"filters": map[string]any{
			"ban_user_id":               append([]string{}, s.Filters.BanUserID...),
			"ban_qq_bot":                s.Filters.BanQQBot,
			"regex_filter_enabled":      s.Filters.RegexFilterEnabled,
			"regex_filter_mode":         s.Filters.RegexFilterMode,
			"regex_filter_patterns":     append([]string{}, s.Filters.RegexFilterPatterns...),
			"regex_filter_show_dropped": s.Filters.RegexFilterShowDropped,
		},
		"debug": map[string]any{
			"enable_ada_debug_raw_message_log":          s.Debug.EnableAdaDebugRawMessageLog,
			"enable_ada_debug_raw_outbound_message_log": s.Debug.EnableAdaDebugRawOutboundMessageLog,
			"ignore_self_message":                       s.Debug.IgnoreSelfMessage,
		},
		"mute": map[string]any{
			"admin_users":    append([]string{}, s.Mute.AdminUsers...),
			"allowed_groups": append([]string{}, s.Mute.AllowedGroups...),
			"min_duration":   s.Mute.MinDuration,
			"max_duration":   s.Mute.MaxDuration,
		},
	}
}

// ParseSettings 从原始 map 解析并迁移旧键
func ParseSettings(raw map[string]any) (Settings, error) {
	s := DefaultSettings()
	if raw == nil {
		return s, nil
	}
	migrated := migrateLegacyConfig(raw)
	if plugin := asMap(migrated["plugin"]); plugin != nil {
		s.Plugin.Enabled = asBool(plugin["enabled"], false)
		s.Plugin.QQFaceParseMode = firstNonEmpty(asString(plugin["qq_face_parse_mode"]), "description")
		s.Plugin.EnableTextFaceConversion = asBool(plugin["enable_text_face_conversion"], false)
		s.Plugin.EnableTextAtConversion = asBool(plugin["enable_text_at_conversion"], false)
		version := asString(plugin["config_version"])
		if version == "" {
			version = SupportedConfigVersion
		}
		s.Plugin.ConfigVersion = version
	}
	if chat := asMap(migrated["chat_abilities"]); chat != nil {
		s.ChatAbilities.EnablePrivateChatTool = asBool(chat["enable_private_chat_tool"], false)
		s.ChatAbilities.EnablePokeTool = asBool(chat["enable_poke_tool"], false)
		s.ChatAbilities.EnableRecallTool = asBool(chat["enable_recall_tool"], false)
		s.ChatAbilities.EnableMuteTool = asBool(chat["enable_mute_tool"], false)
		s.ChatAbilities.EnableEmojiLikeTool = asBool(chat["enable_emoji_like_tool"], false)
		s.ChatAbilities.EnableForwardTool = asBool(chat["enable_forward_tool"], false)
	}
	if client := asMap(migrated["client"]); client != nil {
		s.Client.ClientType = normalizeClientType(asString(client["client_type"]))
		s.Client.Server = firstNonEmpty(asString(client["server"]), DefaultClientHost)
		s.Client.Port = asInt(client["port"], DefaultClientPort)
		s.Client.Token = asString(client["token"])
		s.Client.HeartbeatInterval = asFloat(client["heartbeat_interval"], DefaultHeartbeatSec)
		s.Client.ReconnectDelaySec = asFloat(client["reconnect_delay_sec"], DefaultReconnectSec)
		s.Client.ActionTimeoutSec = asFloat(client["action_timeout_sec"], DefaultActionTimeout)
		s.Client.ConnectionID = asString(client["connection_id"])
	}
	if notice := asMap(migrated["notice"]); notice != nil {
		s.Notice.Enabled = asBool(notice["enabled"], true)
		s.Notice.EnablePoke = asBool(notice["enable_poke"], true)
		s.Notice.EnableFriendRecall = asBool(notice["enable_friend_recall"], true)
		s.Notice.EnableGroupRecall = asBool(notice["enable_group_recall"], true)
		s.Notice.EnableGroupBan = asBool(notice["enable_group_ban"], true)
		s.Notice.EnableGroupMsgEmojiLike = asBool(notice["enable_group_msg_emoji_like"], true)
		s.Notice.EnableGroupUpload = asBool(notice["enable_group_upload"], true)
		s.Notice.EnableGroupIncrease = asBool(notice["enable_group_increase"], true)
		s.Notice.EnableGroupDecrease = asBool(notice["enable_group_decrease"], true)
		s.Notice.EnableGroupAdmin = asBool(notice["enable_group_admin"], true)
		s.Notice.EnableEssence = asBool(notice["enable_essence"], true)
		s.Notice.EnableGroupName = asBool(notice["enable_group_name"], true)
	}
	if filters := asMap(migrated["filters"]); filters != nil {
		s.Filters.BanUserID = asStringSlice(filters["ban_user_id"])
		s.Filters.BanQQBot = asBool(filters["ban_qq_bot"], false)
		s.Filters.RegexFilterEnabled = asBool(filters["regex_filter_enabled"], false)
		s.Filters.RegexFilterMode = normalizeRegexMode(asString(filters["regex_filter_mode"]))
		s.Filters.RegexFilterPatterns = asStringSlice(filters["regex_filter_patterns"])
		s.Filters.RegexFilterShowDropped = asBool(filters["regex_filter_show_dropped"], false)
	}
	if debug := asMap(migrated["debug"]); debug != nil {
		s.Debug.EnableAdaDebugRawMessageLog = asBool(debug["enable_ada_debug_raw_message_log"], false)
		s.Debug.EnableAdaDebugRawOutboundMessageLog = asBool(debug["enable_ada_debug_raw_outbound_message_log"], false)
		s.Debug.IgnoreSelfMessage = asBool(debug["ignore_self_message"], false)
	}
	if mute := asMap(migrated["mute"]); mute != nil {
		s.Mute.AdminUsers = asStringSlice(mute["admin_users"])
		s.Mute.AllowedGroups = asStringSlice(mute["allowed_groups"])
		s.Mute.MinDuration = asInt(mute["min_duration"], 60)
		s.Mute.MaxDuration = asInt(mute["max_duration"], 2592000)
	}
	if s.Plugin.ConfigVersion != SupportedConfigVersion && s.Plugin.ConfigVersion != "" {
		s.Plugin.ConfigVersion = SupportedConfigVersion
	}
	return s, nil
}

func migrateLegacyConfig(raw map[string]any) map[string]any {
	out := cloneMap(raw)
	plugin := asMap(out["plugin"])
	if plugin == nil {
		plugin = map[string]any{}
	} else {
		plugin = cloneMap(plugin)
	}
	debug := asMap(out["debug"])
	if debug == nil {
		debug = map[string]any{}
	} else {
		debug = cloneMap(debug)
	}
	chatAbilities := asMap(out["chat_abilities"])
	if chatAbilities == nil {
		chatAbilities = map[string]any{}
	} else {
		chatAbilities = cloneMap(chatAbilities)
	}
	filters := asMap(out["filters"])
	if filters == nil {
		filters = map[string]any{}
	} else {
		filters = cloneMap(filters)
	}
	notice := asMap(out["notice"])
	if notice == nil {
		notice = map[string]any{}
	}
	mute := asMap(out["mute"])
	if mute == nil {
		mute = map[string]any{}
	}
	client := asMap(out["client"])
	if client == nil {
		client = map[string]any{}
	} else {
		client = cloneMap(client)
	}

	abilityKeys := []string{
		"enable_private_chat_tool", "enable_poke_tool", "enable_recall_tool",
		"enable_mute_tool", "enable_emoji_like_tool", "enable_forward_tool",
	}
	for _, key := range abilityKeys {
		if value, ok := plugin[key]; ok {
			delete(plugin, key)
			if _, exists := chatAbilities[key]; !exists {
				chatAbilities[key] = value
			}
		}
	}
	debugKeys := []string{
		"enable_ada_debug_raw_message_log",
		"enable_ada_debug_raw_outbound_message_log",
		"ignore_self_message",
	}
	for _, key := range debugKeys {
		if value, ok := plugin[key]; ok {
			delete(plugin, key)
			if _, exists := debug[key]; !exists {
				debug[key] = value
			}
		}
	}
	if value, ok := filters["ignore_self_message"]; ok {
		delete(filters, "ignore_self_message")
		if _, exists := debug["ignore_self_message"]; !exists {
			debug["ignore_self_message"] = value
		}
	}
	if chat := asMap(out["chat"]); chat != nil {
		if _, ok := filters["ban_qq_bot"]; !ok {
			if value, exists := chat["ban_qq_bot"]; exists {
				filters["ban_qq_bot"] = value
			}
		}
	}

	var legacy map[string]any
	for _, key := range []string{"luma_client", "napcat_server", "connection"} {
		if section := asMap(out[key]); section != nil && len(section) > 0 {
			legacy = section
			delete(out, key)
			break
		}
	}
	if legacy != nil {
		known := map[string]struct{}{
			"client_type": {}, "server": {}, "port": {}, "token": {},
			"heartbeat_interval": {}, "reconnect_delay_sec": {},
			"action_timeout_sec": {}, "connection_id": {},
		}
		for legacyKey, legacyValue := range legacy {
			target := legacyKey
			switch legacyKey {
			case "host":
				target = "server"
			case "access_token":
				target = "token"
			}
			if _, ok := known[target]; !ok {
				continue
			}
			if _, exists := client[target]; !exists {
				client[target] = legacyValue
			}
		}
	}

	if version := asString(plugin["config_version"]); version != "" && version != SupportedConfigVersion {
		plugin["config_version"] = SupportedConfigVersion
	}
	out["plugin"] = plugin
	out["chat_abilities"] = chatAbilities
	out["client"] = client
	out["debug"] = debug
	out["filters"] = filters
	out["notice"] = notice
	out["mute"] = mute
	return out
}

func (c ClientConfig) BuildWSURL() string {
	return fmt.Sprintf("ws://%s:%d", c.Server, c.Port)
}

func normalizeClientType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "onebot_v11", "napcat", "snowluma":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "auto"
	}
}

func normalizeRegexMode(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "whitelist") {
		return "whitelist"
	}
	return "blacklist"
}

func asMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
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

func asBool(value any, fallback bool) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on", "enabled":
			return true
		case "false", "0", "no", "off", "disabled":
			return false
		}
	}
	return fallback
}

func asInt(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return n
		}
	}
	return fallback
}

func asFloat(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return n
		}
	}
	return fallback
}

func asStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := asString(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return []string{}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
