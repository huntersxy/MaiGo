package runtime

import (
	"strconv"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/database"
	"maibot-go/internal/maisaka/monitor"
)

func (a *Agent) monitorThinkSessionID(kind string, targetID int64) string {
	if a.memory == nil || targetID <= 0 {
		return ""
	}
	mgr := chatsession.NewManager(a.memory.GetDB())
	rec, err := mgr.ResolveSession(a.ctx, a.conversationPlatform(kind, targetID), strconv.FormatInt(targetID, 10), kind)
	if err != nil || rec == nil {
		return ""
	}
	return rec.SessionID
}

func (a *Agent) emitMonitorThinkStart(kind string, targetID int64) {
	monitor.EmitThinkStart(kind, targetID, a.monitorThinkSessionID(kind, targetID))
	a.callbacksMu.RLock()
	notifier := a.callbacks.ThinkingNotifier
	a.callbacksMu.RUnlock()
	if notifier != nil && kind == memory.ConversationKindPrivate && a.isInternalConversation(kind, targetID) {
		notifier(kind, targetID)
	}
}

func (a *Agent) emitMonitorThinkEnd(kind string, targetID int64) {
	monitor.EmitThinkEnd(kind, targetID, a.monitorThinkSessionID(kind, targetID))
}
