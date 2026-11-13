package runtime

import (
	"context"
	"strconv"
	"strings"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/database"
	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/maisaka/builtin_tool"
	"maibot-go/internal/maisaka/focus"
	"maibot-go/internal/prompt"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"maibot-go/internal/platform_io"
)

const kindFocusContext = "focus"

// refreshFocusContext 在 Focus 会话启用时注入注意力漂移与 roster 提示块
func (a *Agent) refreshFocusContext(ctx context.Context, session *Session, messages []*schema.AgenticMessage) []*schema.AgenticMessage {
	if session == nil || len(messages) == 0 {
		return messages
	}
	settings, _, ok := a.focusExperimentalSnapshot()
	if !ok {
		return messages
	}
	info, ok := a.focusSessionInfo(ctx, session)
	if !ok || !focus.IsEnabledForSession(settings, info) {
		return messages
	}

	rosterText := focus.BuildFocusRoster(a.focusRosterMembers(ctx, session, settings, info))
	if session.hasPendingFocusContext() && session.focusRuntime.RosterSnapshot() == rosterText {
		return messages
	}
	body := a.buildFocusPromptBody(ctx, session, settings, info, rosterText)
	if strings.TrimSpace(body) == "" {
		session.focusRuntime.SetRosterSnapshot(rosterText)
		return messages
	}

	refMsg := schema.UserAgenticMessage(systemNote("Focus 上下文", body))
	refMsg.Extra = map[string]any{"source": "internal_context", "kind": kindFocusContext}
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
		zap.L().Debug("写入 Focus 上下文 transcript 失败",
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
	session.focusRuntime.SetRosterSnapshot(rosterText)

	if tc := tools.GetToolContext(ctx); tc != nil {
		tc.VisibleTranscriptIDs = append(tc.VisibleTranscriptIDs, entry.ID)
	}
	return out
}

func (a *Agent) focusExperimentalSnapshot() (focus.Settings, maibotcfg.ExperimentalConfig, bool) {
	if a == nil {
		return focus.Settings{}, maibotcfg.ExperimentalConfig{}, false
	}
	a.instanceMu.RLock()
	holder := a.instanceHolder
	a.instanceMu.RUnlock()
	if holder == nil {
		return focus.Settings{}, maibotcfg.ExperimentalConfig{}, false
	}
	cfg := holder.Config()
	if cfg == nil {
		return focus.Settings{}, maibotcfg.ExperimentalConfig{}, false
	}
	settings := focus.SettingsFromExperimental(cfg.Bot.Experimental, cfg.Bot.Chat)
	return settings, cfg.Bot.Experimental, true
}

func (a *Agent) focusSessionInfo(ctx context.Context, session *Session) (focus.SessionInfo, bool) {
	sessionID := a.resolveMaiBotSessionID(ctx, session.kind, session.targetID)
	if sessionID == "" {
		return focus.SessionInfo{}, false
	}
	platform := a.conversationPlatform(session.kind, session.targetID)
	if platform == "" {
		platform = platformio.PlatformQQ
	}
	info := focus.SessionInfo{
		SessionID: sessionID,
		IsGroup:   session.kind == memory.ConversationKindGroup,
		Platform:  platform,
	}
	if session.kind == memory.ConversationKindGroup {
		info.GroupID = strconv.FormatInt(session.targetID, 10)
	} else {
		info.UserID = strconv.FormatInt(session.targetID, 10)
	}
	if a.chatSessions != nil {
		if rec, err := a.chatSessions.ResolveSession(ctx, platform, strconv.FormatInt(session.targetID, 10), session.kind); err == nil && rec != nil {
			if rec.Platform != "" {
				info.Platform = rec.Platform
			}
			if rec.GroupID != nil {
				info.GroupID = strings.TrimSpace(*rec.GroupID)
			}
			if rec.UserID != nil {
				info.UserID = strings.TrimSpace(*rec.UserID)
			}
		}
	}
	return info, true
}

func (a *Agent) buildFocusPromptBody(
	ctx context.Context,
	session *Session,
	settings focus.Settings,
	info focus.SessionInfo,
	rosterText string,
) string {
	var parts []string
	if rosterText != "" {
		parts = append(parts, rosterText)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// buildAttentionDriftBlock 渲染注意力漂移提示词块，供 Planner 系统提示词使用
func (a *Agent) buildAttentionDriftBlock(isGroup bool) string {
	_, experimental, ok := a.focusExperimentalSnapshot()
	if !ok {
		return ""
	}
	return focus.BuildAttentionDriftPromptBlock(&isGroup, prompt.InitialLocale(), experimental.GroupAttentionDrift, experimental.PrivateAttentionDrift)
}

func (a *Agent) focusRosterMembers(ctx context.Context, session *Session, settings focus.Settings, info focus.SessionInfo) []focus.MemberAttention {
	group := matchingFocusGroup(settings, info)
	if group == nil || len(group.Targets) == 0 {
		return []focus.MemberAttention{a.focusMemberForSession(ctx, session, info)}
	}
	members := make([]focus.MemberAttention, 0, len(group.Targets))
	seen := map[string]struct{}{}
	for _, target := range group.Targets {
		rec := a.focusSessionRecordForTarget(ctx, target)
		if rec == nil || rec.SessionID == "" {
			continue
		}
		if _, ok := seen[rec.SessionID]; ok {
			continue
		}
		seen[rec.SessionID] = struct{}{}
		member := focus.MemberAttention{
			SessionID:   rec.SessionID,
			SessionName: chatsession.DisplayName(rec),
		}
		if rec.SessionID == info.SessionID {
			member.Instructions = a.focusChatInstructions(ctx, session)
		}
		members = append(members, member)
	}
	if len(members) == 0 {
		return []focus.MemberAttention{a.focusMemberForSession(ctx, session, info)}
	}
	return members
}

func matchingFocusGroup(settings focus.Settings, info focus.SessionInfo) *maibotcfg.ChatStreamGroup {
	for i := range settings.FocusGroups {
		group := &settings.FocusGroups[i]
		if focusGroupMatchesSession(*group, info) {
			return group
		}
	}
	return nil
}

func focusGroupMatchesSession(group maibotcfg.ChatStreamGroup, session focus.SessionInfo) bool {
	for _, target := range group.Targets {
		if focus.TargetMatchesSessionWithWildcards(target, session) {
			return true
		}
	}
	return false
}

func (a *Agent) focusSessionRecordForTarget(ctx context.Context, target maibotcfg.TargetItem) *chatsession.Record {
	if a.chatSessions == nil {
		return nil
	}
	platform := strings.TrimSpace(target.Platform)
	if platform == "" {
		platform = platformio.PlatformQQ
	}
	itemID := strings.TrimSpace(target.ItemId)
	if itemID == "" {
		return nil
	}
	var userID, groupID string
	switch strings.TrimSpace(target.RuleType) {
	case "group":
		groupID = itemID
	case "private":
		userID = itemID
	default:
		return nil
	}
	accountID := ""
	if a.SelfID() > 0 {
		accountID = strconv.FormatInt(a.SelfID(), 10)
	}
	rec, err := a.chatSessions.GetOrCreateSession(ctx, platform, userID, groupID, accountID, "")
	if err != nil || rec == nil {
		return nil
	}
	return rec
}

func (a *Agent) focusMemberForSession(ctx context.Context, session *Session, info focus.SessionInfo) focus.MemberAttention {
	name := info.SessionID
	if a.chatSessions != nil {
		if rec, err := a.chatSessions.GetExistingBySessionID(ctx, info.SessionID); err == nil && rec != nil {
			if display := chatsession.DisplayName(rec); display != "" {
				name = display
			}
		}
	}
	return focus.MemberAttention{
		SessionID:    info.SessionID,
		SessionName:  name,
		Instructions: a.focusChatInstructions(ctx, session),
	}
}

func (a *Agent) focusChatInstructions(ctx context.Context, session *Session) string {
	if session == nil || session.kind != memory.ConversationKindGroup {
		return ""
	}
	parts := make([]string, 0, 2)
	if note, err := a.memory.WithContext(ctx).GetGroupExtraPrompt(ctx, session.targetID); err == nil {
		if trimmed := strings.TrimSpace(note); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if bot := a.instanceBotConfig(); bot != nil {
		if prompt := a.chatPromptForSession(bot.Chat.ReplyStyle.ChatPrompts, session.kind, session.targetID, true); prompt != "" {
			parts = append(parts, prompt)
		}
	}
	return strings.Join(parts, "\n")
}

func (s *Session) hasPendingFocusContext() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.seen {
		entry := &s.seen[i]
		if entry.transcriptID != 0 && entry.visibleSeq == 0 && isFocusContextEntry(entry.msg) {
			return true
		}
	}
	return false
}

func isFocusContextEntry(message *schema.AgenticMessage) bool {
	if message == nil || message.Extra == nil {
		return false
	}
	kind, _ := message.Extra["kind"].(string)
	return kind == kindFocusContext
}
