package runtime

import (
	"context"
	"strconv"
	"strings"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/database"
	"maibot-go/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
	"maibot-go/internal/platform_io"
)

func (a *Agent) ensureChatSession(ctx context.Context, msg *onebot.ConversationMessage) string {
	if a.chatSessions == nil || msg == nil || (msg.TargetID <= 0 && !msg.HasExternalIdentity()) {
		return ""
	}
	groupName := msg.GroupName
	groupKey := msg.GroupKey
	if msg.ConversationKind == memory.ConversationKindGroup {
		if groupKey == "" {
			groupKey = msg.ExternalTargetID
		}
		if groupKey == "" {
			groupKey = a.conversationGroupKey(msg.ConversationKind, msg.TargetID)
		}
		if groupKey != "" {
			a.sessionIndex().setGroupKey(msg.ConversationKind, msg.TargetID, groupKey)
		}
		if row, err := a.memory.GetConversationTarget(ctx, msg.ConversationKind, msg.TargetID); err == nil && row != nil {
			if groupName == "" {
				groupName = row.Name
			}
		}
	}
	accountID := strings.TrimSpace(msg.AccountID)
	if accountID == "" && (msg.Platform == "" || msg.Platform == "qq") {
		if selfID := a.SelfID(); selfID > 0 {
			accountID = strconv.FormatInt(selfID, 10)
		}
	}
	rec, err := a.chatSessions.EnsureFromInbound(ctx, chatsession.InboundParams{
		Platform:         messagePlatform(msg),
		ConversationKind: msg.ConversationKind,
		TargetID:         msg.TargetID,
		GroupKey:         groupKey,
		UserID:           msg.UserID,
		ExternalTargetID: msg.ExternalTargetID,
		ExternalUserID:   msg.ExternalUserID,
		Nickname:         msg.Nickname,
		GroupCard:        msg.GroupCard,
		GroupName:        groupName,
		AccountID:        accountID,
		Scope:            msg.Scope,
	})
	if err != nil {
		zap.L().Warn("注册 chat_sessions 失败",
			zap.String("conversation_kind", msg.ConversationKind),
			zap.Int64("target_id", msg.TargetID),
			zap.String("platform", msg.Platform),
			zap.Error(err))
		return ""
	}
	if rec == nil {
		return ""
	}
	a.sessionIndex().setChatSessionID(msg.ConversationKind, msg.TargetID, rec.SessionID)
	return rec.SessionID
}

// messagePlatform 返回消息所属平台，未标注时按 QQ 处理
func messagePlatform(msg *onebot.ConversationMessage) string {
	if msg != nil && msg.Platform != "" {
		return msg.Platform
	}
	return platformio.PlatformQQ
}

// platformRoute 保存会话的多平台出站路由，TargetID 为外部平台原始目标 ID
type platformRoute struct {
	Platform  string
	AccountID string
	Scope     string
	TargetID  string
}

// noteConversationPlatform 记录会话最近一次入站平台与路由
func (a *Agent) noteConversationPlatform(msg *onebot.ConversationMessage) {
	if a == nil || msg == nil || (msg.TargetID <= 0 && !msg.HasExternalIdentity()) {
		return
	}
	platform := messagePlatform(msg)
	index := a.sessionIndex()
	// 平台索引与路由索引是两份独立缓存：conversationPlatform 只读前者
	index.setPlatform(msg.ConversationKind, msg.TargetID, platform)
	route, _ := index.route(msg.ConversationKind, msg.TargetID)
	route.Platform = platform
	if accountID := strings.TrimSpace(msg.AccountID); accountID != "" {
		route.AccountID = accountID
	}
	if scope := strings.TrimSpace(msg.Scope); scope != "" {
		route.Scope = scope
	}
	if externalTarget := strings.TrimSpace(msg.ExternalTargetID); externalTarget != "" {
		route.TargetID = externalTarget
	}
	if route.Platform == "qq" && route.AccountID == "" {
		if selfID := a.SelfID(); selfID > 0 {
			route.AccountID = strconv.FormatInt(selfID, 10)
		}
	}
	index.setRoute(msg.ConversationKind, msg.TargetID, route)
}

