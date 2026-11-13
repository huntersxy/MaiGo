package platformio

import (
	"fmt"
	"strconv"
	"strings"
)

// ForwardNodesFromPluginMessages 把插件能力的合并转发节点转换为平台段节点
// messages 对齐 pluginruntime normalizeForwardNodes 的结构：
// user_id/nickname/user_nickname/user_cardname/time/content（或 segments）
// convert 负责把节点内单个消息段转为 platformio.Segment
func ForwardNodesFromPluginMessages(messages []map[string]any, convert func(map[string]any) (Segment, error)) ([]ForwardNode, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("合并转发缺少消息节点")
	}
	if convert == nil {
		return nil, fmt.Errorf("合并转发缺少消息段转换器")
	}
	nodes := make([]ForwardNode, 0, len(messages))
	for index, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("合并转发第 %d 个节点为空", index+1)
		}
		rawSegments, ok := forwardContentNodes(message)
		if !ok || len(rawSegments) == 0 {
			return nil, fmt.Errorf("合并转发第 %d 个节点缺少消息段", index+1)
		}
		segments := make([]Segment, 0, len(rawSegments))
		for _, raw := range rawSegments {
			seg, err := convert(raw)
			if err != nil {
				return nil, fmt.Errorf("合并转发第 %d 个节点消息段无效: %w", index+1, err)
			}
			segments = append(segments, seg)
		}
		nodes = append(nodes, ForwardNode{
			UserID:    forwardString(message, "user_id", "userid", "qq"),
			Nickname:  forwardString(message, "nickname", "user_nickname", "name"),
			Cardname:  forwardString(message, "user_cardname", "cardname", "card"),
			MessageID: forwardString(message, "message_id", "id"),
			Time:      forwardInt64(message["time"]),
			Segments:  segments,
		})
	}
	return nodes, nil
}

func forwardContentNodes(message map[string]any) ([]map[string]any, bool) {
	raw, ok := message["content"]
	if !ok || raw == nil {
		raw, ok = message["segments"]
	}
	if !ok || raw == nil {
		return nil, false
	}
	switch typed := raw.(type) {
	case []map[string]any:
		return typed, true
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			node, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, node)
		}
		return out, true
	default:
		return nil, false
	}
}

func forwardString(message map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := message[key]
		if !ok || value == nil {
			continue
		}
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			return text
		}
	}
	return ""
}

func forwardInt64(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}
