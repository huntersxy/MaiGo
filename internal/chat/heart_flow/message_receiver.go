package heartflow

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"maibot-go/internal/maisaka"
)

// UserInfo 入站消息发送者，对应 Python UserInfo
type UserInfo struct {
	UserID   string
	Nickname string
	Cardname string
}

// GroupInfo 入站消息群信息，对应 Python GroupInfo
type GroupInfo struct {
	GroupID   string
	GroupName string
}

// MessageInfo 入站消息元信息
type MessageInfo struct {
	User             *UserInfo
	Group            *GroupInfo
	AdditionalConfig map[string]any
}

// MetadataHostSource 宿主原始消息在 ContextMessage 元数据中的键
// 生产分发桥接用它把心流消息还原为宿主消息，避免丢失平台段
const MetadataHostSource = "host_source"

// SessionMessage 是 HeartFCMessageReceiver 需要的最小消息结构
// 对应 Python SessionMessage 的 message_id/session_id/platform/message_info/
// processed_plain_text/received_at
type SessionMessage struct {
	MessageID          string
	SessionID          string
	Platform           string
	MessageInfo        MessageInfo
	ProcessedPlainText string
	Timestamp          time.Time
	ReceivedAt         *time.Time
	// IsNotify 平台通知（戳一戳等）以 user 来源进入规划器，但不是用户发言
	IsNotify bool
	// IsAt / IsMentioned 上游标记；最终语义由 is_mentioned_bot_in_message 补齐
	IsAt        bool
	IsMentioned bool
	// Source 宿主原始消息，仅在宿主分发桥接时使用；普通构造可留空
	Source any
	// ShapeKind/ShapeLength 对齐 get_message_shape；nil 表示未计算
	ShapeKind   *int
	ShapeLength *int
}

// ToContextMessage 将入站消息转换为 Maisaka 运行时消息
// Python 在 register_message 内完成同样的转换，这里只保留学习与规划需要的字段
func (m *SessionMessage) ToContextMessage() maisaka.ContextMessage {
	if m == nil {
		return maisaka.ContextMessage{}
	}
	receivedAt := m.ReceivedAt
	if receivedAt == nil && !m.Timestamp.IsZero() {
		value := m.Timestamp
		receivedAt = &value
	}
	userID := ""
	senderName := ""
	cardname := ""
	if m.MessageInfo.User != nil {
		userID = m.MessageInfo.User.UserID
		senderName = m.MessageInfo.User.Nickname
		cardname = m.MessageInfo.User.Cardname
	}
	if strings.TrimSpace(cardname) != "" {
		senderName = cardname
	}
	metadata := map[string]any{
		"user_id":      userID,
		"nickname":     nicknameOf(m.MessageInfo.User),
		"sender_name":  senderName,
		"is_notify":    m.IsNotify,
		"is_at":        m.IsAt,
		"is_mentioned": m.IsMentioned,
	}
	if len(m.MessageInfo.AdditionalConfig) > 0 {
		metadata["additional_config"] = m.MessageInfo.AdditionalConfig
	}
	if group := m.MessageInfo.Group; group != nil {
		metadata["is_group"] = true
		metadata["group_id"] = group.GroupID
		metadata["group_name"] = group.GroupName
	}
	if m.Source != nil {
		metadata[MetadataHostSource] = m.Source
	}
	if m.ShapeKind != nil {
		metadata["shape_kind"] = *m.ShapeKind
	}
	if m.ShapeLength != nil {
		metadata["shape_length"] = *m.ShapeLength
	}
	return maisaka.ContextMessage{
		Role:       maisaka.RoleUser,
		Content:    m.ProcessedPlainText,
		Source:     "user",
		Timestamp:  m.Timestamp,
		ReceivedAt: receivedAt,
		MessageID:  m.MessageID,
		SessionID:  m.SessionID,
		Platform:   m.Platform,
		UserID:     userID,
		Metadata:   metadata,
	}
}

func nicknameOf(user *UserInfo) string {
	if user == nil {
		return ""
	}
	return user.Nickname
}

// ChatProvider 获取或创建会话运行时
type ChatProvider interface {
	GetOrCreate(ctx context.Context, sessionID string) (Runtime, error)
}

