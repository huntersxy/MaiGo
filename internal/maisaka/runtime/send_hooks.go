package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

// 出站 Hook 名称，对齐 MaiBot src/services/send_service.py
const (
	hookSendAfterBuildMessage = "send_service.after_build_message"
	hookSendBeforeSend        = "send_service.before_send"
	hookSendAfterSend         = "send_service.after_send"
)

// sendHookEmitter 抽象出站 Hook 的触发入口，便于在没有完整插件宿主时单测
type sendHookEmitter interface {
	EmitHook(ctx context.Context, hookName string, kwargs map[string]any) hooks.DispatchResult
}

// outboundMessage 描述一条待出站消息（文本或图片），对应 MaiBot 的 SessionMessage
type outboundMessage struct {
	Kind     string
	TargetID int64
	SelfID   int64
	Text     string
	Mentions []int64
	ReplyTo  int64
	// ReplyAnchor 被动回复锚点，独立于显示引用，供适配器占用被动额度
	ReplyAnchor int64
	ImageFile   string
	IsSticker   bool

	Typing   bool
	SetReply bool
	Storage  bool
	ShowLog  bool

	// storageExplicit/showLogExplicit 区分调用方显式关闭与零值默认，默认与 MaiBot 一致为开启
	storageExplicit bool
	showLogExplicit bool

	MessageID         int64
	ExternalMessageID string
}

func (m *outboundMessage) isImage() bool {
	return m != nil && strings.TrimSpace(m.ImageFile) != ""
}

// prepare 填充与 MaiBot 发送默认值一致的字段
func (m *outboundMessage) prepare(a *Agent) {
	if m == nil {
		return
	}
	if m.SelfID == 0 && a != nil {
		m.SelfID = a.SelfID()
	}
	m.SetReply = m.ReplyTo != 0
	if !m.storageExplicit {
		m.Storage = true
	}
	if !m.showLogExplicit {
		m.ShowLog = true
	}
}

// serialize 序列化出站消息，键名与入站 Hook 的 serializeInboundHookMessage 保持一致
func (m *outboundMessage) serialize() map[string]any {
	if m == nil {
		return map[string]any{}
	}
	mentions := make([]any, 0, len(m.Mentions))
	segments := make([]any, 0, len(m.Mentions)+1)
	if m.isImage() {
		image := map[string]any{"type": "image", "file": m.ImageFile, "is_sticker": m.IsSticker}
		segments = append(segments, image)
	} else {
		for _, userID := range m.Mentions {
			if userID <= 0 {
				continue
			}
			mentions = append(mentions, userID)
			segments = append(segments, map[string]any{"type": "at", "user_id": strconv.FormatInt(userID, 10)})
		}
		segments = append(segments, map[string]any{"type": "text", "text": m.Text})
	}
	out := map[string]any{
		"message_id":        m.MessageID,
		"conversation_kind": m.Kind,
		"target_id":         m.TargetID,
		"self_id":           m.SelfID,
		"content":           m.Text,
		"mentions":          mentions,
		"reply_to":          m.ReplyTo,
		"segments":          segments,
		"is_image":          m.isImage(),
	}
	if m.isImage() {
		out["image"] = map[string]any{"file": m.ImageFile, "is_sticker": m.IsSticker}
	}
	return out
}

// apply 读回 Hook 返回的 message 改写，无法反序列化时按 MaiBot 语义告警并保留原消息
func (m *outboundMessage) apply(kwargs map[string]any, hookName string) {
	if m == nil || kwargs == nil {
		return
	}
	raw, ok := kwargs["message"]
	if !ok || raw == nil {
		return
	}
	patch, ok := raw.(map[string]any)
	if !ok {
		zap.L().Warn("Hook 返回的 message 无法反序列化，已忽略",
			zap.String("hook", hookName), zap.String("value_type", fmt.Sprintf("%T", raw)))
		return
	}
	if value, ok := patch["content"].(string); ok {
		m.Text = value
	}
	if mentions, ok := hookInt64Slice(patch["mentions"]); ok {
		m.Mentions = mentions
	}
	if replyTo, ok := hookInt64(patch["reply_to"]); ok {
		m.ReplyTo = replyTo
	}
	if image, ok := patch["image"].(map[string]any); ok {
		if file, ok := image["file"].(string); ok {
			m.ImageFile = file
		}
		if sticker, ok := image["is_sticker"].(bool); ok {
			m.IsSticker = sticker
		}
	}
}

