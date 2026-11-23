package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	messagereceive "MaiGo/internal/chat/message_receive"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	builtin_tool "MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/person_info"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/services/statistics"

	"go.uber.org/zap"
)

func (a *Agent) HandleMessage(parent context.Context, msg *onebot.ConversationMessage) {
	if msg == nil {
		return
	}
	if msg.ConversationKind == "" {
		msg.ConversationKind = memory.ConversationKindGroup
	}
	ctx, cancel := persistenceContextFrom(parent)
	defer cancel()
	ctx, finish, err := a.reserveCommit(ctx, msg.ConversationKind, msg.TargetID, &msg.ArrivalSeq)
	if err != nil {
		zap.L().Warn("入站提交未接收消息", zap.Error(err))
		return
	}
	defer finish(nil)
	a.noteConversationPlatform(msg)
	builtin_tool.NotifyDesktopPetUserMessage(msg.Platform, msg.UserID, msg.GroupKey != "")
	a.applyConversationRouteIdentity(msg)
	a.rememberExternalIdentities(msg)
	a.ensureExternalConversationTarget(ctx, msg)
	var mediaRefs []media.Ref
	var forwards []messagereceive.ForwardDraft
	if msg.ParseFailed || msg.UserID <= 0 || !a.messageAllowed(ctx, msg) {
		return
	}
	if msg.UserID != a.SelfID() {
		stats.RecordInbound(chatsession.ConversationKey(msg.ConversationKind, msg.TargetID))
	}
	if msg.ReceivedAt.IsZero() {
		msg.ReceivedAt = time.Now()
	}
	if msg.MessageID == 0 {
		if !a.onInteractionMessage(msg) {
			return
		}
	} else {
		mediaRefs, forwards = a.prepareMessageContent(ctx, msg)
		selfID := a.SelfID()
		msg.IsMentioned = msg.IsMentioned || (msg.Reply != nil && msg.Reply.SenderID == selfID)
	}
	var aborted bool
	msg, aborted = a.emitReceiveHook(ctx, "chat.receive.before_process", msg)
	if aborted {
		return
	}
	msg, aborted = a.emitReceiveHook(ctx, "chat.receive.after_process", msg)
	if aborted {
		return
	}
	if a.handleCommand(ctx, msg, mediaRefs, forwards) {
		return
	}
	finish(func() {
		a.commitConversationMessage(ctx, msg, mediaRefs, forwards)
	})
}

// onRecall 撤回事件入口：带到达序号进入提交策略，与会话内消息保持顺序
func (a *Agent) onRecall(parent context.Context, kind string, targetID, messageID int64, arrivalSeq uint64) {
	if kind == "" {
		kind = memory.ConversationKindGroup
	}
	if targetID <= 0 {
		return
	}
	ctx, cancel := persistenceContextFrom(parent)
	defer cancel()
	ctx, finish, err := a.reserveCommit(ctx, kind, targetID, &arrivalSeq)
	if err != nil {
		zap.L().Warn("入站提交未接收撤回事件", zap.Error(err))
		return
	}
	defer finish(nil)
	if messageID == 0 {
		return
	}
	// 撤回修正已有原文，不受会话拉黑、退群或数据库暂时不可用影响
	recall := &recallCommit{kind: kind, targetID: targetID, messageID: messageID}
	finish(func() { a.commitRecall(ctx, recall) })
}

// onInteractionMessage 只构造戳一戳的展示内容，返回是否有效；缓冲和思考调度由提交策略统一处理
func (a *Agent) onInteractionMessage(msg *onebot.ConversationMessage) bool {
	if msg.UserID <= 0 || len(msg.AtList) == 0 || msg.AtList[0] <= 0 {
		return false
	}
	targetID := msg.AtList[0]
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	actorName := strings.TrimSpace(msg.Nickname)
	if actorName == "" {
		if msg.UserID == targetID {
			actorName = a.resolveMentionDisplayName(ctx, msg, targetID)
		} else {
			actorName = a.resolveMentionDisplayName(ctx, msg, msg.UserID)
		}
	}
	text := pokeNoticeText(actorName, msg.UserID, targetID)
	msg.Content = text
	msg.FinalContent = text
	msg.IsMentioned = false
	msg.IsNotify = true
	return true
}

func botMentionDisplayName(botName string) string {
	if botName = strings.TrimSpace(botName); botName != "" {
		return botName + "(你)"
	}
	return "机器人(你)"
}

