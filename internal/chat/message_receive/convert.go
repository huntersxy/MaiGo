package messagereceive

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// RouteInfo 对齐 platformio.RouteKey 的消息路由字段
type RouteInfo struct {
	Platform  string
	AccountID string
	Scope     string
}

var routeAccountKeys = []string{
	"platform_io_account_id",
	"account_id",
	"self_id",
	"bot_account",
}

var routeScopeKeys = []string{
	"platform_io_scope",
	"route_scope",
	"adapter_scope",
	"connection_id",
}

// RouteInfoFromMessageDict 对齐 RouteKeyFactory.from_message_dict：
// 顶层存在 account_id 时严格使用顶层 account/scope，不再回退旧字段
func RouteInfoFromMessageDict(raw map[string]any) (RouteInfo, error) {
	if raw == nil {
		return RouteInfo{}, fmt.Errorf("消息字典为空")
	}
	platform := strings.TrimSpace(stringValue(raw["platform"]))
	if platform == "" {
		return RouteInfo{}, fmt.Errorf("消息缺少平台信息")
	}
	info := RouteInfo{Platform: platform}
	if value, ok := raw["account_id"]; ok {
		// 正式字段存在时不再回退旧字段；None 允许为空，非字符串报错
		if value != nil {
			account, ok := value.(string)
			if !ok {
				return RouteInfo{}, fmt.Errorf("顶层 account_id 必须是字符串")
			}
			info.AccountID = strings.TrimSpace(account)
		}
		if scope, ok := raw["scope"]; ok && scope != nil {
			text, ok := scope.(string)
			if !ok {
				return RouteInfo{}, fmt.Errorf("顶层 scope 必须是字符串")
			}
			info.Scope = strings.TrimSpace(text)
		}
		return info, nil
	}

	messageInfo, _ := raw["message_info"].(map[string]any)
	additionalConfig, _ := messageInfo["additional_config"].(map[string]any)
	// 对齐 Python 优先级：消息字典 → message_info → additional_config
	info.AccountID = firstNonEmptyRouteValue(
		firstRouteString(raw, routeAccountKeys),
		firstRouteString(messageInfo, routeAccountKeys),
		firstRouteString(additionalConfig, routeAccountKeys),
	)
	info.Scope = firstNonEmptyRouteValue(
		firstRouteString(raw, routeScopeKeys),
		firstRouteString(messageInfo, routeScopeKeys),
		firstRouteString(additionalConfig, routeScopeKeys),
	)
	zap.L().Warn("消息使用旧版路由归属格式，请迁移到顶层 account_id/scope",
		zap.String("platform", platform),
		zap.Any("message_id", raw["message_id"]))
	return info, nil
}

func firstNonEmptyRouteValue(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

func firstRouteString(mapping map[string]any, keys []string) string {
	for _, key := range keys {
		if value, ok := mapping[key]; ok && value != nil {
			switch typed := value.(type) {
			case string:
				if text := strings.TrimSpace(typed); text != "" {
					return text
				}
			default:
				if text := strings.TrimSpace(fmt.Sprint(typed)); text != "" {
					return text
				}
			}
		}
	}
	return ""
}
