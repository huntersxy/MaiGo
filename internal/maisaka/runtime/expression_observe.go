package runtime

import (
	"context"
	"strconv"
	"strings"

	"MaiGo/internal/learners"
	"MaiGo/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
)

func (a *Agent) observeExpressionInbound(ctx context.Context, msg *onebot.ConversationMessage) {
	if msg == nil || msg.UserID == a.SelfID() || msg.MessageID == 0 || msg.IsNotify {
		return
	}
	sessionID := a.resolveMaiBotSessionID(ctx, msg.ConversationKind, msg.TargetID)
	if sessionID == "" {
		zap.L().Debug("表达学习跳过：未解析到会话", zap.Int64("message_id", msg.MessageID))
		return
	}
	_, enableLearning := learners.DefaultRegistry().ChatConfig().GetExpressionConfig(sessionID)
	if !enableLearning {
		zap.L().Debug("表达学习跳过：学习开关关闭", zap.String("session_id", sessionID))
		return
	}
	text := strings.TrimSpace(msg.FinalContent)
	if text == "" {
		text = strings.TrimSpace(msg.Content)
	}
	if text == "" {
		return
	}
	learner := a.expressionLearnerFor(sessionID)
	if learner == nil {
		zap.L().Debug("表达学习跳过：学习器未创建", zap.String("session_id", sessionID))
		return
	}
	platform := strings.TrimSpace(msg.Platform)
	if platform == "" {
		platform = a.conversationPlatform(msg.ConversationKind, msg.TargetID)
	}
	learner.ObserveContext(learners.ContextMessage{
		MessageID:  strconv.FormatInt(msg.MessageID, 10),
		SourceKind: "user",
		Text:       text,
		Platform:   platform,
		UserID:     strconv.FormatInt(msg.UserID, 10),
		Name:       strings.TrimSpace(msg.Nickname),
	})
	zap.L().Debug("表达学习缓冲入站消息",
		zap.String("session_id", sessionID),
		zap.Int64("message_id", msg.MessageID))
	zap.L().Debug("表达学习已入缓冲",
		zap.String("session_id", sessionID),
		zap.Int("pending", learner.PendingCount()),
		zap.Int("threshold", learners.DefaultMinMessagesForExtraction))
	learner.TryScheduleBatchAsync(ctx)
}