// MessageStore 对应 MessageUtils.store_message_to_db_async
type MessageStore interface {
	StoreMessage(ctx context.Context, message *SessionMessage) error
}

// MemoryAutomation 对应 memory_automation_service.on_incoming_message
type MemoryAutomation interface {
	OnIncomingMessage(ctx context.Context, message *SessionMessage) error
}

// PersonRegistrar 对应 Person.register_person
type PersonRegistrar interface {
	RegisterPerson(ctx context.Context, platform, userID, nickname, groupID, groupNickname string) error
}

// ReceiverOptions 入站消息处理器依赖
type ReceiverOptions struct {
	Chats  ChatProvider
	Store  MessageStore
	Memory MemoryAutomation
	People PersonRegistrar
	Now    func() time.Time
}

// Receiver 心流消息处理器，负责校验、落库、登记运行时与关系
type Receiver struct {
	opts ReceiverOptions
}

// NewReceiver 创建心流消息处理器
func NewReceiver(opts ReceiverOptions) *Receiver {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Receiver{opts: opts}
}

// ProcessMessage 对齐 Python process_message：所有异常只记录，不向调用方传播
func (r *Receiver) ProcessMessage(ctx context.Context, message *SessionMessage) {
	if err := r.ProcessMessageStrict(ctx, message); err != nil {
		zap.L().Error(fmt.Sprintf("消息处理失败: %v", err), zap.Error(err))
	}
}

// ProcessMessageStrict 执行同一条链路但把错误返回给调用方，便于测试与显式降级
func (r *Receiver) ProcessMessageStrict(ctx context.Context, message *SessionMessage) error {
	user := userInfoOf(message)
	if user == nil || message == nil || message.Platform == "" {
		return fmt.Errorf("message userinfo or platform is missing")
	}
	if user.UserID == "" || user.Nickname == "" {
		return fmt.Errorf("message userinfo id or nickname is missing")
	}

	var chat Runtime
	if r.opts.Chats != nil {
		runtime, err := r.opts.Chats.GetOrCreate(ctx, message.SessionID)
		if err != nil {
			zap.L().Error(fmt.Sprintf("出现错误: %v", err), zap.Error(err))
		} else {
			chat = runtime
		}
	}

	if r.opts.Store != nil {
		if err := r.opts.Store.StoreMessage(ctx, message); err != nil {
			return fmt.Errorf("存储消息失败: %w", err)
		}
	}
	if chat != nil {
		if err := chat.RegisterMessage(ctx, message.ToContextMessage()); err != nil {
			return fmt.Errorf("登记聊天消息失败: %w", err)
		}
	}
	if r.opts.Memory != nil {
		if err := r.opts.Memory.OnIncomingMessage(ctx, message); err != nil {
			return fmt.Errorf("触发记忆自动化失败: %w", err)
		}
	}

	group := groupInfoOf(message)
	displayName := "私聊"
	if group != nil {
		displayName = group.GroupName
	}
	text := message.ProcessedPlainText
	preview := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(preview) > 200 {
		preview = truncateRunes(preview, 200) + fmt.Sprintf("…（共%d字，已截断）", utf8.RuneCountInString(text))
	}
	zap.L().Info("[" + displayName + "] " + user.Nickname + ": " + preview)
	if preview != text {
		zap.L().Debug("["+displayName+"] "+user.Nickname+" 完整消息: "+text, zap.String("session_id", message.SessionID))
	}

	groupID := ""
	groupNickname := ""
	if group != nil {
		groupID = group.GroupID
		groupNickname = user.Cardname
	}
	if r.opts.People != nil {
		if err := r.opts.People.RegisterPerson(ctx, message.Platform, user.UserID, user.Nickname, groupID, groupNickname); err != nil {
			return fmt.Errorf("登记人物信息失败: %w", err)
		}
	}
	return nil
}

func userInfoOf(message *SessionMessage) *UserInfo {
	if message == nil {
		return nil
	}
	return message.MessageInfo.User
}

func groupInfoOf(message *SessionMessage) *GroupInfo {
	if message == nil {
		return nil
	}
	return message.MessageInfo.Group
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
