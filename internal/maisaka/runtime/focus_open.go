package runtime

import (
	"context"
	"strconv"
	"strings"

	"maibot-go/internal/common/database"
	"maibot-go/internal/maisaka"
	"maibot-go/internal/maisaka/focus"

	"go.uber.org/zap"
)

// OpenSession 从检查点恢复其他真实会话，供 Focus 共享组重启后继续合流
func (r *focusGroupRegistry) OpenSession(sessionID string) (context.Context, focus.Member, bool) {
	if r == nil || r.manager == nil || r.agent == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil, false
	}
	ctx, cancel := r.agent.persistenceContext()
	defer cancel()
	rec, err := r.agent.chatSessions.GetExistingBySessionID(ctx, sessionID)
	kind := memory.ConversationKindPrivate
	targetRaw := ""
	if err == nil && rec != nil {
		if rec.IsGroup() && rec.GroupID != nil {
			kind = memory.ConversationKindGroup
			targetRaw = strings.TrimSpace(*rec.GroupID)
		} else if rec.UserID != nil {
			targetRaw = strings.TrimSpace(*rec.UserID)
		}
	} else {
		// 会话表没有记录时，尝试仅凭 Focus 检查点里的成员身份重建
		checkpoint, checkpointErr := focus.FindCheckpointForSession(sessionID, focus.DefaultCheckpointRoot)
		if checkpointErr != nil || checkpoint == nil {
			return nil, nil, false
		}
		member := checkpoint.Members[sessionID]
		if member.Kind == memory.ConversationKindGroup {
			kind = memory.ConversationKindGroup
		} else if member.Kind == memory.ConversationKindPrivate {
			kind = memory.ConversationKindPrivate
		} else if strings.HasPrefix(checkpoint.ScopeID, "group:") {
			kind = memory.ConversationKindGroup
		}
		if member.TargetID > 0 {
			targetRaw = strconv.FormatInt(member.TargetID, 10)
		}
		if member.Platform != "" && member.TargetID > 0 {
			index := r.agent.sessionIndex()
			index.setPlatform(kind, member.TargetID, member.Platform)
			index.setChatSessionID(kind, member.TargetID, sessionID)
		}
		zap.L().Info("Focus 共享组仅凭检查点重建会话",
			zap.String("session_id", sessionID), zap.String("scope_id", checkpoint.ScopeID), zap.String("kind", kind))
	}
	targetID, parseErr := strconv.ParseInt(targetRaw, 10, 64)
	if parseErr != nil || targetID == 0 {
		return nil, nil, false
	}
	session := r.agent.sessions.get(kind, targetID)
	if session == nil {
		return nil, nil, false
	}
	r.register(sessionID, session)
	runtime := r.agent.attachMaisakaRuntime(kind, targetID, session)
	if runtime == nil {
		return nil, nil, false
	}
	if messages := loadFocusSessionHistory(ctx, r.agent, session, sessionID); len(messages) > 0 {
		runtime.SetHistory(messages)
	}
	member := &focusGroupMember{agent: r.agent, runtime: runtime, session: session, sessionID: sessionID}
	zap.L().Info("Focus 共享组按检查点恢复会话", zap.String("session_id", sessionID))
	return r.agent.ctx, member, true
}

// loadFocusSessionHistory 读取共享组检查点中属于指定会话的完整消息
func loadFocusSessionHistory(ctx context.Context, a *Agent, session *Session, sessionID string) []maisaka.ContextMessage {
	if a == nil || session == nil || sessionID == "" {
		return nil
	}
	settings, _, ok := a.focusExperimentalSnapshot()
	if !ok {
		return nil
	}
	info, ok := a.focusSessionInfo(ctx, session)
	if !ok {
		return nil
	}
	scopeID, err := focus.GetFocusScopeKey(settings, info)
	if err != nil || scopeID == "" {
		return nil
	}
	store := focus.NewCheckpointStore(scopeID, focus.DefaultCheckpointRoot)
	checkpoint, err := store.Load()
	if err != nil || checkpoint == nil {
		if err != nil {
			zap.L().Debug("读取 Focus 检查点失败", zap.String("scope_id", scopeID), zap.Error(err))
		}
		return nil
	}
	entries := focus.DecodeCheckpointEntries(checkpoint)
	messages := make([]maisaka.ContextMessage, 0)
	for _, entry := range entries {
		for _, message := range entry.Messages {
			if message.SessionID != sessionID {
				continue
			}
			messages = append(messages, message)
		}
	}
	return messages
}
