package native

// 聊天流过滤适配器：services.ChatFilterService 不带 context，补内核接口的 ctx 形参
// 配置裁决逻辑与 services.chatFilterConfigAllows 保持一致

import (
	"context"
	"strings"

	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/runtime/services"
)

type chatFilterAdapter struct {
	inner *services.ChatFilterService
}

var _ memruntime.ChatFilterService = (*chatFilterAdapter)(nil)

func (a *chatFilterAdapter) IsChatEnabled(ctx context.Context, streamID string, groupID string, userID string) bool {
	return a.inner.IsChatEnabled(streamID, groupID, userID)
}

func (a *chatFilterAdapter) ChatFilterConfigAllows(ctx context.Context, filterConfig map[string]any, streamID string, groupID string, userID string, defaultWhenEmpty bool) bool {
	return nativeChatFilterConfigAllows(filterConfig, streamID, groupID, userID, defaultWhenEmpty)
}

func (a *chatFilterAdapter) IsChatFiltered(ctx context.Context, respectFilter bool, streamID string, groupID string, userID string) bool {
	return a.inner.IsChatFiltered(respectFilter, streamID, groupID, userID)
}

func nativeChatFilterConfigAllows(filterConfig map[string]any, streamID string, groupID string, userID string, defaultWhenEmpty bool) bool {
	if enabled, exists := filterConfig["enabled"]; exists && enabled != nil && !memruntime.PyTruthy(enabled) {
		return true
	}
	mode := "blacklist"
	if rawMode, ok := filterConfig["mode"].(string); ok && strings.TrimSpace(rawMode) != "" {
		mode = strings.ToLower(strings.TrimSpace(rawMode))
	}
	patterns := make([]string, 0)
	if rawPatterns, ok := filterConfig["chats"].([]any); ok {
		for _, item := range rawPatterns {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				patterns = append(patterns, strings.TrimSpace(text))
			}
		}
	}
	if len(patterns) == 0 {
		if mode == "blacklist" {
			return defaultWhenEmpty
		}
		return false
	}
	streamToken := strings.TrimSpace(streamID)
	groupToken := strings.TrimSpace(groupID)
	userToken := strings.TrimSpace(userID)
	candidates := map[string]struct{}{}
	for _, token := range []string{streamToken, groupToken, userToken} {
		if token != "" {
			candidates[token] = struct{}{}
		}
	}
	matched := false
	for _, pattern := range patterns {
		if strings.Contains(pattern, ":") {
			parts := strings.SplitN(pattern, ":", 2)
			prefix := strings.ToLower(strings.TrimSpace(parts[0]))
			value := strings.TrimSpace(parts[1])
			switch prefix {
			case "group":
				if groupToken != "" && value == groupToken {
					matched = true
				}
			case "user", "private":
				if userToken != "" && value == userToken {
					matched = true
				}
			case "stream":
				if streamToken != "" && value == streamToken {
					matched = true
				}
			}
		} else if _, ok := candidates[pattern]; ok {
			matched = true
		}
		if matched {
			break
		}
	}
	if mode == "blacklist" {
		return !matched
	}
	return matched
}
