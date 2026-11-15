package cli

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/core/localoperator"

	"github.com/google/uuid"
)

// UserInfo 消息发送者信息，对齐 MaiBot MessageInfo.user_info
type UserInfo struct {
	UserID       string
	UserNickname string
	UserCardname string
}

// GroupInfo 群聊信息，对齐 MaiBot MessageInfo.group_info
type GroupInfo struct {
	GroupID   string
	GroupName string
}

// SessionMessage CLI 侧会话消息，保留 HeartFC 处理链需要的字段
type SessionMessage struct {
	MessageID          string
	Timestamp          time.Time
	ReceivedAt         time.Time
	Platform           string
	AccountID          string
	Scope              string
	UserInfo           *UserInfo
	GroupInfo          *GroupInfo
	AdditionalConfig   map[string]any
	RawMessage         *messagecomponent.MessageSequence
	ProcessedPlainText string
	Initialized        bool
	SessionID          string
}

// NewSessionMessage 创建一条空消息
func NewSessionMessage(messageID string, timestamp time.Time, platform string, receivedAt time.Time) *SessionMessage {
	return &SessionMessage{
		MessageID:        messageID,
		Timestamp:        timestamp,
		Platform:         platform,
		ReceivedAt:       receivedAt,
		AdditionalConfig: map[string]any{},
		RawMessage:       messagecomponent.NewMessageSequence(),
	}
}

// BuildMaisakaCLISessionMessage 构造 mai_cli_{毫秒时间戳} 的 CLI 用户消息
func BuildMaisakaCLISessionMessage(userText string, timestamp time.Time) *SessionMessage {
	message := NewSessionMessage(
		fmt.Sprintf("maisaka_cli_%d", timestamp.UnixMilli()),
		timestamp,
		localoperator.MaisakaCLIPlatform,
		timestamp,
	)
	message.AccountID = localoperator.MaisakaCLIBotID
	message.UserInfo = &UserInfo{UserID: "maisaka_user", UserNickname: "用户"}
	message.RawMessage.Text(userText)
	message.ProcessedPlainText = userText
	message.Initialized = true
	return message
}

// BuildBotConsoleMessage 构造本地操作员终端消息
func BuildBotConsoleMessage(text string, timestamp time.Time) *SessionMessage {
	message := NewSessionMessage(
		"bot_console_"+strings.ReplaceAll(uuid.NewString(), "-", ""),
		timestamp,
		localoperator.BotConsolePlatform,
		timestamp,
	)
	message.AccountID = localoperator.BotConsoleBotID
	message.UserInfo = &UserInfo{UserID: localoperator.BotConsoleUserID, UserNickname: "本地操作员"}
	message.AdditionalConfig = map[string]any{localoperator.LocalOperatorConfig: true}
	message.RawMessage.Text(text)
	return message
}

// Validate 校验 HeartFC 处理链必需字段，错误文案对齐 Python
func (m *SessionMessage) Validate() error {
	if m == nil {
		return fmt.Errorf("message 不能为空")
	}
	if m.UserInfo == nil || strings.TrimSpace(m.Platform) == "" {
		return fmt.Errorf("message userinfo or platform is missing")
	}
	if strings.TrimSpace(m.UserInfo.UserID) == "" || strings.TrimSpace(m.UserInfo.UserNickname) == "" {
		return fmt.Errorf("message userinfo id or nickname is missing")
	}
	return nil
}

// GroupChat 是否群聊消息
func (m *SessionMessage) GroupChat() bool {
	return m != nil && m.GroupInfo != nil
}

// DisplayChatName 日志中的会话名称，对齐 Python：有群信息用群名（可为空），否则“私聊”
func (m *SessionMessage) DisplayChatName() string {
	if m != nil && m.GroupInfo != nil {
		return m.GroupInfo.GroupName
	}
	return "私聊"
}

// ComponentNames 返回原始消息组件类型名，空文本回复时用于占位
func (m *SessionMessage) ComponentNames() []string {
	if m == nil || m.RawMessage == nil {
		return nil
	}
	names := make([]string, 0, len(m.RawMessage.Components))
	for _, component := range m.RawMessage.Components {
		if component == nil {
			continue
		}
		names = append(names, component.FormatName())
	}
	return names
}

// FallbackText 空正文时用组件类型名拼出 [text, image]
func (m *SessionMessage) FallbackText() string {
	names := m.ComponentNames()
	if len(names) == 0 {
		return "[]"
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// EnsureSessionID 按 Python SessionUtils.calculate_session_id 计算 MD5 会话编号
func (m *SessionMessage) EnsureSessionID() error {
	if m == nil {
		return fmt.Errorf("message 不能为空")
	}
	if strings.TrimSpace(m.SessionID) != "" {
		return nil
	}
	if m.UserInfo == nil {
		return fmt.Errorf("UserID 或 GroupID 必须提供其一")
	}
	groupID := ""
	if m.GroupInfo != nil {
		groupID = m.GroupInfo.GroupID
	}
	sessionID, err := CalculateSessionID(m.Platform, m.UserInfo.UserID, groupID, m.AccountID, m.Scope)
	if err != nil {
		return err
	}
	m.SessionID = sessionID
	return nil
}

// CalculateSessionID 对齐 MaiBot SessionUtils.calculate_session_id
func CalculateSessionID(platform, userID, groupID, accountID, scope string) (string, error) {
	if userID == "" && groupID == "" {
		return "", fmt.Errorf("UserID 或 GroupID 必须提供其一")
	}
	components := make([]string, 0, 5)
	components = append(components, platform)
	if accountID != "" {
		components = append(components, "account:"+accountID)
	}
	if scope != "" {
		components = append(components, "scope:"+scope)
	}
	if groupID != "" {
		components = append(components, groupID)
	} else {
		components = append(components, userID, "private")
	}
	sum := md5.Sum([]byte(strings.Join(components, "_")))
	return hex.EncodeToString(sum[:]), nil
}

// PreviewText 日志预览：合并空白，超过 200 个字符截断并标注总字数
func (m *SessionMessage) PreviewText() (preview string, full string) {
	if m == nil {
		return "", ""
	}
	full = m.ProcessedPlainText
	preview = strings.Join(strings.Fields(full), " ")
	runes := []rune(preview)
	if len(runes) > 200 {
		preview = string(runes[:200]) + fmt.Sprintf("…（共%d字，已截断）", len([]rune(full)))
	}
	return preview, full
}
