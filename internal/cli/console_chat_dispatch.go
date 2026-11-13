package cli

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/data_models/messagecomponent"
)

// BotConsoleMessageReceiver 接收终端输入的正式消息处理链
// 对齐 MaiBot BotConsole._dispatch_input 调用的 chat_bot.receive_message
type BotConsoleMessageReceiver interface {
	ReceiveMessage(ctx context.Context, message *messagereceive.SessionMessage) error
}

// NewBotConsoleDispatch 把终端输入转换成正式会话消息并交给接收链
// 返回 ChatBot 计算出的 session_id；错误原样上抛，不吞掉处理失败
func NewBotConsoleDispatch(receiver BotConsoleMessageReceiver) func(ctx context.Context, message *SessionMessage) (string, error) {
	return func(ctx context.Context, message *SessionMessage) (string, error) {
		if receiver == nil {
			return "", fmt.Errorf("终端消息接收链不能为空")
		}
		converted, err := botConsoleReceiveMessage(message)
		if err != nil {
			return "", err
		}
		if err := receiver.ReceiveMessage(ctx, converted); err != nil {
			return "", err
		}
		return converted.SessionID, nil
	}
}

// botConsoleReceiveMessage 投影 CLI 消息为 messagereceive.SessionMessage
// Python 的 SessionMessage 是同一个对象，Go 由 CLI 与本包各持一份模型，这里做字段级转换
func botConsoleReceiveMessage(message *SessionMessage) (*messagereceive.SessionMessage, error) {
	if message == nil {
		return nil, fmt.Errorf("终端消息不能为空")
	}
	if err := message.Validate(); err != nil {
		return nil, err
	}
	plainText := cliPlainText(message)
	if strings.TrimSpace(plainText) != "" {
		message.ProcessedPlainText = plainText
	}
	if err := message.EnsureSessionID(); err != nil {
		return nil, err
	}
	converted := &messagereceive.SessionMessage{
		MessageID:   message.MessageID,
		Timestamp:   message.Timestamp,
		ReceivedAt:  message.ReceivedAt,
		Initialized: message.Initialized,
		Platform:    message.Platform,
		SessionID:   message.SessionID,
		RawMessage:  message.RawMessage,
		MessageInfo: messagereceive.MessageInfo{
			UserInfo: messagereceive.UserInfo{
				UserID:       message.UserInfo.UserID,
				UserNickname: message.UserInfo.UserNickname,
			},
			AdditionalConfig: message.AdditionalConfig,
		},
	}
	if message.UserInfo != nil && strings.TrimSpace(message.UserInfo.UserCardname) != "" {
		cardname := message.UserInfo.UserCardname
		converted.MessageInfo.UserInfo.UserCardname = &cardname
	}
	if message.GroupInfo != nil {
		converted.MessageInfo.GroupInfo = &messagereceive.GroupInfo{
			GroupID:   message.GroupInfo.GroupID,
			GroupName: message.GroupInfo.GroupName,
		}
	}
	if text := strings.TrimSpace(message.AccountID); text != "" {
		converted.AccountID = &text
	}
	if text := strings.TrimSpace(message.Scope); text != "" {
		converted.Scope = &text
	}
	if text := strings.TrimSpace(message.ProcessedPlainText); text != "" {
		converted.ProcessedPlainText = &text
	}
	return converted, nil
}

// cliPlainText 从原始消息组件提取纯文本，对齐 Python MessageSequence 的文本投影
func cliPlainText(message *SessionMessage) string {
	if message == nil || message.RawMessage == nil {
		return ""
	}
	parts := make([]string, 0, len(message.RawMessage.Components))
	for _, component := range message.RawMessage.Components {
		text, ok := component.(messagecomponent.TextComponent)
		if !ok {
			continue
		}
		parts = append(parts, text.Text)
	}
	return strings.Join(parts, "")
}
