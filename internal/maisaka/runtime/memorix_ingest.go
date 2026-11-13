package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/A_memorix"
	"maibot-go/internal/common/database"
	"maibot-go/internal/person_info"
	"maibot-go/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
	"maibot-go/internal/platform_io"
)

const memorixInboundSourceType = "chat_stream"

// memorixInboundMetadata 构造 A_memorix ingest_text 的 metadata JSON，键名是外部存储契约而非 mai_messages 列名
func memorixInboundMetadata(msg *onebot.ConversationMessage) map[string]any {
	return map[string]any{
		"onebot_message_id": msg.MessageID,
		"conversation_kind": msg.ConversationKind,
	}
}

// ingestMemorixInbound 入站用户消息提交后 best-effort 写入 Memorix Host
func (a *Agent) ingestMemorixInbound(ctx context.Context, msg *onebot.ConversationMessage) {
	if msg == nil || msg.MessageID == 0 || msg.UserID == a.SelfID() {
		return
	}
	text := strings.TrimSpace(msg.FinalContent)
	if text == "" {
		text = strings.TrimSpace(msg.Content)
	}
	if text == "" {
		return
	}

	host := memorix.DefaultRegistry().Host()
	if host == nil {
		return
	}

	sessionID := a.resolveMaiBotSessionID(ctx, msg.ConversationKind, msg.TargetID)
	userID := strconv.FormatInt(msg.UserID, 10)
	groupID := ""
	if msg.ConversationKind == memory.ConversationKindGroup {
		groupID = strconv.FormatInt(msg.TargetID, 10)
	}

	var ts *time.Time
	switch {
	case !msg.Time.IsZero():
		t := msg.Time
		ts = &t
	case !msg.ReceivedAt.IsZero():
		t := msg.ReceivedAt
		ts = &t
	}

	personID := personinfo.PersonID(platformio.PlatformQQ, userID)
	req := memorix.IngestMessageRequest{
		ExternalID: fmt.Sprintf("onebot:%s:%d:%d", msg.ConversationKind, msg.TargetID, msg.MessageID),
		SourceType: memorixInboundSourceType,
		Text:       text,
		ChatID:     sessionID,
		PersonIDs:  []string{personID},
		Timestamp:  ts,
		UserID:     userID,
		GroupID:    groupID,
		Metadata:   memorixInboundMetadata(msg),
	}

	result, err := host.IngestMessage(ctx, req)
	if err != nil {
		zap.L().Warn("Memorix 入站写入失败",
			zap.String("conversation_kind", msg.ConversationKind),
			zap.Int64("target_id", msg.TargetID),
			zap.Int64("message_id", msg.MessageID),
			zap.Error(err))
		return
	}
	if !result.Success && !result.Disabled {
		zap.L().Warn("Memorix 入站写入未成功",
			zap.String("conversation_kind", msg.ConversationKind),
			zap.Int64("target_id", msg.TargetID),
			zap.Int64("message_id", msg.MessageID),
			zap.String("detail", result.Detail))
	}
}
