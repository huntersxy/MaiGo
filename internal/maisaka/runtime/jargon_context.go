package runtime

import (
	"context"
	"strconv"
	"strings"

	"MaiGo/internal/common/database"
	"MaiGo/internal/learners"
	"MaiGo/internal/maisaka/builtin_tool"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const kindJargonReference = "jargon"

// refreshJargonReference 对齐 Maisaka _refresh_jargon_reference_message，在 pending 用户消息前注入黑话参考
func (a *Agent) refreshJargonReference(ctx context.Context, session *Session, messages []*schema.AgenticMessage) []*schema.AgenticMessage {
	if session == nil || len(messages) == 0 {
		return messages
	}
	if session.hasPendingJargonReference() {
		return messages
	}
	sessionID := a.resolveMaiBotSessionID(ctx, session.kind, session.targetID)
	if sessionID == "" {
		return messages
	}

	excluded := session.jargonExcludedContents()
	ctxMsgs := session.jargonMatchContext(a.BotSelfID(), a.conversationPlatform(session.kind, session.targetID))
	matches := learners.MatchJargon(ctx, sessionID, ctxMsgs, excluded)
	if len(matches) == 0 {
		return messages
	}
	body := learners.BuildJargonReferenceBody(matches)
	if strings.TrimSpace(body) == "" {
		return messages
	}

	refMsg := schema.UserAgenticMessage(systemNote("黑话参考", body))
	refMsg.Extra = map[string]any{"source": "internal_context", "kind": kindJargonReference}
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
		zap.L().Debug("写入黑话参考 transcript 失败",
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
	return out
}

func (s *Session) hasPendingJargonReference() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.seen {
		entry := &s.seen[i]
		if entry.transcriptID != 0 && entry.visibleSeq == 0 && isJargonReferenceEntry(entry.msg) {
			return true
		}
	}
	return false
}

func (s *Session) jargonExcludedContents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]struct{}{}
	var excluded []string
	add := func(text string) {
		for _, term := range learners.ExtractJargonReferenceContents(text) {
			key := learners.NormalizeMatchText(term)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			excluded = append(excluded, term)
		}
	}
	for _, entry := range s.seen {
		if entry.msg == nil {
			continue
		}
		if isJargonReferenceEntry(entry.msg) {
			add(agenticMessageText(entry.msg))
			continue
		}
		if text := agenticMessageText(entry.msg); learners.IsJargonReferenceText(text) {
			add(text)
		}
	}
	return excluded
}

func (s *Session) jargonMatchContext(botSelfID int64, platform string) []learners.ContextMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]learners.ContextMessage, 0, len(s.seen)+len(s.pending))
	appendUser := func(user *sessionUser) {
		if user == nil || user.IsAgentMessage {
			return
		}
		if botSelfID > 0 && user.SenderID == botSelfID {
			return
		}
		text := strings.TrimSpace(user.Content)
		if text == "" {
			text = strings.TrimSpace(user.Text)
		}
		if text == "" {
			return
		}
		messageID := strings.TrimSpace(user.Ref)
		if messageID == "" && user.MessageID != 0 {
			messageID = strconv.FormatInt(user.MessageID, 10)
		}
		if messageID == "" {
			messageID = user.NoticeID
		}
		userID := ""
		if user.SenderID > 0 {
			userID = strconv.FormatInt(user.SenderID, 10)
		}
		out = append(out, learners.ContextMessage{
			MessageID:  messageID,
			SourceKind: "user",
			Text:       text,
			Platform:   platform,
			UserID:     userID,
			Name:       strings.TrimSpace(user.Nickname),
			IsNotify:   user.IsNotify,
		})
	}
	for _, entry := range s.seen {
		appendUser(entry.user)
	}
	for _, user := range s.pending {
		appendUser(user)
	}
	return out
}

func isJargonReferenceEntry(message *schema.AgenticMessage) bool {
	if message == nil || message.Extra == nil {
		return false
	}
	kind, _ := message.Extra["kind"].(string)
	return kind == kindJargonReference
}
