package runtime

import (
	"context"
	"strconv"
	"strings"

	"maibot-go/internal/common/database"
	"maibot-go/internal/maisaka"
	"maibot-go/internal/maisaka/builtin_tool"
)

// doReplySend 发送可见回复并回传逐段真实结果，对齐 Python reply.execute_reply 的发送循环
func (a *Agent) doReplySend(ctx context.Context, session *Session, kind string, targetID int64, content string, replyTo int64, mentions []int64) (tools.ReplySendOutcome, error) {
	return a.doReplySendWith(ctx, session, kind, targetID, content, replyTo, mentions, a.sendOutbound)
}

// doReplySendWith 与 doReplySend 相同，但显式指定发送入口，便于单测逐段结果
func (a *Agent) doReplySendWith(ctx context.Context, session *Session, kind string, targetID int64, content string, replyTo int64, mentions []int64, send speakSender) (tools.ReplySendOutcome, error) {
	segments := a.pipelineSegments(ctx, content)
	effectiveReplyTo := replyTo
	if session != nil {
		effectiveReplyTo = a.applyQuoteDecision(ctx, session, kind, targetID, replyTo, firstSegmentText(segments))
	}
	outcome := tools.ReplySendOutcome{
		SetQuote:          replyTo != 0,
		EffectiveSetQuote: effectiveReplyTo != 0,
	}
	if tc := tools.GetToolContext(ctx); tc != nil && tc.EffectiveReplyTo != nil {
		*tc.EffectiveReplyTo = effectiveReplyTo
	}
	var (
		results []tools.ReplySendSegment
		err     error
	)
	if kind == memory.ConversationKindPrivate {
		results, err = a.sendPrivateSpeakSegmentsDetailed(ctx, targetID, segments, effectiveReplyTo, send, true)
	} else {
		results, err = a.sendGroupSpeakSegmentsDetailed(ctx, targetID, segments, effectiveReplyTo, mentions, send, true)
	}
	if err != nil {
		return tools.ReplySendOutcome{}, err
	}
	outcome.Segments = results
	outcome.Sent = len(results) > 0
	for _, result := range results {
		if !result.Success {
			outcome.Sent = false
			break
		}
	}
	if len(results) > 0 {
		outcome.EffectiveSetQuote = results[0].SetQuote
	}
	return outcome, nil
}

// resolveMessageAuthor 按 msg_id 解析消息作者，供 reply 回执显示目标称呼
func (a *Agent) resolveMessageAuthor(session *Session, msgID string) (tools.MessageAuthor, bool) {
	if session == nil {
		return tools.MessageAuthor{}, false
	}
	runtime := session.MaisakaRuntime()
	if runtime == nil {
		return tools.MessageAuthor{}, false
	}
	raw := strings.TrimSpace(msgID)
	if raw == "" {
		return tools.MessageAuthor{}, false
	}
	history := runtime.History()
	messageIDs := make([]string, 0, len(history))
	for _, item := range history {
		if item.MessageID != "" {
			messageIDs = append(messageIDs, item.MessageID)
		}
	}
	if expanded, ok := maisaka.ExpandMessageIDAliases(raw, maisaka.BuildAliasMap(messageIDs)).(string); ok && expanded != "" {
		raw = expanded
	}
	message, ok := runtime.FindSourceMessageByID(raw)
	if !ok {
		return tools.MessageAuthor{}, false
	}
	author := tools.MessageAuthor{
		UserID:   strings.TrimSpace(message.UserID),
		Nickname: replyMetadataString(message.Metadata, "sender_name", "nickname"),
		Cardname: replyMetadataString(message.Metadata, "sender_cardname"),
	}
	if author.UserID == "" {
		author.UserID = replyMetadataString(message.Metadata, "sender_id")
	}
	if selfID := a.SelfID(); selfID > 0 && author.UserID == strconv.FormatInt(selfID, 10) {
		author.IsSelf = true
	}
	return author, true
}

// replyMetadataString 依次读取元数据中的首个非空字符串
func replyMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := metadata[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		default:
			if text := strings.TrimSpace(replyMetadataText(value)); text != "" {
				return text
			}
		}
	}
	return ""
}

func replyMetadataText(value any) string {
	switch typed := value.(type) {
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		return ""
	}
}
