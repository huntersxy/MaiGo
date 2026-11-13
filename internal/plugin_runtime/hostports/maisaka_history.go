package hostports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"maibot-go/internal/maisaka"
	maisakactx "maibot-go/internal/maisaka/context"
	"maibot-go/internal/services"
)

// MaisakaRuntimeProvider 按 session_id 提供已创建的 Maisaka 运行时
// *agent.Agent 的 SessionRuntime 方法直接满足该接口
type MaisakaRuntimeProvider interface {
	SessionRuntime(sessionID string) (*maisaka.Runtime, error)
}

type maisakaHistoryAppender struct {
	provider MaisakaRuntimeProvider
}

// NewMaisakaHistoryAppender 把已发送消息写回真实 Maisaka 历史
func NewMaisakaHistoryAppender(provider MaisakaRuntimeProvider) services.MaisakaHistoryAppender {
	return &maisakaHistoryAppender{provider: provider}
}

// AppendSentMessage 对齐 Python append_sent_message_to_chat_history 的常用字段
// 运行时尚未创建时返回真实错误，由 SendService 记录告警并保持发送非阻断
func (a *maisakaHistoryAppender) AppendSentMessage(ctx context.Context, message services.MaisakaSentMessage) error {
	if a == nil || a.provider == nil {
		return errors.New("Maisaka 历史 appender 未接线：会话运行时提供者为空")
	}
	if ctx == nil {
		return errors.New("同步 Maisaka 历史失败：context 为空")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID := strings.TrimSpace(message.SessionID)
	if sessionID == "" {
		return errors.New("同步 Maisaka 历史失败：session_id 为空")
	}
	content := strings.TrimSpace(message.Text)
	if content == "" {
		return errors.New("同步 Maisaka 历史失败：消息文本为空")
	}
	runtime, err := a.provider.SessionRuntime(sessionID)
	if err != nil {
		return fmt.Errorf("查找 Maisaka 会话运行时失败: %w", err)
	}
	if runtime == nil {
		return fmt.Errorf("查找 Maisaka 会话运行时失败: session_id=%s", sessionID)
	}
	timestamp := message.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	sourceKind := strings.TrimSpace(message.SourceKind)
	if sourceKind == "" {
		sourceKind = "plugin_send"
	}
	messageIDText := strings.TrimSpace(message.MessageID)
	speakerName := strings.TrimSpace(message.SpeakerName)
	quoteIDs := make([]string, 0, 1)
	if replyTo := strings.TrimSpace(message.ReplyToMessageID); replyTo != "" {
		quoteIDs = append(quoteIDs, replyTo)
	}
	plannerPrefix := maisakactx.BuildPlannerPrefix(maisakactx.PlannerPrefixInput{
		Timestamp:        timestamp,
		UserName:         speakerName,
		MessageID:        messageIDText,
		ChatID:           sessionID,
		ChatName:         runtime.SessionName(),
		QuoteIDs:         quoteIDs,
		IncludeMessageID: messageIDText != "",
		IncludeChatID:    runtime.FocusModeActive(),
		IsSelfMessage:    true,
	})
	historyMessage := maisaka.ContextMessage{
		Role:           maisaka.RoleAssistant,
		Content:        plannerPrefix + content,
		VisibleText:    maisakactx.FormatSpeakerContent(speakerName, content, &timestamp, messageIDText),
		Source:         sourceKind,
		Timestamp:      timestamp,
		MessageID:      messageIDText,
		SessionID:      sessionID,
		Platform:       message.Platform,
		UserID:         message.AccountID,
		CountInContext: true,
	}
	runtime.AppendHistory(historyMessage)
	runtime.RecordFocusSentMessage(historyMessage)
	runtime.EmitMonitorMessageSent(historyMessage, speakerName, sourceKind)
	return nil
}
