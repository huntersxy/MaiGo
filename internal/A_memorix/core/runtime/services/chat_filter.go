package services

// A_memorix 聊天流记忆开关过滤，对齐 Python core/runtime/services/chat_filter_service.py

import "strings"

// ChatFilterService 对齐 MemoryChatFilterService
type ChatFilterService struct {
	*Base
}

// NewChatFilterService 构造聊天流过滤器
func NewChatFilterService(base *Base) *ChatFilterService {
	return &ChatFilterService{Base: base}
}

// IsChatEnabled 判断聊天流是否启用记忆，对齐 is_chat_enabled
func (s *ChatFilterService) IsChatEnabled(streamID string, groupID string, userID string) bool {
	filterConfig := s.CfgMap("filter")
	if len(filterConfig) == 0 {
		return true
	}
	return chatFilterConfigAllows(filterConfig, streamID, groupID, userID, true)
}

// IsChatFiltered 判断是否应当跳过该聊天流，对齐 _is_chat_filtered
func (s *ChatFilterService) IsChatFiltered(respectFilter bool, streamID string, groupID string, userID string) bool {
	if !respectFilter {
		return false
	}
	if strings.TrimSpace(streamID) == "" && strings.TrimSpace(groupID) == "" && strings.TrimSpace(userID) == "" {
		return false
	}
	return !s.IsChatEnabled(streamID, groupID, userID)
}

// chatFilterConfigAllows 对齐 _chat_filter_config_allows
func chatFilterConfigAllows(filterConfig map[string]any, streamID string, groupID string, userID string, defaultWhenEmpty bool) bool {
	if !pyTruthy(filterConfig["enabled"]) && filterConfig["enabled"] != nil {
		return true
	}
	mode := "blacklist"
	if rawMode, ok := filterConfig["mode"].(string); ok && strings.TrimSpace(rawMode) != "" {
		mode = strings.ToLower(strings.TrimSpace(rawMode))
	}

	patterns := []string{}
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
