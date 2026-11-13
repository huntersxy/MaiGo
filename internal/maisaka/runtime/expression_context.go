package runtime

import (
	"context"

	"maibot-go/internal/chat/replyer"
	"maibot-go/internal/common/database"
	"maibot-go/internal/learners"
	"maibot-go/internal/maisaka/builtin_tool"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const kindExpressionHabits = replyer.ExpressionHabitsReferenceKind

// refreshExpressionHabits 对齐 Maisaka replyer 在生成前注入 expression_habits 用户块
//
// 消息块构造与 kind 判定在 chat/replyer；这里只负责会话解析、transcript 落库与插入位置。
func (a *Agent) refreshExpressionHabits(ctx context.Context, session *Session, messages []*schema.AgenticMessage) []*schema.AgenticMessage {
	if session == nil || len(messages) == 0 {
		return messages
	}
	if session.hasPendingExpressionHabits() {
		return messages
	}
	sessionID := a.resolveMaiBotSessionID(ctx, session.kind, session.targetID)
	if sessionID == "" {
		return messages
	}

	targetMessage := session.lastUserMessageText()
	ctxMsgs := session.jargonMatchContext(a.BotSelfID(), a.conversationPlatform(session.kind, session.targetID))
	result := learners.MatchExpressions(ctx, sessionID, ctxMsgs, targetMessage, "")
	refMsg := replyer.BuildExpressionHabitsMessage(result.ExpressionHabits, result.SelectedExpressionIDs)
	if refMsg == nil {
		return messages
	}
	payload, err := sonic.Marshal(refMsg)
	if err != nil {
		return messages
	}
	mem := a.memory.WithContext(ctx)
	entry := &memory.TranscriptEntry{
		ConversationKind: session.kind,
		TargetID:         session.targetID,
		EntryKind:        "internal_reference",
		Payload:          payload,
	}
	if err := mem.AppendTranscript(ctx, entry); err != nil {
		zap.L().Debug("写入表达习惯 transcript 失败",
			zap.String("conversation_kind", session.kind),
			zap.Int64("target_id", session.targetID),
			zap.Error(err))
		return messages
	}

	pending := session.PendingMessages()
	insertAt := len(messages) - len(pending)
	if insertAt < 0 {
		insertAt = len(messages)
	}
	out := append([]*schema.AgenticMessage(nil), messages[:insertAt]...)
	out = append(out, refMsg)
	out = append(out, messages[insertAt:]...)

	session.mu.Lock()
	session.seen = append(session.seen, sessionEntry{msg: refMsg, transcriptID: entry.ID})
	session.mu.Unlock()

	if tc := tools.GetToolContext(ctx); tc != nil {
		tc.VisibleTranscriptIDs = append(tc.VisibleTranscriptIDs, entry.ID)
	}

	zap.L().Debug("思考轮注入表达习惯",
		zap.String("session_id", sessionID),
		zap.Int("count", len(result.SelectedExpressionIDs)))
	return out
}

func (s *Session) hasPendingExpressionHabits() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.seen {
		entry := &s.seen[i]
		if entry.transcriptID != 0 && entry.visibleSeq == 0 && replyer.IsExpressionHabitsMessage(entry.msg) {
			return true
		}
	}
	return false
}
