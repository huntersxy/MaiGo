package capabilities

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

// _build_send_result 对齐 core.py _build_send_result
func buildSendResult(args map[string]any, sent bool, messageID, errorText string) map[string]any {
	result := map[string]any{"success": sent}
	if truthyPayloadValue(args["return_details"]) {
		trimmed := strings.TrimSpace(messageID)
		var id any
		if sent && trimmed != "" {
			id = trimmed
		}
		result["sent"] = sent
		result["message_id"] = id
	}
	if errorText != "" {
		result["error"] = errorText
	}
	return result
}

func sendStringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := args[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func validBase64Text(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if _, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return true
	}
	_, err := base64.RawStdEncoding.DecodeString(raw)
	return err == nil
}

func buildSendRequest(kind, pluginID string, args map[string]any) PluginSendRequest {
	req := PluginSendRequest{
		Kind:               kind,
		PluginID:           pluginID,
		StreamID:           sendStringArg(args, "stream_id"),
		Text:               sendStringArg(args, "text"),
		ProcessedPlainText: sendStringArg(args, "processed_plain_text"),
		SetReply:           truthyPayloadValue(args["set_reply"]),
		ReplyMessageID:     firstNonEmptyText(sendStringArg(args, "reply_message_id"), sendStringArg(args, "reply_to")),
		SourceKind:         firstNonEmptyText(sendStringArg(args, "maisaka_source_kind"), "plugin_send"),
	}
	req.Typing = boolPtr(args["typing"])
	// 缺省布尔值保持 nil，由发送端口按 Python 默认值（storage_message=True、sync=False）处理
	req.StorageMessage = boolPtr(args["storage_message"])
	req.SyncToMaisakaHistory = boolPtr(args["sync_to_maisaka_history"])
	return req
}

func boolPtr(value any) *bool {
	if value == nil {
		return nil
	}
	b := truthyPayloadValue(value)
	return &b
}

// normalizePluginSegment 对齐 _normalize_plugin_segment
func normalizePluginSegment(raw map[string]any) map[string]any {
	segmentType := strings.ToLower(strings.TrimSpace(fmt.Sprint(raw["type"])))
	switch segmentType {
	case "text":
		content := raw["data"]
		if content == nil || content == "" {
			content = raw["content"]
		}
		return map[string]any{"type": "text", "data": stringValue(content)}
	case "image", "emoji", "voice":
		out := cloneAnyMap(raw)
		out["type"] = segmentType
		content := strings.TrimSpace(stringValue(raw["content"]))
		if content != "" && stringValue(out["binary_data_base64"]) == "" && stringValue(out["hash"]) == "" {
			out["binary_data_base64"] = content
		}
		if _, exists := out["data"]; !exists {
			out["data"] = stringValue(raw["data"])
		}
		return out
	default:
		return cloneAnyMap(raw)
	}
}

func normalizePluginSegments(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		if maps, ok := raw.([]map[string]any); ok {
			out := make([]map[string]any, 0, len(maps))
			for _, item := range maps {
				out = append(out, normalizePluginSegment(item))
			}
			return out
		}
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if segment, ok := item.(map[string]any); ok {
			out = append(out, normalizePluginSegment(segment))
		}
	}
	return out
}

// normalizeForwardNodes 归一化转发节点，对齐 _cap_send_forward 的节点构造
func normalizeForwardNodes(messages any) ([]map[string]any, string) {
	items, ok := messages.([]any)
	if !ok || len(items) == 0 {
		return nil, "缺少必要参数 messages 或 stream_id"
	}
	nodes := make([]map[string]any, 0, len(items))
	for index, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rawSegments := message["segments"]
		if rawSegments == nil {
			rawSegments = message["content"]
		}
		segments := normalizePluginSegments(rawSegments)
		if len(segments) == 0 {
			continue
		}
		nickname := firstNonEmptyText(stringValue(message["nickname"]), stringValue(message["user_nickname"]), "插件消息")
		messageID := stringValue(message["message_id"])
		if messageID == "" {
			messageID = fmt.Sprintf("plugin_forward_%d", index)
		}
		nodes = append(nodes, map[string]any{
			"user_id":       stringValue(message["user_id"]),
			"user_nickname": nickname,
			"user_cardname": stringValue(message["user_cardname"]),
			"message_id":    messageID,
			"content":       segments,
		})
	}
	if len(nodes) == 0 {
		return nil, "messages 中缺少有效的转发节点"
	}
	return nodes, ""
}

// makeSendCapabilityHandler 构造 send.* 能力；kind 允许传入完整能力名 send.text
func makeSendCapabilityHandler(sender PluginSendPort, capability string) CapabilityHandler {
	kind := strings.TrimPrefix(strings.TrimSpace(capability), "send.")
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		req := buildSendRequest(kind, pluginID, args)
		if req.StreamID == "" {
			return buildSendResult(args, false, "", "缺少必要参数 stream_id"), nil
		}
		switch kind {
		case "text":
			if req.Text == "" {
				return buildSendResult(args, false, "", "缺少必要参数 text 或 stream_id"), nil
			}
		case "emoji":
			req.Base64 = sendStringArg(args, "emoji_base64")
			if !validBase64Text(req.Base64) {
				return buildSendResult(args, false, "", "缺少必要参数 emoji_base64 或 stream_id"), nil
			}
		case "image":
			req.Base64 = sendStringArg(args, "image_base64")
			if !validBase64Text(req.Base64) {
				return buildSendResult(args, false, "", "缺少必要参数 image_base64 或 stream_id"), nil
			}
		case "command":
			req.Command = sendStringArg(args, "command")
			if req.Command == "" {
				return buildSendResult(args, false, "", "缺少必要参数 command 或 stream_id"), nil
			}
		case "custom":
			req.MessageType = firstNonEmptyText(sendStringArg(args, "message_type"), sendStringArg(args, "custom_type"))
			if req.MessageType == "" {
				return buildSendResult(args, false, "", "缺少必要参数 message_type 或 stream_id"), nil
			}
			req.Content = args["content"]
			if req.Content == nil {
				req.Content = args["data"]
			}
		case "hybrid":
			raw := args["segments"]
			if raw == nil {
				raw = args["parts"]
			}
			req.HybridNodes = normalizePluginSegments(raw)
			if len(req.HybridNodes) == 0 {
				return buildSendResult(args, false, "", "缺少必要参数 segments 或 stream_id"), nil
			}
		case "forward":
			nodes, message := normalizeForwardNodes(args["messages"])
			if message != "" {
				return buildSendResult(args, false, "", message), nil
			}
			req.ForwardNodes = nodes
			if req.ProcessedPlainText == "" {
				req.ProcessedPlainText = "[转发消息]"
			}
		}
		if sender == nil {
			return buildSendResult(args, false, "", "send."+kind+" 端口未接线：主程序尚未注入发送服务"), nil
		}
		result, err := sender.SendPluginMessage(ctx, req)
		if err != nil {
			return buildSendResult(args, false, "", err.Error()), nil
		}
		return buildSendResult(args, result.Sent, result.MessageID, ""), nil
	}
}
