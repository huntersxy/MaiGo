package chatwiring

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"maibot-go/internal/chat/debounce"
	"maibot-go/internal/chat/heart_flow"
	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/person_info"
)

// HeartflowChatProvider 用 chat_sessions 提供心流会话信息
type HeartflowChatProvider struct {
	Manager *messagereceive.ChatManager
}

// SessionByID 实现 heartflow.SessionProvider
func (p *HeartflowChatProvider) SessionByID(ctx context.Context, sessionID string) (heartflow.ChatSession, bool, error) {
	if p == nil || p.Manager == nil {
		return heartflow.ChatSession{}, false, errors.New("聊天流管理器未接线")
	}
	session, ok := p.Manager.GetExistingSessionBySessionID(ctx, sessionID)
	if !ok || session == nil {
		return heartflow.ChatSession{}, false, nil
	}
	record := session.Record
	return heartflow.ChatSession{
		SessionID: record.SessionID,
		Platform:  record.Platform,
		AccountID: pointerText(record.AccountID),
		Scope:     pointerText(record.Scope),
		UserID:    pointerText(record.UserID),
		GroupID:   pointerText(record.GroupID),
		IsGroup:   record.IsGroup(),
	}, true, nil
}

// HeartflowStore 把心流接收器消息复用宿主消息的完整落库
type HeartflowStore struct {
	Messages *MessageStore
}

// StoreMessage 实现 heartflow.MessageStore
func (s *HeartflowStore) StoreMessage(ctx context.Context, message *heartflow.SessionMessage) error {
	if s == nil || s.Messages == nil {
		return errors.New("消息存储未接线")
	}
	return s.Messages.StoreHeartflowMessage(ctx, message)
}

// HeartflowPeople 把心流的人员登记接到 personinfo
type HeartflowPeople struct {
	Registry *personinfo.Registry
}

// RegisterPerson 实现 heartflow.PersonRegistrar
func (p *HeartflowPeople) RegisterPerson(ctx context.Context, platform, userID, nickname, groupID, groupNickname string) error {
	if p == nil || p.Registry == nil {
		return errors.New("人物登记服务未接线")
	}
	input := personinfo.RegisterInput{
		Platform:      platform,
		UserID:        userID,
		Nickname:      nickname,
		GroupNickName: groupNickname,
	}
	if trimmed := strings.TrimSpace(groupID); trimmed != "" {
		if value, err := strconv.ParseInt(trimmed, 10, 64); err == nil && value > 0 {
			input.GroupID = value
		} else {
			input.GroupKey = trimmed
		}
	}
	_, err := p.Registry.RegisterPerson(ctx, input)
	if err != nil {
		return fmt.Errorf("登记人物信息失败: %w", err)
	}
	return nil
}

// HeartflowProcessor 把宿主 SessionMessage 转成心流消息并交给接收器
type HeartflowProcessor struct {
	Receiver *heartflow.Receiver
	OnError  func(context.Context, *messagereceive.SessionMessage, error)
}

// ProcessMessage 实现 messagereceive.HeartflowProcessor；接收器错误必须向上返回
func (p *HeartflowProcessor) ProcessMessage(ctx context.Context, message *messagereceive.SessionMessage) error {
	if p == nil || p.Receiver == nil {
		return errors.New("心流接收器未接线")
	}
	if message == nil {
		return errors.New("消息不能为空")
	}
	converted := &heartflow.SessionMessage{
		MessageID:          message.MessageID,
		SessionID:          message.SessionID,
		Platform:           message.Platform,
		ProcessedPlainText: message.PlainText(),
		Timestamp:          message.Timestamp,
		IsNotify:           message.IsNotify,
		IsAt:               message.IsAt,
		IsMentioned:        message.IsMentioned,
		Source:             message,
	}
	if message.RawMessage != nil {
		kind, length := debounce.ShapeFromComponents(message.RawMessage.Components)
		converted.ShapeKind = &kind
		converted.ShapeLength = &length
	}
	if !message.ReceivedAt.IsZero() {
		value := message.ReceivedAt
		converted.ReceivedAt = &value
	}
	if userID := strings.TrimSpace(message.UserID()); userID != "" || message.Nickname() != "" {
		converted.MessageInfo.User = &heartflow.UserInfo{
			UserID:   userID,
			Nickname: message.Nickname(),
			Cardname: message.GroupCard(),
		}
	}
	if message.MessageInfo.GroupInfo != nil {
		groupID := strings.TrimSpace(message.MessageInfo.GroupInfo.GroupID)
		if groupID != "" {
			converted.MessageInfo.Group = &heartflow.GroupInfo{
				GroupID:   groupID,
				GroupName: message.GroupName(),
			}
		}
	}
	converted.MessageInfo.AdditionalConfig = message.MessageInfo.AdditionalConfig
	err := p.Receiver.ProcessMessageStrict(ctx, converted)
	if err != nil && p.OnError != nil {
		p.OnError(ctx, message, err)
	}
	return err
}
