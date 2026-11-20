package runtime

import (
	"context"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/plugin_runtime"
	"MaiGo/internal/plugin_runtime/hooks"
	"MaiGo/internal/plugin_runtime/hostports"

	"go.uber.org/zap"
)

// SchedulePluginReload 在插件组件变化后合并刷新 generation，避免每个插件各重建一次
func (a *Agent) SchedulePluginReload() {
	if a == nil || !a.pluginReloadPending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.pluginReloadPending.Store(false)
		time.Sleep(500 * time.Millisecond)
		if a.ctx == nil || a.ctx.Err() != nil || a.currentGeneration() == nil {
			return
		}
		if err := a.Reload(); err != nil {
			zap.L().Warn("插件组件变化后刷新 Agent 工具失败", zap.Error(err))
		}
	}()
}

func (a *Agent) SetPluginHost(host *pluginruntime.Host) {
	if a == nil {
		return
	}
	a.pluginHost = host
	if host != nil {
		hooks.SetGlobalEmitter(host)
		maisakaPort := hostports.NewMaisaka(func() hostports.DocumentReplyGenerator { return a })
		maisakaPort.SetContextRuntime(func() hostports.MaisakaContextRuntime { return a })
		host.SetMaisakaPort(maisakaPort)
	}
}

func (a *Agent) emitReceiveHook(ctx context.Context, hookName string, msg *onebot.ConversationMessage) (*onebot.ConversationMessage, bool) {
	if a == nil || a.pluginHost == nil || msg == nil {
		return msg, false
	}
	receivedAt := msg.ReceivedAt
	hookMessage := serializeInboundHookMessage(msg)
	// 插件按 message.session_id 构造发送路由，入站阶段会话可能尚未解析，这里补上
	if sessionID := a.resolveMaiBotSessionID(ctx, msg.ConversationKind, msg.TargetID); sessionID != "" {
		hookMessage["session_id"] = sessionID
	}
	kwargs := map[string]any{
		"message": hookMessage,
	}
	result := a.pluginHost.EmitHook(ctx, hookName, kwargs)
	msg = applyHookMessage(msg, result.Kwargs, receivedAt)
	if result.Aborted {
		zap.L().Info("入站消息被 Hook 中止",
			zap.String("hook", hookName),
			zap.String("stopped_by", result.StoppedBy),
			zap.Int64("message_id", msg.MessageID))
		return msg, true
	}
	for _, errMsg := range result.Errors {
		zap.L().Warn("入站 Hook 错误", zap.String("hook", hookName), zap.String("error", errMsg))
	}
	return msg, false
}

func serializeInboundHookMessage(msg *onebot.ConversationMessage) map[string]any {
	if msg == nil {
		return map[string]any{}
	}
	platform := strings.TrimSpace(msg.Platform)
	if platform == "" {
		platform = platformio.PlatformQQ
	}
	// 与 Python hook_payloads.serialize_session_message 对齐：插件按 raw_message 的 text 段
	// 解析正文，缺这段会当成「没有正文」直接放行
	out := map[string]any{
		"message_id":        msg.MessageID,
		"conversation_kind": msg.ConversationKind,
		"platform":          platform,
		"account_id":        msg.AccountID,
		"scope":             msg.Scope,
		"target_id":         msg.TargetID,
		"user_id":           msg.UserID,
		"nickname":          msg.Nickname,
		"group_card":        msg.GroupCard,
		"content":           msg.Content,
		"final_content":     msg.FinalContent,
		"processed_plain_text": inlineHookFirstText(msg.FinalContent, msg.Content),
		"raw_message":       serializeInboundHookSegments(msg),
		"message_info":      serializeInboundHookMessageInfo(msg),
		"is_mentioned":      msg.IsMentioned,
		"is_at":             msg.IsMentioned || len(msg.AtList) > 0,
		"is_emoji":          len(msg.Faces) > 0,
		"is_picture":        len(msg.Images) > 0,
		"is_notify":         msg.IsNotify,
	}
	if !msg.Time.IsZero() {
		out["time"] = msg.Time.UTC().Format(time.RFC3339Nano)
	}
	if !msg.ReceivedAt.IsZero() {
		out["received_at"] = msg.ReceivedAt.UTC().Format(time.RFC3339Nano)
	}
	if msg.Reply != nil {
		out["reply"] = map[string]any{
			"message_id": msg.Reply.MessageID,
			"content":    msg.Reply.Content,
			"sender_id":  msg.Reply.SenderID,
			"nickname":   msg.Reply.Nickname,
			"group_card": msg.Reply.GroupCard,
		}
	}
	return out
}

func applyHookMessage(msg *onebot.ConversationMessage, kwargs map[string]any, receivedAt time.Time) *onebot.ConversationMessage {
	if msg == nil || kwargs == nil {
		return msg
	}
	raw, ok := kwargs["message"]
	if !ok || raw == nil {
		return msg
	}
	patch, ok := raw.(map[string]any)
	if !ok {
		return msg
	}
	if v, ok := patch["content"].(string); ok {
		msg.Content = v
	}
	if v, ok := patch["final_content"].(string); ok {
		msg.FinalContent = v
	}
	if v, ok := patch["is_mentioned"].(bool); ok {
		msg.IsMentioned = v
	}
	if v, ok := patch["nickname"].(string); ok {
		msg.Nickname = v
	}
	if v, ok := patch["group_card"].(string); ok {
		msg.GroupCard = v
	}
	msg.ReceivedAt = receivedAt
	return msg
}

// serializeInboundHookSegments 把消息段序列化为 OneBot 风格数组，对齐 Python raw_message 结构
func serializeInboundHookSegments(msg *onebot.ConversationMessage) []map[string]any {
	if msg == nil {
		return []map[string]any{}
	}
	segments := make([]map[string]any, 0, len(msg.MessageParts))
	for _, part := range msg.MessageParts {
		switch part.Kind {
		case "text":
			segments = append(segments, map[string]any{"type": "text", "data": part.Text})
		case "at":
			segments = append(segments, map[string]any{
				"type": "at",
				"data": map[string]any{"qq": strconv.FormatInt(part.AtUserID, 10)},
			})
		case "image":
			segments = append(segments, map[string]any{"type": "image", "data": map[string]any{}})
		default:
			if strings.TrimSpace(part.Text) != "" {
				segments = append(segments, map[string]any{"type": "text", "data": part.Text})
			}
		}
	}
	return segments
}

// serializeInboundHookMessageInfo 组装 Python 侧 message_info 结构
func serializeInboundHookMessageInfo(msg *onebot.ConversationMessage) map[string]any {
	if msg == nil {
		return map[string]any{}
	}
	userInfo := map[string]any{
		"user_id":       strconv.FormatInt(msg.UserID, 10),
		"user_nickname": msg.Nickname,
		"user_cardname": msg.GroupCard,
	}
	info := map[string]any{"user_info": userInfo}
	if msg.ConversationKind == memory.ConversationKindGroup {
		groupID := msg.GroupKey
		if groupID == "" {
			groupID = strconv.FormatInt(msg.TargetID, 10)
		}
		info["group_info"] = map[string]any{
			"group_id":   groupID,
			"group_name": msg.GroupName,
		}
	}
	return info
}

// inlineHookFirstText 返回第一个非空字符串
func inlineHookFirstText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
