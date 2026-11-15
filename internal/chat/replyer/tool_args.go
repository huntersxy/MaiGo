package replyer

import (
	"fmt"
	"strings"

	"MaiGo/internal/maisaka"
)

// ToolArgString 读取 reply 工具参数中的字符串值
func ToolArgString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

// SessionIDFromContext 从目标消息或历史中解析真实会话标识
func SessionIDFromContext(target *maisaka.ContextMessage, history []maisaka.ContextMessage) string {
	if target != nil {
		if sessionID := strings.TrimSpace(target.SessionID); sessionID != "" {
			return sessionID
		}
	}
	for i := len(history) - 1; i >= 0; i-- {
		if sessionID := strings.TrimSpace(history[i].SessionID); sessionID != "" {
			return sessionID
		}
	}
	return ""
}