// applyConversationRouteIdentity 为出站后回写的自身消息补齐平台路由身份
func (a *Agent) applyConversationRouteIdentity(msg *onebot.ConversationMessage) {
	if a == nil || msg == nil || msg.TargetID <= 0 {
		return
	}
	route, ok := a.sessionIndex().route(msg.ConversationKind, msg.TargetID)
	if !ok {
		return
	}
	if msg.Platform == "" {
		msg.Platform = route.Platform
	}
	if msg.AccountID == "" {
		msg.AccountID = route.AccountID
	}
	if msg.Scope == "" {
		msg.Scope = route.Scope
	}
	if msg.ExternalTargetID == "" {
		msg.ExternalTargetID = route.TargetID
	}
}

// ensureExternalConversationTarget 为平台外部会话补建 conversation_targets 记录，
// 否则 messageAllowed/think 的会话许可检查会静默丢弃非 QQ 消息。
func (a *Agent) ensureExternalConversationTarget(ctx context.Context, msg *onebot.ConversationMessage) {
	if a == nil || a.memory == nil || msg == nil || !msg.HasExternalIdentity() || msg.TargetID <= 0 {
		return
	}
	// 自身出站回写消息不重建目标名，避免把机器人昵称写成外部会话名
	if msg.MessageID != 0 && msg.UserID == a.SelfID() {
		return
	}
	name := strings.TrimSpace(msg.GroupName)
	if name == "" && msg.ConversationKind == memory.ConversationKindGroup {
		name = strings.TrimSpace(msg.ExternalTargetID)
	}
	if name == "" {
		name = strings.TrimSpace(msg.Nickname)
	}
	if name == "" {
		name = strings.TrimSpace(msg.ExternalTargetID)
	}
	if err := a.memory.EnsureConversationTargetName(ctx, msg.ConversationKind, msg.TargetID, name); err != nil {
		zap.L().Warn("补建多平台会话目标失败",
			zap.String("platform", msg.Platform),
			zap.String("external_target_id", msg.ExternalTargetID),
			zap.Int64("target_id", msg.TargetID),
			zap.Error(err))
	}
}

// conversationPlatform 读取会话最近入站平台，未知时按 QQ 处理
func (a *Agent) conversationPlatform(kind string, targetID int64) string {
	if a != nil {
		if platform, ok := a.sessionIndex().platform(kind, targetID); ok {
			return platform
		}
	}
	return platformio.PlatformQQ
}

// resolveMaiBotSessionID 供表达/记忆等资源归属解析，解析不到时不伪造 MD5
func (a *Agent) resolveMaiBotSessionID(ctx context.Context, kind string, targetID int64) string {
	if a == nil || targetID <= 0 {
		return ""
	}
	if sessionID, ok := a.sessionIndex().chatSessionID(kind, targetID); ok {
		return sessionID
	}
	if a.chatSessions == nil {
		return ""
	}
	target := strconv.FormatInt(targetID, 10)
	if saved, ok := a.sessionIndex().route(kind, targetID); ok && saved.TargetID != "" {
		target = saved.TargetID
	}
	if kind == memory.ConversationKindGroup {
		if groupKey := a.conversationGroupKey(kind, targetID); groupKey != "" {
			target = groupKey
		}
	}
	rec, err := a.chatSessions.ResolveSession(ctx, a.conversationPlatform(kind, targetID), target, kind)
	if err != nil {
		return ""
	}
	return rec.SessionID
}

// conversationGroupKey 读取虚拟群原始 group_id
func (a *Agent) conversationGroupKey(kind string, targetID int64) string {
	if a == nil || kind != memory.ConversationKindGroup || targetID <= 0 {
		return ""
	}
	if groupKey, ok := a.sessionIndex().groupKey(kind, targetID); ok {
		return groupKey
	}
	return ""
}

// markInternalConversation 记录内部聊天目标，供出站路由识别虚拟平台
func (a *Agent) markInternalConversation(kind string, targetID int64) {
	if a == nil || targetID <= 0 {
		return
	}
	a.sessionIndex().markInternal(kind, targetID)
}

// isInternalTarget 判断目标是否由内部聊天驱动
func (a *Agent) isInternalTarget(kind string, targetID int64) bool {
	if a == nil || targetID <= 0 {
		return false
	}
	return a.sessionIndex().isInternal(kind, targetID)
}
