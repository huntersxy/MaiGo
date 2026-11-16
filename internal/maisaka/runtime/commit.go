package runtime

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	messagereceive "MaiGo/internal/chat/message_receive"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/services"

	"MaiGo/internal/platform_io"
	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	commitQueueSize    = 256
	recallPendingTTL   = 5 * time.Minute
	persistenceTimeout = 30 * time.Second
)

type recallCommit struct {
	kind      string
	targetID  int64
	messageID int64
}

// persistenceContext 已解析事件和成功发言独立于推理取消，每次数据库处理都有期限
func (a *Agent) persistenceContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(a.ctx), persistenceTimeout)
}

// SkipConversationEvent 消费未处理事件的到达序号，避免提交重排器等待永久缺口
func (a *Agent) SkipConversationEvent(kind string, targetID int64, arrivalSeq uint64) {
	item := commitItem{skip: true}
	if kind == memory.ConversationKindPrivate {
		a.privateCommits.enqueue(targetID, arrivalSeq, item)
	} else {
		a.groupCommits.enqueue(targetID, arrivalSeq, item)
	}
}

func (a *Agent) messageAllowed(ctx context.Context, msg *onebot.ConversationMessage) bool {
	if msg.MessageID != 0 && msg.UserID == a.SelfID() {
		return true
	}
	allowed, err := a.memory.ConversationAllowed(ctx, msg.ConversationKind, msg.TargetID)
	if err != nil {
		zap.L().Warn("事件许可检查失败", zap.String("conversation_kind", msg.ConversationKind), zap.Int64("target_id", msg.TargetID), zap.Error(err))
	}
	return err == nil && allowed
}

// persistConversationMessage 在队列消费时锁住会话许可，已成功发送的自身消息必须保留
func (a *Agent) persistConversationMessage(ctx context.Context, msg *onebot.ConversationMessage, mediaRefs []media.Ref, forwards []messagereceive.ForwardDraft, platform, sessionID string) (*memory.MessageLog, bool, error) {
	if platform == "" {
		platform = platformio.PlatformQQ
	}
	var log *memory.MessageLog
	created := false
	err := a.memory.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if msg.MessageID == 0 || msg.UserID != a.SelfID() {
			if err := memory.LockConversationAllowed(tx, msg.ConversationKind, msg.TargetID); err != nil {
				return err
			}
		}
		if msg.MessageID == 0 {
			created = true
			return nil
		}
		groupID, groupName := messageGroupFields(msg)
		log = &memory.MessageLog{
			ConversationKind: msg.ConversationKind, MessageID: msg.MessageID,
			TargetID: msg.TargetID, UserID: msg.UserID, UserNickname: msg.Nickname,
			UserCardname: msg.GroupCard, GroupID: groupID, GroupName: groupName,
			ProcessedPlainText: msg.FinalContent,
			IsMentioned:        msg.IsMentioned, IsAt: messageIsAt(msg),
			IsEmoji: messageIsEmoji(msg), IsPicture: messageIsPicture(msg),
			IsCommand: messageIsCommand(msg), IsNotify: msg.IsNotify,
			Timestamp: msg.Time, MediaRefs: mediaRefs,
			Platform: platform, SessionID: sessionID,
			ExternalMessageID: msg.ExternalMessageID,
			ExternalTargetID:  msg.ExternalTargetID,
			ExternalUserID:    msg.ExternalUserID,
			AccountID:         msg.AccountID,
			Scope:             msg.Scope,
			RawContent:        messagereceive.BuildRawContent(msg, mediaRefs, forwards),
		}
		if len(msg.AdditionalConfig) > 0 {
			log.AdditionalConfig = memory.MessageAdditionalConfig(msg.AdditionalConfig)
		}
		if !msg.ReceivedAt.IsZero() {
			receivedAt := msg.ReceivedAt
			log.ReceivedAt = &receivedAt
		}
		// 对齐 MaiBot fill_reply_frequency_if_available：入库前记录当前会话生效回复频率，
		// 自身消息不补造
		if msg.UserID != a.SelfID() {
			frequency := a.privateReplyFrequency(msg.TargetID)
			if msg.ConversationKind == memory.ConversationKindGroup {
				frequency = a.gateFrequency(msg.TargetID)
			}
			log.ReplyFrequency = &frequency
		}
		if msg.Reply != nil && msg.Reply.MessageID != 0 {
			replyTo := msg.Reply.MessageID
			log.ReplyTo = &replyTo
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(log)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected > 0
		if !created {
			if err := tx.Where("conversation_kind=? AND target_id=? AND message_id=?", msg.ConversationKind, msg.TargetID, msg.MessageID).First(log).Error; err != nil {
				return err
			}
			return memory.AttachMessageLogSessionIDTx(tx, log.ID, sessionID)
		}
		return saveMessageForwards(tx, log, forwards)
	})
	return log, created, err
}

