package chatwiring

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/data_models/messagecomponent"
)

// SenderText 把命令回复接到统一发送链，避免命令链另建一套平台发送
type SenderText struct {
	Sender *messagereceive.Sender
}

// SendText 实现 messagereceive.TextSender
func (s *SenderText) SendText(ctx context.Context, message *messagereceive.SessionMessage, text string) error {
	if s == nil || s.Sender == nil {
		return errors.New("统一发送器未接线")
	}
	if message == nil {
		return errors.New("消息不能为空")
	}
	if strings.TrimSpace(message.MessageID) == "" {
		return fmt.Errorf("命令回复缺少 message_id，无法发送")
	}
	reply := message.Clone()
	reply.SetPlainText(text)
	reply.RawMessage = messagecomponent.NewMessageSequence().Text(text)
	_, err := s.Sender.SendMessage(ctx, reply, messagereceive.SendOptions{StorageMessage: false, ShowLog: false})
	return err
}
