package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/maisaka"
	maisakactx "maibot-go/internal/maisaka/context"

	"go.uber.org/zap"
)

// AppendPluginContext 把插件提供的图文上下文追加到 Maisaka 历史，返回写入位置
// 对齐 src/plugin_runtime/capabilities/core.py _cap_maisaka_context_append
func (a *Agent) AppendPluginContext(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error) {
	streamID := firstNonEmptyMapString(args, "stream_id", "chat_id")
	if streamID == "" {
		return map[string]any{"success": false, "error": "缺少必要参数 stream_id 或 chat_id"}, nil
	}
	segments := normalizePluginContextSegments(args["segments"])
	if len(segments) == 0 {
		return map[string]any{"success": false, "error": "缺少有效的 segments"}, nil
	}
	replyable, err := pluginContextReplyable(args["replyable"])
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	visibleText := strings.TrimSpace(firstNonEmptyMapString(args, "visible_text"))
	images := make([]maisaka.ImageComponent, 0)
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		text, image := pluginContextSegmentContent(segment)
		if text != "" {
			parts = append(parts, text)
		}
		if image != nil {
			images = append(images, *image)
		}
	}
	if visibleText == "" {
		visibleText = strings.TrimSpace(strings.Join(parts, " "))
	}
	if visibleText == "" {
		visibleText = "[插件上下文消息]"
	}
	sourceKind := strings.TrimSpace(firstNonEmptyMapString(args, "source_kind"))
	if sourceKind == "" {
		sourceKind = "plugin:" + pluginID
	}
	session, runtime, err := a.pluginContextRuntime(ctx, streamID)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	_ = session
	now := time.Now()
	messageID := strings.TrimSpace(firstNonEmptyMapString(args, "message_id"))
	metadata := map[string]any{
		"plugin_id":   pluginID,
		"replyable":   replyable,
		"source_kind": sourceKind,
	}
	content := visibleText
	if replyable {
		// 可回复材料按 Python SessionBackedMessage：正文带 Planner 前缀，并保留真实作者
		userInfo, _ := args["user_info"].(map[string]any)
		userName := strings.TrimSpace(firstNonEmptyMapString(userInfo, "user_nickname", "user_cardname", "user_id"))
		if userName == "" {
			userName = "unknown"
		}
		content = maisakactx.BuildPlannerPrefix(maisakactx.PlannerPrefixInput{
			Timestamp:        now,
			UserName:         userName,
			MessageID:        messageID,
			IncludeMessageID: messageID != "",
		}) + visibleText
		metadata["sender_name"] = userName
		if userID := strings.TrimSpace(firstNonEmptyMapString(userInfo, "user_id")); userID != "" {
			metadata["sender_id"] = userID
		}
	}
	message := maisaka.ContextMessage{
		Role:       maisaka.RoleUser,
		Content:    content,
		Source:     sourceKind,
		Timestamp:  now,
		ReceivedAt: &now,
		MessageID:  messageID,
		SessionID:  streamID,
		Images:     images,
		// 可回复的插件材料等价 Python SessionBackedMessage，计入上下文；内部材料不计
		CountInContext: replyable,
		Metadata:       metadata,
	}
	index := runtime.AppendHistory(message)
	return map[string]any{
		"success":      true,
		"index":        index,
		"stream_id":    streamID,
		"visible_text": visibleText,
		"source_kind":  sourceKind,
		"replyable":    replyable,
	}, nil
}

// ResolvePluginContextImage 读取现有运行上下文中的图片，返回 base64 与哈希
// 对齐 _cap_maisaka_context_resolve_image，不创建会话或查询其他聊天流
func (a *Agent) ResolvePluginContextImage(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error) {
	_ = pluginID
	streamID := strings.TrimSpace(firstNonEmptyMapString(args, "stream_id"))
	if streamID == "" {
		return map[string]any{"success": false, "error": "缺少必要参数 stream_id"}, nil
	}
	sourceID := strings.TrimSpace(firstNonEmptyMapString(args, "source_id"))
	if sourceID == "" {
		return map[string]any{"success": false, "error": "缺少必要参数 source_id"}, nil
	}
	index, err := pluginContextIndex(args["index"])
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	_, runtime, err := a.pluginContextRuntime(ctx, streamID)
	if err != nil {
		return map[string]any{"success": false, "error": "该聊天流没有现存的 Maisaka 上下文"}, nil
	}
	message, ok := runtime.FindSourceMessageByID(sourceID)
	if !ok || index < 0 || index >= len(message.Images) {
		return map[string]any{"success": false, "error": "未找到指定的上下文图片"}, nil
	}
	image := message.Images[index]
	binary, hash, err := readPluginContextImage(image)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	return map[string]any{"success": true, "image": map[string]any{
		"binary_data_base64": base64.StdEncoding.EncodeToString(binary),
		"binary_hash":        hash,
	}}, nil
}

