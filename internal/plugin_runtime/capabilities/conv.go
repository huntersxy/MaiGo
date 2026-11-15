package capabilities

// 会话标识与 map 拷贝工具。

import (
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/common/database"
)

func parseConversationKey(raw string) (targetID int64, kind string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, "", false
	}
	if strings.HasPrefix(raw, "group:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(raw, "group:"), 10, 64)
		return id, memory.ConversationKindGroup, err == nil && id > 0
	}
	if strings.HasPrefix(raw, "private:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(raw, "private:"), 10, 64)
		return id, memory.ConversationKindPrivate, err == nil && id > 0
	}
	return 0, "", false
}

func cloneAnyMap(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		if nested, ok := value.(map[string]any); ok {
			out[key] = cloneAnyMap(nested)
			continue
		}
		out[key] = value
	}
	return out
}

func uintFromAny(v any) (uint, error) {
	switch n := v.(type) {
	case uint:
		return n, nil
	case int:
		if n < 0 {
			return 0, fmt.Errorf("negative")
		}
		return uint(n), nil
	case int64:
		if n < 0 {
			return 0, fmt.Errorf("negative")
		}
		return uint(n), nil
	case float64:
		if n < 0 {
			return 0, fmt.Errorf("negative")
		}
		return uint(n), nil
	case string:
		u, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
		return uint(u), err
	default:
		return 0, fmt.Errorf("unsupported")
	}
}