// runSendHooks 触发 after_build_message 与 before_send，返回 true 表示本次发送被 Hook 中止
func runSendHooks(ctx context.Context, emitter sendHookEmitter, msg *outboundMessage) bool {
	if msg == nil {
		return false
	}
	result := emitSendHook(ctx, emitter, hookSendAfterBuildMessage, msg, map[string]any{
		"stream_id":            chatsession.ConversationKey(msg.Kind, msg.TargetID),
		"processed_plain_text": msg.Text,
		"typing":               msg.Typing,
		"set_reply":            msg.SetReply,
		"storage_message":      msg.Storage,
		"show_log":             msg.ShowLog,
	})
	applySendScalars(result.Kwargs, msg)
	if result.Aborted {
		return true
	}
	result = emitSendHook(ctx, emitter, hookSendBeforeSend, msg, map[string]any{
		"typing":           msg.Typing,
		"set_reply":        msg.SetReply,
		"reply_message_id": replyMessageIDValue(msg.ReplyTo),
		"storage_message":  msg.Storage,
		"show_log":         msg.ShowLog,
	})
	applySendScalars(result.Kwargs, msg)
	// 对齐 Python src/services/send_service.py:591 与 :670-673：
	// reply_to 元数据只要传了 reply_message 就写入，与 set_reply 无关；
	// set_reply 只决定是否构建 ReplyComponent（引用段），由下游按 msg.SetReply 判定
	return result.Aborted
}

// emitAfterSend 触发 after_send；该 Hook 禁止 abort 与 kwargs 改写，只观察最终结果
func emitAfterSend(ctx context.Context, emitter sendHookEmitter, msg *outboundMessage, sent bool) {
	if msg == nil || emitter == nil {
		return
	}
	kwargs := map[string]any{
		"message":          msg.serialize(),
		"sent":             sent,
		"typing":           msg.Typing,
		"set_reply":        msg.SetReply,
		"reply_message_id": replyMessageIDValue(msg.ReplyTo),
		"storage_message":  msg.Storage,
		"show_log":         msg.ShowLog,
	}
	result := emitter.EmitHook(ctx, hookSendAfterSend, kwargs)
	for _, errText := range result.Errors {
		zap.L().Warn("出站 Hook 错误", zap.String("hook", hookSendAfterSend), zap.String("error", errText))
	}
}

func emitSendHook(ctx context.Context, emitter sendHookEmitter, hookName string, msg *outboundMessage, extra map[string]any) hooks.DispatchResult {
	if emitter == nil || msg == nil {
		return hooks.DispatchResult{HookName: hookName}
	}
	kwargs := make(map[string]any, len(extra)+1)
	kwargs["message"] = msg.serialize()
	for key, value := range extra {
		kwargs[key] = value
	}
	result := emitter.EmitHook(ctx, hookName, kwargs)
	msg.apply(result.Kwargs, hookName)
	for _, errText := range result.Errors {
		zap.L().Warn("出站 Hook 错误", zap.String("hook", hookName), zap.String("error", errText))
	}
	if result.Aborted {
		zap.L().Info("出站消息被 Hook 中止",
			zap.String("hook", hookName),
			zap.String("stopped_by", result.StoppedBy),
			zap.String("conversation", chatsession.ConversationKey(msg.Kind, msg.TargetID)))
	}
	return result
}

// applySendScalars 读回 Hook 改写后的标量开关，对齐 send_service 的 _coerce_bool 与 reply_message_id 归一化
func applySendScalars(kwargs map[string]any, msg *outboundMessage) {
	if kwargs == nil || msg == nil {
		return
	}
	msg.Typing = coerceHookBool(kwargs["typing"], msg.Typing)
	msg.SetReply = coerceHookBool(kwargs["set_reply"], msg.SetReply)
	msg.Storage = coerceHookBool(kwargs["storage_message"], msg.Storage)
	msg.storageExplicit = true
	msg.ShowLog = coerceHookBool(kwargs["show_log"], msg.ShowLog)
	msg.showLogExplicit = true
	if raw, ok := kwargs["reply_message_id"]; ok {
		msg.ReplyTo = hookReplyMessageID(raw)
	}
}

