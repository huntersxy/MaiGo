package localoperator

import (
	"reflect"
	"strings"
)

const (
	BotConsolePlatform  = "bot_console"
	BotConsoleUserID    = "local_operator"
	BotConsoleBotID     = "__maibot_console_bot__"
	MaisakaCLIPlatform  = "maisaka_cli"
	MaisakaCLIBotID     = "__maisaka_cli_bot__"
	LocalOperatorConfig = "is_local_operator"
	// PluginRelayConfigKey 插件代为投递的消息标记，对齐 MaiBot PLUGIN_RELAY_CONFIG_KEY
	PluginRelayConfigKey = "relayed_by_plugin"
)

// LocalPlatformBotIDs 本地平台的固定机器人 ID
var LocalPlatformBotIDs = map[string]string{
	BotConsolePlatform: BotConsoleBotID,
	MaisakaCLIPlatform: MaisakaCLIBotID,
}

// IsLocalOperator 判断消息是否来自主程序创建的本地操作员终端
func IsLocalOperator(platform string, additionalConfig map[string]any) bool {
	if platform != BotConsolePlatform {
		return false
	}
	value, ok := additionalConfig[LocalOperatorConfig]
	return ok && value == true
}

// IsPluginRelayed 判断消息是否由插件代为投递
func IsPluginRelayed(additionalConfig map[string]any) bool {
	return pyBool(additionalConfig[PluginRelayConfigKey])
}

// BuildScopedUserID 构造插件管理权限使用的跨平台用户 ID
func BuildScopedUserID(platform, userID string) string {
	normalizedPlatform := strings.ToLower(strings.TrimSpace(platform))
	normalizedUserID := strings.TrimSpace(userID)
	if normalizedPlatform == "" || normalizedUserID == "" {
		return ""
	}
	return normalizedPlatform + ":" + normalizedUserID
}

// NormalizeOperatorPermissions 规范化插件管理权限列表
// 与 BuildScopedUserID 保持一致：仅平台前缀转小写，user_id 保持原样
func NormalizeOperatorPermissions(permissions []string) map[string]struct{} {
	normalized := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		permission = strings.TrimSpace(permission)
		if permission == "" {
			continue
		}
		platform, userID, _ := strings.Cut(permission, ":")
		normalized[strings.ToLower(strings.TrimSpace(platform))+":"+strings.TrimSpace(userID)] = struct{}{}
	}
	return normalized
}

// HasPluginManagementPermission 判断用户是否具有插件生命周期管理权限
func HasPluginManagementPermission(platform, userID string, permissions []string, localOperator, relayedByPlugin bool) bool {
	if relayedByPlugin {
		return false
	}
	if localOperator {
		return true
	}
	scopedUserID := BuildScopedUserID(platform, userID)
	if scopedUserID == "" {
		return false
	}
	_, ok := NormalizeOperatorPermissions(permissions)[scopedUserID]
	return ok
}

// CommandPermissionRule 单个受保护命令的用户与会话白名单
type CommandPermissionRule struct {
	AllowUsers []string
	AllowChats []string
}

// HasCommandPermission 判断用户或聊天是否被允许执行受保护命令
func HasCommandPermission(commandID, platform, userID, sessionID string, operatorPermissions []string, commandPermissions map[string]CommandPermissionRule, localOperator, relayedByPlugin bool) bool {
	if relayedByPlugin {
		return false
	}
	if HasPluginManagementPermission(platform, userID, operatorPermissions, localOperator, false) {
		return true
	}
	rule, ok := commandPermissions[commandID]
	if !ok {
		return false
	}
	scopedUserID := BuildScopedUserID(platform, userID)
	if _, allowed := NormalizeOperatorPermissions(rule.AllowUsers)[scopedUserID]; allowed {
		return true
	}
	for _, chatID := range rule.AllowChats {
		if strings.TrimSpace(chatID) == sessionID {
			return true
		}
	}
	return false
}

// pyBool 对齐 Python bool 对常见 JSON 值的真值判断
func pyBool(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case int8:
		return v != 0
	case int16:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case uint:
		return v != 0
	case uint8:
		return v != 0
	case uint16:
		return v != 0
	case uint32:
		return v != 0
	case uint64:
		return v != 0
	case float32:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		rv := reflect.ValueOf(value)
		switch rv.Kind() {
		case reflect.Slice, reflect.Array, reflect.Map:
			return rv.Len() > 0
		}
		return true
	}
}
