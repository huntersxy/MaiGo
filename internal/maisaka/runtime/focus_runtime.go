package runtime

import (
	"context"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/chat/replyer"
	"maibot-go/internal/common/database"
	"maibot-go/internal/maisaka/quote"
	"maibot-go/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
)

func (a *Agent) quoteFrequency(isGroup bool) quote.Frequency {
	if a == nil {
		return quote.FrequencyOccasionally
	}
	a.instanceMu.RLock()
	holder := a.instanceHolder
	a.instanceMu.RUnlock()
	if holder == nil {
		return quote.FrequencyOccasionally
	}
	cfg := holder.Config()
	if cfg == nil {
		return quote.FrequencyOccasionally
	}
	raw := cfg.Bot.Chat.ReplyStyle.PrivateQuoteFrequency
	if isGroup {
		raw = cfg.Bot.Chat.ReplyStyle.GroupQuoteFrequency
	}
	return quote.ParseFrequency(raw)
}

// recordQuoteInbound 在消息落库进入会话时更新引用计数
func (a *Agent) recordQuoteInbound(ctx context.Context, session *Session, msg *onebot.ConversationMessage) {
	if a == nil || session == nil || msg == nil || msg.MessageID == 0 {
		return
	}
	if msg.UserID == a.SelfID() {
		return
	}
	sessionID := a.resolveMaiBotSessionID(ctx, msg.ConversationKind, msg.TargetID)
	counter := session.focusRuntime.EnsureQuoteCounter(sessionID)
	if counter == nil {
		return
	}
	senderID := strconv.FormatInt(msg.UserID, 10)
	at := msg.Time
	if at.IsZero() {
		at = time.Now()
	}
	if err := counter.RecordMessage(strconv.FormatInt(msg.MessageID, 10), senderID, at, false); err != nil {
		zap.L().Debug("引用计数记录入站消息失败",
			zap.String("conversation_kind", msg.ConversationKind),
			zap.Int64("target_id", msg.TargetID),
			zap.Int64("message_id", msg.MessageID),
			zap.Error(err))
	}
}

// recordQuoteOutbound 在 bot 发言成功后写入引用计数
func (a *Agent) recordQuoteOutbound(ctx context.Context, kind string, targetID, messageID, botUserID int64, at time.Time) {
	if a == nil || messageID == 0 || botUserID == 0 {
		return
	}
	session := a.sessions.get(kind, targetID)
	sessionID := a.resolveMaiBotSessionID(ctx, kind, targetID)
	counter := session.focusRuntime.EnsureQuoteCounter(sessionID)
	if counter == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	if err := counter.RecordMessage(strconv.FormatInt(messageID, 10), strconv.FormatInt(botUserID, 10), at, false); err != nil {
		zap.L().Debug("引用计数记录出站消息失败",
			zap.String("conversation_kind", kind),
			zap.Int64("target_id", targetID),
			zap.Int64("message_id", messageID),
			zap.Error(err))
	}
}

func (s *Session) lastInboundMessageID() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pick := func(user *sessionUser) int64 {
		if user == nil || user.IsAgentMessage || user.MessageID == 0 {
			return 0
		}
		return user.MessageID
	}
	for i := len(s.pending) - 1; i >= 0; i-- {
		if id := pick(s.pending[i]); id != 0 {
			return id
		}
	}
	for i := len(s.seen) - 1; i >= 0; i-- {
		if id := pick(s.seen[i].user); id != 0 {
			return id
		}
	}
	return 0
}

func firstSegmentText(segments []replyer.Segment) string {
	for _, seg := range segments {
		if text := strings.TrimSpace(seg.Text); text != "" {
			return text
		}
	}
	return ""
}

// applyQuoteDecision 对齐 Maisaka reply 首段引用抽签；显式 reply_to 仍参与抽签
func (a *Agent) applyQuoteDecision(
	ctx context.Context,
	session *Session,
	kind string,
	targetID int64,
	candidateReplyTo int64,
	text string,
) int64 {
	if a == nil || session == nil {
		return candidateReplyTo
	}
	isGroup := kind == memory.ConversationKindGroup
	frequency := a.quoteFrequency(isGroup)
	if frequency == quote.FrequencyNever {
		return 0
	}
	targetIDMsg := candidateReplyTo
	if targetIDMsg == 0 {
		targetIDMsg = session.lastInboundMessageID()
	}
	if targetIDMsg == 0 {
		return 0
	}
	sessionID := a.resolveMaiBotSessionID(ctx, kind, targetID)
	counter := session.focusRuntime.EnsureQuoteCounter(sessionID)
	if counter == nil {
		return targetIDMsg
	}
	targetKey := strconv.FormatInt(targetIDMsg, 10)
	snap, err := counter.Snapshot(targetKey, sessionID, false, time.Now())
	if err != nil {
		zap.L().Debug("引用计数快照失败",
			zap.String("conversation_kind", kind),
			zap.Int64("target_id", targetID),
			zap.Int64("reply_to", targetIDMsg),
			zap.Error(err))
		return 0
	}
	botUserID := strconv.FormatInt(a.SelfID(), 10)
	ok, _, err := quote.Decide(snap, botUserID, text, frequency, isGroup, nil)
	if err != nil {
		zap.L().Debug("引用抽签失败",
			zap.String("conversation_kind", kind),
			zap.Int64("target_id", targetID),
			zap.Error(err))
		return 0
	}
	if !ok {
		return 0
	}
	return targetIDMsg
}