// saveMessageForwards 把合并转发内容跟着消息一起落库，父级先于嵌套写入
func saveMessageForwards(tx *gorm.DB, log *memory.MessageLog, drafts []messagereceive.ForwardDraft) error {
	if log == nil || len(drafts) == 0 {
		return nil
	}
	rows := make([]memory.MessageForward, 0, len(drafts))
	for _, draft := range drafts {
		rows = append(rows, memory.MessageForward{
			Handle:       draft.Handle,
			MessageLogID: log.ID,
			ParentHandle: draft.Parent,
			Nodes:        draft.Nodes,
		})
	}
	return memory.SaveMessageForwards(tx, rows)
}

// commitConversationMessage 群私消息、观察与重复事件共用持久化、撤回补偿及发布边界
func (a *Agent) commitConversationMessage(item commitItem) {
	msg := item.msg
	if msg == nil {
		return
	}
	ctx, cancel := a.persistenceContext()
	defer cancel()
	sessionID := a.ensureChatSession(ctx, msg)
	// 迁移暂停期间等待在途消息，避免迁移快照后继续写入旧聊天流
	if sessionID != "" && services.ChatMigrationGateInstance != nil {
		if err := services.ChatMigrationGateInstance.Activity(ctx, sessionID, func() error {
			a.commitConversationMessageLocked(ctx, item, sessionID)
			return nil
		}); err != nil {
			zap.L().Warn("等待聊天迁移完成失败，丢弃入站消息",
				zap.String("session_id", sessionID), zap.Error(err))
		}
		return
	}
	a.commitConversationMessageLocked(ctx, item, sessionID)
}