// sendOutbound 执行「构建消息 → after_build_message → before_send → 发送 → after_send」管线
func (a *Agent) sendOutbound(ctx context.Context, msg *outboundMessage) (messageID int64, arrivalSeq uint64, sent bool, err error) {
	var emitter sendHookEmitter
	if a != nil && a.pluginHost != nil {
		emitter = a.pluginHost
	}
	return a.sendOutboundWith(ctx, emitter, msg)
}

// sendOutboundWith 与 sendOutbound 相同，但显式指定 Hook 触发入口，便于注入替身做单测
func (a *Agent) sendOutboundWith(ctx context.Context, emitter sendHookEmitter, msg *outboundMessage) (messageID int64, arrivalSeq uint64, sent bool, err error) {
	if a == nil || msg == nil {
		return 0, 0, false, nil
	}
	msg.prepare(a)
	if runSendHooks(ctx, emitter, msg) {
		return 0, 0, false, nil
	}
	// 引用段只在 set_reply 为真时生成；reply_to 元数据始终保留，供存储与 Hook 观察
	replySegment := replySegmentFor(msg)
	var externalID string
	if msg.isImage() {
		messageID, externalID, arrivalSeq, err = a.sendConversationImage(ctx, msg.Kind, msg.TargetID, msg.ImageFile, msg.IsSticker, msg.ReplyAnchor)
	} else {
		messageID, externalID, arrivalSeq, err = a.sendConversationMessage(ctx, msg.Kind, msg.TargetID, msg.Text, replySegment, msg.ReplyAnchor, msg.Mentions)
	}
	if err != nil {
		emitAfterSend(ctx, emitter, msg, false)
		return 0, 0, false, err
	}
	msg.MessageID = messageID
	msg.ExternalMessageID = externalID
	emitAfterSend(ctx, emitter, msg, true)
	return messageID, arrivalSeq, true, nil
}

// replySegmentFor 返回本次发送要构建的引用段目标，0 表示不生成引用段
// 对齐 Python send_service.py:670-673：只有 set_reply 为真才构建 ReplyComponent
func replySegmentFor(msg *outboundMessage) int64 {
	if msg == nil || !msg.SetReply {
		return 0
	}
	return msg.ReplyTo
}

// coerceHookBool 对齐 send_service._coerce_bool：None 回落默认值，其余按 Python 真值语义
func coerceHookBool(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case int8:
		return v != 0
	case int16:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case uint:
		return v != 0
	case uint8:
		return v != 0
	case uint16:
		return v != 0
	case uint32:
		return v != 0
	case uint64:
		return v != 0
	case float32:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return fallback
	}
}

// hookReplyMessageID 对齐 send_service：None 或空串视为无引用，其余转成消息 ID
func hookReplyMessageID(value any) int64 {
	if value == nil {
		return 0
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return 0
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			zap.L().Warn("Hook 返回的 reply_message_id 无法解析，已忽略", zap.String("reply_message_id", text))
			return 0
		}
		return parsed
	}
	if id, ok := hookInt64(value); ok {
		return id
	}
	return 0
}

func replyMessageIDValue(replyTo int64) any {
	if replyTo == 0 {
		return nil
	}
	return strconv.FormatInt(replyTo, 10)
}

func hookInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint:
		return int64(v), true
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		return int64(v), true
	case float32:
		return int64(v), true
	case float64:
		return int64(v), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func hookInt64Slice(value any) ([]int64, bool) {
	switch v := value.(type) {
	case []int64:
		return append([]int64(nil), v...), true
	case []any:
		out := make([]int64, 0, len(v))
		for _, item := range v {
			id, ok := hookInt64(item)
			if !ok {
				return nil, false
			}
			out = append(out, id)
		}
		return out, true
	default:
		return nil, false
	}
}