// TriggerPluginProactive 请求 Maisaka 基于指定聊天流主动处理一轮对话
// 对齐 _cap_maisaka_proactive_trigger：先定位已有聊天流，再把意图作为主动触发投递到内部队列
var pluginProactiveMu sync.Mutex

func (a *Agent) TriggerPluginProactive(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error) {
	streamID := strings.TrimSpace(firstNonEmptyMapString(args, "stream_id", "chat_id", "session_id"))
	if streamID == "" {
		return map[string]any{"success": false, "error": "缺少必要参数 stream_id"}, nil
	}
	intent := strings.TrimSpace(firstNonEmptyMapString(args, "intent", "prompt", "text"))
	if intent == "" {
		return map[string]any{"success": false, "error": "缺少必要参数 intent"}, nil
	}
	_, runtime, err := a.pluginContextRuntime(ctx, streamID)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	// 只透传 dict 形式的 metadata，reason/priority 单独成字段，对齐 Python src/plugin_runtime/capabilities/core.py:356-363
	metadata, _ := args["metadata"].(map[string]any)
	taskID, err := runtime.EnqueueProactiveTask(
		pluginID,
		intent,
		strings.TrimSpace(firstNonEmptyMapString(args, "reason")),
		strings.TrimSpace(firstNonEmptyMapString(args, "priority")),
		metadata,
	)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	go func() {
		pluginProactiveMu.Lock()
		defer pluginProactiveMu.Unlock()
		for runtime.IsRunning() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		if _, runErr := runtime.Engine().RunQueuedTurn(context.WithoutCancel(ctx)); runErr != nil {
			zap.L().Warn("插件主动触发执行失败", zap.String("plugin_id", pluginID), zap.String("stream_id", streamID), zap.Error(runErr))
		}
	}()
	return map[string]any{
		"success":   true,
		"stream_id": streamID,
		"task_id":   taskID,
		"queued":    true,
	}, nil
}

func (a *Agent) pluginContextRuntime(ctx context.Context, streamID string) (*Session, *maisaka.Runtime, error) {
	if a == nil || a.chatSessions == nil {
		return nil, nil, fmt.Errorf("聊天流服务未接线")
	}
	record, err := a.chatSessions.GetExistingBySessionID(ctx, streamID)
	if err != nil || record == nil {
		return nil, nil, fmt.Errorf("未找到已存在的聊天流: %s", streamID)
	}
	kind := "private"
	target := ""
	if record.IsGroup() {
		if record.GroupID == nil {
			return nil, nil, fmt.Errorf("聊天流 %s 缺少群号", streamID)
		}
		kind = "group"
		target = strings.TrimSpace(*record.GroupID)
	} else {
		if record.UserID == nil {
			return nil, nil, fmt.Errorf("聊天流 %s 缺少用户编号", streamID)
		}
		target = strings.TrimSpace(*record.UserID)
	}
	targetID, err := strconv.ParseInt(target, 10, 64)
	if err != nil || targetID <= 0 {
		return nil, nil, fmt.Errorf("聊天流 %s 的目标 ID 非法", streamID)
	}
	session := a.sessions.get(kind, targetID)
	if session == nil {
		return nil, nil, fmt.Errorf("聊天流 %s 的会话尚未加载", streamID)
	}
	runtime := session.MaisakaRuntime()
	if runtime == nil {
		runtime = a.attachMaisakaRuntime(kind, targetID, session)
	}
	if runtime == nil {
		return nil, nil, fmt.Errorf("该聊天流没有现存的 Maisaka 上下文")
	}
	return session, runtime, nil
}