// afterMessagePersisted 只由新建的非自身消息调用，重复事件不增加画像计数
func (a *Agent) afterMessagePersisted(ctx context.Context, msg *onebot.ConversationMessage) {
	// 思考调度前先更新成员昵称和会话基础信息
	a.updateMember(ctx, msg)
	a.ingestMemorixInbound(ctx, msg)
}

func (a *Agent) resolveReplyInfo(ctx context.Context, msg *onebot.ConversationMessage) error {
	if msg == nil || msg.Reply == nil {
		return nil
	}
	if msg.Reply.Content != "" && msg.Reply.SenderID != 0 {
		return nil
	}
	if msg.Reply.MessageID == 0 && strings.TrimSpace(msg.Reply.ExternalMessageID) == "" {
		return nil
	}
	key := replyCacheKey(msg.ConversationKind, msg.TargetID, msg.Reply.MessageID)
	if cached := a.replyCache.Get(key); cached != nil {
		clone := cached.Value()
		msg.Reply = &clone
		return nil
	}

	// 优先按平台外部消息编号查询：非 QQ 平台的内部 message_id 是合成数字键，
	// 只有 external_message_id 与适配器给的原值一致
	if externalID := strings.TrimSpace(msg.Reply.ExternalMessageID); externalID != "" {
		if log, err := a.memory.WithContext(ctx).GetMessageLogByScopeExternalID(msg.ConversationKind, msg.TargetID, externalID); err == nil {
			if reply := replyInfoFromMessageLog(log); reply != nil {
				msg.Reply = reply
				return nil
			}
		}
	}
	if msg.Reply.MessageID != 0 {
		log, err := a.memory.WithContext(ctx).GetMessageLogByScope(msg.ConversationKind, msg.TargetID, msg.Reply.MessageID)
		if err == nil {
			if reply := replyInfoFromMessageLog(log); reply != nil {
				msg.Reply = reply
				return nil
			}
		}
	}
	// 本地查不到时保留原始引用 ID，由 Platform IO 引用段或前端按 ID 展示
	return nil
}

// externalPlatformMessage 判断消息是否来自非 QQ 平台
func externalPlatformMessage(msg *onebot.ConversationMessage) bool {
	if msg == nil {
		return false
	}
	if msg.HasExternalIdentity() {
		return true
	}
	platform := strings.TrimSpace(msg.Platform)
	return platform != "" && platform != "qq"
}

func replyCacheKey(kind string, targetID, messageID int64) string {
	if kind == "" {
		kind = memory.ConversationKindGroup
	}
	return recallScopeKey(kind, targetID) + ":" + strconv.FormatInt(messageID, 10)
}

func (a *Agent) updateMember(ctx context.Context, msg *onebot.ConversationMessage) {
	if err := personinfo.DefaultRegistry().RegisterFromMessage(ctx, msg); err != nil {
		zap.L().Error("注册人物画像失败", zap.Error(err))
	}
}

func replyInfoFromMessageLog(log *memory.MessageLog) *onebot.ReplyInfo {
	if log == nil {
		return nil
	}

	if log.ProcessedPlainText == memory.RecalledMessageProcessedPlainText {
		return &onebot.ReplyInfo{
			MessageID:         log.MessageID,
			ExternalMessageID: log.ExternalMessageID,
			Content:           memory.RecalledMessageProcessedPlainText,
			SenderID:          log.UserID,
			ExternalSenderID:  log.ExternalUserID,
			Nickname:          log.UserNickname,
		}
	}
	content := strings.TrimSpace(log.ProcessedPlainText)
	if content == "" {
		content = strings.TrimSpace(log.ProcessedPlainText)
	}

	return &onebot.ReplyInfo{
		MessageID:         log.MessageID,
		ExternalMessageID: log.ExternalMessageID,
		Content:           content,
		SenderID:          log.UserID,
		ExternalSenderID:  log.ExternalUserID,
		Nickname:          log.UserNickname,
		GroupCard:         log.UserCardname,
	}
}

// pokeNoticeText 构造戳一戳通知正文，对齐 OneBot 通知事件的展示文案
func pokeNoticeText(actorName string, actorID, targetID int64) string {
	actor := strings.TrimSpace(actorName)
	if actor == "" {
		if actorID > 0 {
			actor = strconv.FormatInt(actorID, 10)
		} else {
			actor = "系统"
		}
	}
	text := actor + " 发起了戳一戳"
	if targetID > 0 {
		text = fmt.Sprintf("%s -> %d", text, targetID)
	}
	return text
}
