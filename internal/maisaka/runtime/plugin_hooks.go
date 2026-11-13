package runtime

import (
	"context"
	"time"

	"maibot-go/internal/platform_io/drivers/onebot"
	"maibot-go/internal/plugin_runtime"
	"maibot-go/internal/plugin_runtime/hooks"
	"maibot-go/internal/plugin_runtime/hostports"

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
		if gateway, ok := host.MessageGatewayPort().(interface {
			SetInboundHandler(func(context.Context, map[string]any) error)
		}); ok {
			gateway.SetInboundHandler(a.ReceiveExternalPluginMessage)
		}
	}
}

func (a *Agent) emitReceiveHook(ctx context.Context, hookName string, msg *onebot.ConversationMessage) (*onebot.ConversationMessage, bool) {
	if a == nil || a.pluginHost == nil || msg == nil {
		return msg, false
	}
	receivedAt := msg.ReceivedAt
	kwargs := map[string]any{
		"message": serializeInboundHookMessage(msg),
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
	out := map[string]any{
		"message_id":        msg.MessageID,
		"conversation_kind": msg.ConversationKind,
		"target_id":         msg.TargetID,
		"user_id":           msg.UserID,
		"nickname":          msg.Nickname,
		"group_card":        msg.GroupCard,
		"content":           msg.Content,
		"final_content":     msg.FinalContent,
		"is_mentioned":      msg.IsMentioned,
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