func normalizePluginContextSegments(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		if maps, mapOK := raw.([]map[string]any); mapOK {
			items = make([]any, 0, len(maps))
			for _, item := range maps {
				items = append(items, item)
			}
		} else {
			return nil
		}
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		segment, ok := item.(map[string]any)
		if !ok {
			continue
		}
		normalized := make(map[string]any, len(segment))
		for key, value := range segment {
			normalized[key] = value
		}
		kind := strings.ToLower(strings.TrimSpace(stringValueOf(normalized["type"])))
		if kind == "" {
			continue
		}
		normalized["type"] = kind
		if _, exists := normalized["data"]; !exists {
			if content, has := normalized["content"]; has {
				normalized["data"] = content
			}
		}
		switch kind {
		case "image", "emoji", "voice":
			encoded := firstNonEmptyMapString(normalized, "binary_data_base64", "base64", "image_base64", "emoji_base64")
			if encoded != "" {
				normalized["binary_data_base64"] = encoded
				dataText := strings.TrimSpace(stringValueOf(normalized["data"]))
				if _, hasData := normalized["data"]; !hasData || dataText == encoded {
					normalized["data"] = strings.TrimSpace(stringValueOf(normalized["description"]))
				}
			}
		}
		out = append(out, normalized)
	}
	return out
}

func pluginContextSegmentContent(segment map[string]any) (string, *maisaka.ImageComponent) {
	kind := strings.ToLower(strings.TrimSpace(stringValueOf(segment["type"])))
	switch kind {
	case "text":
		return strings.TrimSpace(firstNonEmptyMapString(segment, "data", "content")), nil
	case "image", "emoji":
		encoded := strings.TrimSpace(firstNonEmptyMapString(segment, "binary_data_base64"))
		description := strings.TrimSpace(stringValueOf(segment["data"]))
		if encoded != "" {
			ref := "data:image/png;base64," + encoded
			return "[图片]", &maisaka.ImageComponent{MediaRef: ref, Format: "png", Description: description, Placeholder: "[图片]"}
		}
		if url := strings.TrimSpace(stringValueOf(segment["data"])); strings.HasPrefix(url, "http") {
			return "[图片]", &maisaka.ImageComponent{MediaRef: url, Format: "url", Description: description, Placeholder: "[图片]"}
		}
		return "[图片]", nil
	case "voice":
		return "[语音]", nil
	case "at":
		name := strings.TrimSpace(firstNonEmptyMapString(segment, "data", "name", "user_id"))
		if name == "" {
			return "@某人", nil
		}
		return "@" + name, nil
	case "face":
		return "[表情]", nil
	default:
		return strings.TrimSpace(firstNonEmptyMapString(segment, "data", "content")), nil
	}
}

func pluginContextReplyable(raw any) (bool, error) {
	if raw == nil {
		return false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("replyable 必须为布尔值")
	}
	return value, nil
}

func pluginContextIndex(raw any) (int, error) {
	if raw == nil {
		return 0, nil
	}
	switch value := raw.(type) {
	case int:
		if value >= 0 {
			return value, nil
		}
	case int64:
		if value >= 0 {
			return int(value), nil
		}
	case float64:
		if value >= 0 && value == float64(int(value)) {
			return int(value), nil
		}
	}
	return 0, fmt.Errorf("index 必须是非负整数")
}

func readPluginContextImage(image maisaka.ImageComponent) ([]byte, string, error) {
	ref := strings.TrimSpace(image.MediaRef)
	if ref == "" {
		return nil, "", fmt.Errorf("上下文图片缺少数据")
	}
	if strings.HasPrefix(ref, "data:") {
		if idx := strings.Index(ref, ";base64,"); idx >= 0 {
			raw, err := base64.StdEncoding.DecodeString(ref[idx+len(";base64,"):])
			if err != nil {
				return nil, "", fmt.Errorf("上下文图片 Base64 解码失败: %w", err)
			}
			sum := sha256.Sum256(raw)
			return raw, hex.EncodeToString(sum[:]), nil
		}
	}
	if strings.HasPrefix(ref, "base64://") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ref, "base64://"))
		if err != nil {
			return nil, "", fmt.Errorf("上下文图片 Base64 解码失败: %w", err)
		}
		sum := sha256.Sum256(raw)
		return raw, hex.EncodeToString(sum[:]), nil
	}
	raw, err := os.ReadFile(ref)
	if err != nil {
		return nil, "", fmt.Errorf("读取上下文图片失败: %w", err)
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

func firstNonEmptyMapString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringValueOf(values[key])); value != "" {
			return value
		}
	}
	return ""
}

func stringValueOf(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
