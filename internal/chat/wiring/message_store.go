package chatwiring

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"gorm.io/gorm"

	"maibot-go/internal/chat/heart_flow"
	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/data_models/messagecomponent"
	"maibot-go/internal/common/database"
	"maibot-go/internal/platform_io/drivers/onebot"
)

// MessageLogWriter 消息行写入端口，便于没有真实数据库时做装配测试
type MessageLogWriter interface {
	CreateMessageLog(ctx context.Context, log *memory.MessageLog) error
}

// MessageLogWriterFunc 函数式写入器
type MessageLogWriterFunc func(ctx context.Context, log *memory.MessageLog) error

// CreateMessageLog 实现 MessageLogWriter
func (f MessageLogWriterFunc) CreateMessageLog(ctx context.Context, log *memory.MessageLog) error {
	return f(ctx, log)
}

// MessageStore 把 SessionMessage 落成 mai_messages 行，并在入库前补全回复频率
type MessageStore struct {
	DB        *gorm.DB
	Writer    MessageLogWriter
	Frequency *FrequencyFiller
	Now       func() time.Time
}

// StoreMessage 对齐 Python store_message_to_db：先补频率，再序列化入库
func (s *MessageStore) StoreMessage(ctx context.Context, message *messagereceive.SessionMessage) error {
	if s == nil {
		return errors.New("消息存储未接线")
	}
	if message == nil {
		return errors.New("消息不能为空")
	}
	if s.Frequency != nil {
		if err := s.Frequency.FillReplyFrequency(ctx, message); err != nil {
			return fmt.Errorf("补全回复频率失败: %w", err)
		}
	}
	log, err := BuildMessageLog(message, s.now())
	if err != nil {
		return err
	}
	return s.write(ctx, log)
}

// StoreHeartflowMessage 写心流接收器的消息；优先复用宿主原始消息
func (s *MessageStore) StoreHeartflowMessage(ctx context.Context, message *heartflow.SessionMessage) error {
	if message == nil {
		return errors.New("消息不能为空")
	}
	if source, ok := message.Source.(*messagereceive.SessionMessage); ok && source != nil {
		return s.StoreMessage(ctx, source)
	}
	host := heartflowToSessionMessage(message)
	if s.Frequency != nil && host.ReplyFrequency == nil {
		if frequency, ok := s.Frequency.SessionFrequency(host.SessionID); ok {
			host.ReplyFrequency = &frequency
		}
	}
	return s.StoreMessage(ctx, host)
}

func (s *MessageStore) write(ctx context.Context, log *memory.MessageLog) error {
	if s.Writer != nil {
		return s.Writer.CreateMessageLog(ctx, log)
	}
	if s.DB == nil {
		return errors.New("消息数据库未接线")
	}
	if err := s.DB.WithContext(ctx).Create(log).Error; err != nil {
		return fmt.Errorf("写入消息记录失败: %w", err)
	}
	return nil
}