func (a *Agent) commitConversationMessageLocked(ctx context.Context, item commitItem, sessionID string) {
	msg := item.msg
	replyErr := a.refreshReplyRecall(ctx, msg)
	if replyErr != nil {
		zap.L().Warn("核对引用撤回状态失败，省略引用原文", zap.String("conversation_kind", msg.ConversationKind), zap.Int64("target_id", msg.TargetID), zap.Int64("message_id", msg.MessageID), zap.Error(replyErr))
	}
	ingestor := a.fileIngestorPort()
	if ingestor != nil {
		if err := ingestor(ctx, msg, sessionID); err != nil {
			zap.L().Warn("登记入站文件失败，保留消息原文",
				zap.String("conversation_kind", msg.ConversationKind),
				zap.Int64("target_id", msg.TargetID),
				zap.Error(err))
		}
	}
	log, created, err := a.persistConversationMessage(ctx, msg, item.media, item.forwards, messagePlatform(msg), sessionID)
	if errors.Is(err, memory.ErrConversationUnavailable) {
		return
	}
	if err != nil {
		zap.L().Error("提交消息失败", zap.String("conversation_kind", msg.ConversationKind), zap.Int64("target_id", msg.TargetID), zap.Int64("message_id", msg.MessageID), zap.Error(err))
		return
	}
	if log != nil && log.ProcessedPlainText == memory.RecalledMessageProcessedPlainText {
		msg = recalledConversationMessage(msg)
		a.markSessionRecalled(msg.ConversationKind, msg.TargetID, msg.MessageID)
	}
	if msg.MessageID != 0 {
		msg = a.applyPendingRecall(ctx, msg)
	}
	if !created {
		return
	}
	// 引用缓存只由按序提交写入，避免并行解析在撤回后写回旧值
	if replyErr == nil && msg.Reply != nil && msg.Reply.MessageID != 0 {
		a.replyCache.Set(replyCacheKey(msg.ConversationKind, msg.TargetID, msg.Reply.MessageID), *msg.Reply, ttlcache.DefaultTTL)
	}
	a.appendSessionMessage(ctx, msg, item.media, log)
	if log != nil && sessionID != "" && log.SessionID == "" {
		if err := a.memory.AttachMessageLogSessionID(ctx, log.ID, sessionID); err != nil {
			zap.L().Warn("补写 mai_messages.session_id 失败", zap.Uint("log_id", log.ID), zap.String("session_id", sessionID), zap.Error(err))
		} else {
			log.SessionID = sessionID
		}
	}
	if created && msg.MessageID != 0 && msg.UserID != a.SelfID() {
		a.observeExpressionInbound(ctx, msg)
	}
	if created && msg.MessageID != 0 && msg.UserID == a.SelfID() {
		if !externalPlatformMessage(msg) {
			a.notifySentMessage(ctx, msg)
		}
		return
	}
	if msg.MessageID == 0 || msg.UserID == a.SelfID() || a.stopping.Load() || a.ctx.Err() != nil {
		return
	}
	a.afterMessagePersisted(ctx, msg)
	a.observeInboundDebounce(msg)
	if msg.ConversationKind == memory.ConversationKindPrivate {
		a.schedulePrivateThink(msg.TargetID, msg.ReceivedAt)
	} else {
		a.scheduleGroupThink(msg.TargetID, msg.ReceivedAt)
	}
}

// commitItem 提交队列项：消息、戳一戳、撤回统一按会话内到达序号重排提交；
// skip 用于消费不会产生实际处理的序号（解析失败、无效事件、未启用会话），避免重排器死等
type commitItem struct {
	skip     bool
	recall   *recallCommit
	msg      *onebot.ConversationMessage
	media    []media.Ref
	forwards []messagereceive.ForwardDraft
}

// commitOne 群聊与私聊提交队列共用的分发逻辑，会话隔离由各自队列实例保证
func (a *Agent) commitOne(item commitItem) {
	switch {
	case item.skip:
	case item.recall != nil:
		a.commitRecall(item.recall)
	default:
		a.commitConversationMessage(item)
	}
}

// messageGroupFields 取消息所属群信息；WebUI 虚拟群优先使用原始 group_id
func messageGroupFields(msg *onebot.ConversationMessage) (string, string) {
	if msg == nil || msg.ConversationKind != memory.ConversationKindGroup {
		return "", ""
	}
	groupID := strings.TrimSpace(msg.GroupKey)
	if groupID == "" {
		groupID = strconv.FormatInt(msg.TargetID, 10)
	}
	return groupID, strings.TrimSpace(msg.GroupName)
}

func messageIsAt(msg *onebot.ConversationMessage) bool {
	if msg == nil {
		return false
	}
	if value, _ := msg.AdditionalConfig["at_bot"].(bool); value {
		return true
	}
	return msg.ConversationKind == memory.ConversationKindGroup && msg.IsMentioned
}

func messageIsEmoji(msg *onebot.ConversationMessage) bool {
	if msg == nil {
		return false
	}
	if len(msg.Faces) > 0 {
		return true
	}
	for _, image := range msg.Images {
		if image.SubType == 1 {
			return true
		}
	}
	return false
}

func messageIsPicture(msg *onebot.ConversationMessage) bool {
	if msg == nil {
		return false
	}
	for _, image := range msg.Images {
		if image.SubType != 1 {
			return true
		}
	}
	return false
}

func messageIsCommand(msg *onebot.ConversationMessage) bool {
	if msg == nil {
		return false
	}
	value, _ := msg.AdditionalConfig["is_command"].(bool)
	return value
}
