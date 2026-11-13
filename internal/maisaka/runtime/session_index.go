package runtime

import (
	"sync"

	chatsession "maibot-go/internal/chat/session"
)

// sessionIndex 按会话键缓存的运行期索引
//
// 承载最近入站平台、出站路由身份、外部 session_id、内部聊天目标标记与
// 虚拟群原始 group_id 五类索引。键统一由 chatsession.ConversationKey 生成，
// 读写都经过本类型。
type sessionIndex struct {
	platforms      sync.Map // conversation_key -> platform
	routes         sync.Map // conversation_key -> platformRoute
	chatSessionIDs sync.Map // conversation_key -> session_id
	internal       sync.Map // conversation_key -> struct{}
	groupKeys      sync.Map // conversation_key -> 原始群 ID
}

// newSessionIndex 创建空索引
func newSessionIndex() *sessionIndex {
	return &sessionIndex{}
}

func (x *sessionIndex) setPlatform(kind string, targetID int64, platform string) {
	if x == nil || platform == "" {
		return
	}
	x.platforms.Store(chatsession.ConversationKey(kind, targetID), platform)
}

func (x *sessionIndex) platform(kind string, targetID int64) (string, bool) {
	if x == nil {
		return "", false
	}
	value, ok := x.platforms.Load(chatsession.ConversationKey(kind, targetID))
	if !ok {
		return "", false
	}
	platform, ok := value.(string)
	if !ok || platform == "" {
		return "", false
	}
	return platform, true
}

func (x *sessionIndex) route(kind string, targetID int64) (platformRoute, bool) {
	if x == nil {
		return platformRoute{}, false
	}
	value, ok := x.routes.Load(chatsession.ConversationKey(kind, targetID))
	if !ok {
		return platformRoute{}, false
	}
	route, ok := value.(platformRoute)
	return route, ok
}

func (x *sessionIndex) setRoute(kind string, targetID int64, route platformRoute) {
	if x == nil {
		return
	}
	x.routes.Store(chatsession.ConversationKey(kind, targetID), route)
}

func (x *sessionIndex) setChatSessionID(kind string, targetID int64, sessionID string) {
	if x == nil || sessionID == "" {
		return
	}
	x.chatSessionIDs.Store(chatsession.ConversationKey(kind, targetID), sessionID)
}

func (x *sessionIndex) chatSessionID(kind string, targetID int64) (string, bool) {
	if x == nil {
		return "", false
	}
	value, ok := x.chatSessionIDs.Load(chatsession.ConversationKey(kind, targetID))
	if !ok {
		return "", false
	}
	sessionID, ok := value.(string)
	if !ok || sessionID == "" {
		return "", false
	}
	return sessionID, true
}

func (x *sessionIndex) markInternal(kind string, targetID int64) {
	if x == nil {
		return
	}
	x.internal.Store(chatsession.ConversationKey(kind, targetID), struct{}{})
}

func (x *sessionIndex) isInternal(kind string, targetID int64) bool {
	if x == nil {
		return false
	}
	_, ok := x.internal.Load(chatsession.ConversationKey(kind, targetID))
	return ok
}

func (x *sessionIndex) setGroupKey(kind string, targetID int64, groupKey string) {
	if x == nil || groupKey == "" {
		return
	}
	x.groupKeys.Store(chatsession.ConversationKey(kind, targetID), groupKey)
}

func (x *sessionIndex) groupKey(kind string, targetID int64) (string, bool) {
	if x == nil {
		return "", false
	}
	value, ok := x.groupKeys.Load(chatsession.ConversationKey(kind, targetID))
	if !ok {
		return "", false
	}
	groupKey, ok := value.(string)
	if !ok || groupKey == "" {
		return "", false
	}
	return groupKey, true
}

// sessionIndex 返回会话索引，按需初始化
func (a *Agent) sessionIndex() *sessionIndex {
	if a == nil {
		return nil
	}
	a.indexMu.Lock()
	defer a.indexMu.Unlock()
	if a.index == nil {
		a.index = newSessionIndex()
	}
	return a.index
}
