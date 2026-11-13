package platformio

import (
	"fmt"
	"strings"
)

// RouteKeyFactory 统一构造 RouteKey，对齐 MaiBot route_key_factory.RouteKeyFactory
type RouteKeyFactory struct{}

// FromPlatform 根据平台名与可选 metadata 构造 RouteKey
func (RouteKeyFactory) FromPlatform(platform string, accountID, scope string, metadata map[string]any) (RouteKey, error) {
	extractedAccount, extractedScope := extractRouteComponents(metadata)
	key := RouteKey{
		Platform:  platform,
		AccountID: firstNonEmpty(accountID, extractedAccount),
		Scope:     firstNonEmpty(scope, extractedScope),
	}
	return key.Normalize()
}

// FromChatMessage 从规范化 ChatMessage 提取 RouteKey
func (RouteKeyFactory) FromChatMessage(msg ChatMessage) (RouteKey, error) {
	if msg.AccountID != "" || msg.Scope != "" {
		key := RouteKey{
			Platform:  msg.Platform,
			AccountID: msg.AccountID,
			Scope:     msg.Scope,
		}
		return key.Normalize()
	}
	meta := map[string]any{}
	if msg.Metadata != nil {
		meta = msg.Metadata
	}
	return RouteKeyFactory{}.FromPlatform(msg.Platform, "", "", meta)
}

var accountIDKeys = []string{
	"platform_io_account_id",
	"account_id",
	"self_id",
	"bot_account",
}

var scopeKeys = []string{
	"platform_io_scope",
	"route_scope",
	"adapter_scope",
	"connection_id",
}

func extractRouteComponents(mapping map[string]any) (accountID, scope string) {
	if len(mapping) == 0 {
		return "", ""
	}
	return pickString(mapping, accountIDKeys), pickString(mapping, scopeKeys)
}

func pickString(mapping map[string]any, keys []string) string {
	for _, key := range keys {
		value, ok := mapping[key]
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case string:
			if s := trimNonEmpty(v); s != "" {
				return s
			}
		case fmt.Stringer:
			if s := trimNonEmpty(v.String()); s != "" {
				return s
			}
		default:
			if s := trimNonEmpty(fmt.Sprint(v)); s != "" {
				return s
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := trimNonEmpty(v); s != "" {
			return s
		}
	}
	return ""
}

func trimNonEmpty(s string) string {
	return strings.TrimSpace(s)
}