func (s *MessageStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// BuildMessageLog 把宿主的 SessionMessage 投影成 mai_messages 行
func BuildMessageLog(message *messagereceive.SessionMessage, now time.Time) (*memory.MessageLog, error) {
	if message == nil {
		return nil, errors.New("消息不能为空")
	}
	if now.IsZero() {
		now = time.Now()
	}
	messageTime := message.Timestamp
	if messageTime.IsZero() {
		messageTime = now
	}
	text := message.PlainText()
	if strings.TrimSpace(text) == "" {
		text = rawText(message.RawMessage)
	}
	nickname := message.Nickname()
	platform := strings.TrimSpace(message.Platform)
	if platform == "" {
		platform = "unknown"
	}
	targetID := message.TargetID()
	userID := message.UserID()
	sessionID := strings.TrimSpace(message.SessionID)
	messageID := strings.TrimSpace(message.MessageID)
	accountID := pointerText(message.AccountID)
	scope := pointerText(message.Scope)

	groupID, groupName := "", ""
	if message.IsGroup() {
		groupID = targetID
		groupName = message.GroupName()
	}
	log := &memory.MessageLog{
		MessageID:          positiveInt64OrSynthetic(messageID, "message", platform, accountID, scope, sessionID, messageID),
		ConversationKind:   message.ChatType(),
		TargetID:           positiveInt64OrSynthetic(targetID, "target", platform, accountID, scope, targetID),
		UserID:             positiveInt64OrSynthetic(userID, "user", platform, accountID, scope, userID),
		UserNickname:       nickname,
		UserCardname:       message.GroupCard(),
		GroupID:            groupID,
		GroupName:          groupName,
		ProcessedPlainText: text,
		IsMentioned:        message.IsMentioned,
		IsAt:               message.IsAt,
		IsEmoji:            message.IsEmoji,
		IsPicture:          message.IsPicture,
		IsCommand:          message.IsCommand,
		IsNotify:           message.IsNotify,
		Timestamp:          messageTime,
		ReplyFrequency:     cloneFloat(message.ReplyFrequency),
		Platform:           platform,
		SessionID:          sessionID,
		ExternalMessageID:  messageID,
		ExternalTargetID:   targetID,
		ExternalUserID:     userID,
		AccountID:          accountID,
		Scope:              scope,
	}
	if !message.ReceivedAt.IsZero() {
		value := message.ReceivedAt
		log.ReceivedAt = &value
	}
	if replyTo := pointerText(message.ReplyTo); replyTo != "" {
		if value, err := strconv.ParseInt(replyTo, 10, 64); err == nil && value > 0 {
			log.ReplyTo = &value
		}
	}
	if len(message.MessageInfo.AdditionalConfig) > 0 {
		log.AdditionalConfig = memory.MessageAdditionalConfig(message.MessageInfo.AdditionalConfig)
	}
	if message.RawMessage != nil {
		raw, err := msgpack.Marshal(message.RawMessage.ToDict())
		if err != nil {
			return nil, fmt.Errorf("序列化原始消息失败: %w", err)
		}
		log.RawContent = raw
	}
	return log, nil
}

func heartflowToSessionMessage(message *heartflow.SessionMessage) *messagereceive.SessionMessage {
	host := &messagereceive.SessionMessage{
		MessageID: message.MessageID,
		Timestamp: message.Timestamp,
		Platform:  message.Platform,
		SessionID: message.SessionID,
		IsNotify:  message.IsNotify,
	}
	if message.ReceivedAt != nil {
		host.ReceivedAt = *message.ReceivedAt
	}
	if message.MessageInfo.User != nil {
		host.MessageInfo.UserInfo = messagereceive.UserInfo{
			UserID:       message.MessageInfo.User.UserID,
			UserNickname: message.MessageInfo.User.Nickname,
		}
		if strings.TrimSpace(message.MessageInfo.User.Cardname) != "" {
			card := message.MessageInfo.User.Cardname
			host.MessageInfo.UserInfo.UserCardname = &card
		}
	}
	if message.MessageInfo.Group != nil {
		host.MessageInfo.GroupInfo = &messagereceive.GroupInfo{
			GroupID:   message.MessageInfo.Group.GroupID,
			GroupName: message.MessageInfo.Group.GroupName,
		}
	}
	host.MessageInfo.AdditionalConfig = message.MessageInfo.AdditionalConfig
	host.SetPlainText(message.ProcessedPlainText)
	return host
}

func positiveInt64OrSynthetic(raw string, parts ...string) int64 {
	value := strings.TrimSpace(raw)
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
		return parsed
	}
	return onebot.SyntheticIdentityID(parts...)
}

func rawText(sequence *messagecomponent.MessageSequence) string {
	if sequence == nil {
		return ""
	}
	var builder strings.Builder
	for _, component := range sequence.Components {
		if text, ok := component.(messagecomponent.TextComponent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

func pointerText(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
