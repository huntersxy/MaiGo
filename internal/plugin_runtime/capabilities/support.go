package capabilities

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	chatsession "MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/platform_io"
)

// 能力层用到的错误类型与会话/消息转换工具。
//
// CapabilityError 是能力实现的统一错误封装（对齐 Python capability_service
// 的异常约定）；resolvePluginChatSessionID 把插件传入的 chat_id 归一成
// chatsession 的 session_id；serializePluginMessages 把消息记录转成插件可见
// 的 map 形态。三者都只依赖 database/chat 的基础类型，与 Host 状态无关。

type CapabilityError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *CapabilityError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// NewCapabilityError 构造带错误码的能力错误
func NewCapabilityError(code, message string) *CapabilityError {
	return &CapabilityError{Code: code, Message: message}
}

func resolvePluginChatSessionID(ctx context.Context, mem *memory.Manager, chatID string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "", fmt.Errorf("chat_id 不能为空")
	}
	mgr := chatsession.NewManager(mem.GetDB())
	if rec, err := mgr.GetExistingBySessionID(ctx, chatID); err == nil && rec != nil {
		return rec.SessionID, nil
	}
	if rec, err := mgr.ResolveSessionByConversationKey(ctx, platformio.PlatformQQ, chatID); err == nil && rec != nil {
		return rec.SessionID, nil
	}
	if targetID, kind, ok := parseConversationKey(chatID); ok {
		if rec, err := mgr.ResolveSession(ctx, platformio.PlatformQQ, strconv.FormatInt(targetID, 10), kind); err == nil && rec != nil {
			return rec.SessionID, nil
		}
	}
	return chatID, nil
}

func serializePluginMessages(rows []memory.MessageLog) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		additional := map[string]any{}
		for key, value := range row.AdditionalConfig {
			additional[key] = value
		}
		item := map[string]any{
			"message_id":           strconv.FormatInt(row.MessageID, 10),
			"chat_id":              row.SessionID,
			"session_id":           row.SessionID,
			"platform":             row.Platform,
			"account_id":           row.AccountID,
			"scope":                row.Scope,
			"chat_type":            row.ConversationKind,
			"target_id":            row.TargetID,
			"user_id":              row.UserID,
			"user_nickname":        row.UserNickname,
			"user_cardname":        row.UserCardname,
			"group_id":             row.GroupID,
			"group_name":           row.GroupName,
			"processed_plain_text": row.ProcessedPlainText,
			"is_mentioned":         row.IsMentioned,
			"is_at":                row.IsAt,
			"is_emoji":             row.IsEmoji,
			"is_picture":           row.IsPicture,
			"is_command":           row.IsCommand,
			"is_notify":            row.IsNotify,
			"additional_config":    additional,
			"timestamp":            row.Timestamp.Unix(),
			"message_time":         row.Timestamp.UTC().Format(time.RFC3339Nano),
		}
		out = append(out, item)
	}
	return out
}

func stringValue(raw any) string {
	if raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	return fmt.Sprint(raw)
}
