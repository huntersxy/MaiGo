// Package messagereceive 移植 MaiBot chat/message_receive 的消息模型、会话管理、
// 入站命令链与统一发送链；外部数据库、平台与模型能力通过端口注入
package messagereceive

import (
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/data_models/messagecomponent"
)

// UserInfo 对齐 MaiBot UserInfo
type UserInfo struct {
	UserID       string
	UserNickname string
	UserCardname *string
}

// GroupInfo 对齐 MaiBot GroupInfo
type GroupInfo struct {
	GroupID   string
	GroupName string
}

// MessageInfo 对齐 MaiBot MessageInfo
type MessageInfo struct {
	UserInfo         UserInfo
	GroupInfo        *GroupInfo
	AdditionalConfig map[string]any
}

// SessionMessage 对齐 MaiBot SessionMessage 的 Go 投影
// Optional 字段用指针表达 Python None；消息组件保持 MaiBot 序列化格式
type SessionMessage struct {
	MessageID          string
	Timestamp          time.Time
	ReceivedAt         time.Time
	Initialized        bool
	Platform           string
	AccountID          *string
	Scope              *string
	MessageInfo        MessageInfo
	IsMentioned        bool
	IsAt               bool
	IsEmoji            bool
	IsPicture          bool
	IsCommand          bool
	IsNotify           bool
	SessionID          string
	ReplyTo            *string
	ReplyFrequency     *float64
	ProcessedPlainText *string
	RawMessage         *messagecomponent.MessageSequence
	FileSource         map[string]any
	PlatformMessageID  *string
}

// ChatType 返回 group / private，对齐 MaiBot SessionMessage.chat_type
func (m *SessionMessage) ChatType() string {
	if m == nil || m.MessageInfo.GroupInfo == nil {
		return "private"
	}
	if strings.TrimSpace(m.MessageInfo.GroupInfo.GroupID) == "" {
		return "private"
	}
	return "group"
}

// IsGroup 是否为群聊消息
func (m *SessionMessage) IsGroup() bool { return m.ChatType() == "group" }

// TargetID 返回群号或私聊用户号
func (m *SessionMessage) TargetID() string {
	if m == nil {
		return ""
	}
	if m.IsGroup() {
		return strings.TrimSpace(m.MessageInfo.GroupInfo.GroupID)
	}
	return strings.TrimSpace(m.MessageInfo.UserInfo.UserID)
}

// UserID 返回发送者 ID
func (m *SessionMessage) UserID() string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.MessageInfo.UserInfo.UserID)
}

// Nickname 返回发送者昵称
func (m *SessionMessage) Nickname() string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.MessageInfo.UserInfo.UserNickname)
}

// GroupCard 返回群名片
func (m *SessionMessage) GroupCard() string {
	if m == nil || m.MessageInfo.UserInfo.UserCardname == nil {
		return ""
	}
	return strings.TrimSpace(*m.MessageInfo.UserInfo.UserCardname)
}

// GroupName 返回群名
func (m *SessionMessage) GroupName() string {
	if m == nil || m.MessageInfo.GroupInfo == nil {
		return ""
	}
	return strings.TrimSpace(m.MessageInfo.GroupInfo.GroupName)
}

// AdditionalConfigSafe 返回非 nil 的附加配置 map
func (m *SessionMessage) AdditionalConfigSafe() map[string]any {
	if m == nil {
		return map[string]any{}
	}
	if m.MessageInfo.AdditionalConfig == nil {
		m.MessageInfo.AdditionalConfig = map[string]any{}
	}
	return m.MessageInfo.AdditionalConfig
}

// PlainText 返回已处理正文
func (m *SessionMessage) PlainText() string {
	if m == nil || m.ProcessedPlainText == nil {
		return ""
	}
	return *m.ProcessedPlainText
}

// SetPlainText 设置已处理正文
func (m *SessionMessage) SetPlainText(text string) {
	m.ProcessedPlainText = &text
}

// Clone 复制消息，避免 Hook 覆盖修改共享对象
func (m *SessionMessage) Clone() *SessionMessage {
	if m == nil {
		return nil
	}
	cloned := *m
	if m.AccountID != nil {
		value := *m.AccountID
		cloned.AccountID = &value
	}
	if m.Scope != nil {
		value := *m.Scope
		cloned.Scope = &value
	}
	if m.ReplyTo != nil {
		value := *m.ReplyTo
		cloned.ReplyTo = &value
	}
	if m.ReplyFrequency != nil {
		value := *m.ReplyFrequency
		cloned.ReplyFrequency = &value
	}
	if m.ProcessedPlainText != nil {
		value := *m.ProcessedPlainText
		cloned.ProcessedPlainText = &value
	}
	if m.PlatformMessageID != nil {
		value := *m.PlatformMessageID
		cloned.PlatformMessageID = &value
	}
	if m.MessageInfo.UserInfo.UserCardname != nil {
		value := *m.MessageInfo.UserInfo.UserCardname
		cloned.MessageInfo.UserInfo.UserCardname = &value
	}
	if m.MessageInfo.GroupInfo != nil {
		group := *m.MessageInfo.GroupInfo
		cloned.MessageInfo.GroupInfo = &group
	}
	if m.MessageInfo.AdditionalConfig != nil {
		cloned.MessageInfo.AdditionalConfig = make(map[string]any, len(m.MessageInfo.AdditionalConfig))
		for key, value := range m.MessageInfo.AdditionalConfig {
			cloned.MessageInfo.AdditionalConfig[key] = value
		}
	}
	if m.RawMessage != nil {
		cloned.RawMessage = &messagecomponent.MessageSequence{
			Components: messagecomponent.CloneComponents(m.RawMessage.Components),
		}
	}
	if m.FileSource != nil {
		cloned.FileSource = make(map[string]any, len(m.FileSource))
		for key, value := range m.FileSource {
			cloned.FileSource[key] = value
		}
	}
	return &cloned
}

// ToHookMap 序列化入站 Hook 的 message 参数
func (m *SessionMessage) ToHookMap() map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"message_id":   m.MessageID,
		"initialized":  m.Initialized,
		"platform":     m.Platform,
		"is_mentioned": m.IsMentioned,
		"is_at":        m.IsAt,
		"is_emoji":     m.IsEmoji,
		"is_picture":   m.IsPicture,
		"is_command":   m.IsCommand,
		"is_notify":    m.IsNotify,
		"session_id":   m.SessionID,
	}
	if !m.Timestamp.IsZero() {
		out["timestamp"] = strconv.FormatFloat(float64(m.Timestamp.UnixNano())/1e9, 'f', -1, 64)
	}
	if !m.ReceivedAt.IsZero() {
		out["received_at"] = m.ReceivedAt.Format("2006-01-02T15:04:05.000000")
	}
	if m.AccountID != nil {
		out["account_id"] = *m.AccountID
	}
	if m.Scope != nil {
		out["scope"] = *m.Scope
	}
	if m.ReplyTo != nil {
		out["reply_to"] = *m.ReplyTo
	}
	if m.ReplyFrequency != nil {
		out["reply_frequency"] = *m.ReplyFrequency
	}
	if m.ProcessedPlainText != nil {
		out["processed_plain_text"] = *m.ProcessedPlainText
	}
	if m.PlatformMessageID != nil {
		out["platform_message_id"] = *m.PlatformMessageID
	}
	messageInfo := map[string]any{
		"user_info": map[string]any{
			"user_id":       m.MessageInfo.UserInfo.UserID,
			"user_nickname": m.MessageInfo.UserInfo.UserNickname,
			"user_cardname": m.MessageInfo.UserInfo.UserCardname,
		},
		"additional_config": m.MessageInfo.AdditionalConfig,
	}
	if m.MessageInfo.GroupInfo != nil {
		messageInfo["group_info"] = map[string]any{
			"group_id":   m.MessageInfo.GroupInfo.GroupID,
			"group_name": m.MessageInfo.GroupInfo.GroupName,
		}
	}
	out["message_info"] = messageInfo
	if m.RawMessage != nil {
		out["raw_message"] = m.RawMessage.ToDict()
	} else {
		out["raw_message"] = []any{}
	}
	return out
}

// ApplyHookPatch 用 Hook 返回的 message 字典更新消息；不存在的字段保持原值
func (m *SessionMessage) ApplyHookPatch(patch map[string]any) {
	if m == nil || patch == nil {
		return
	}
	if value, ok := stringField(patch, "message_id"); ok {
		m.MessageID = value
	}
	if value, ok := stringField(patch, "platform"); ok {
		m.Platform = value
	}
	if value, ok := stringField(patch, "session_id"); ok {
		m.SessionID = value
	}
	if value, ok := stringField(patch, "processed_plain_text"); ok {
		m.SetPlainText(value)
	}
	if value, ok := boolField(patch, "is_mentioned"); ok {
		m.IsMentioned = value
	}
	if value, ok := boolField(patch, "is_command"); ok {
		m.IsCommand = value
	}
	if value, ok := boolField(patch, "is_notify"); ok {
		m.IsNotify = value
	}
	if value, ok := boolField(patch, "is_at"); ok {
		m.IsAt = value
	}
	if value, ok := boolField(patch, "is_emoji"); ok {
		m.IsEmoji = value
	}
	if value, ok := boolField(patch, "is_picture"); ok {
		m.IsPicture = value
	}
	if value, ok := boolField(patch, "initialized"); ok {
		m.Initialized = value
	}
	if value, ok := optionalStringValue(patch["reply_to"]); ok {
		m.ReplyTo = value
	}
	if value, ok := floatField(patch, "reply_frequency"); ok {
		m.ReplyFrequency = &value
	}
	if value, ok := optionalStringValue(patch["platform_message_id"]); ok {
		m.PlatformMessageID = value
	}
	if value, ok := patch["file_source"].(map[string]any); ok {
		m.FileSource = cloneMap(value)
	}
	if value, ok := patch["account_id"].(string); ok {
		m.AccountID = stringPointer(strings.TrimSpace(value))
	}
	if value, ok := patch["scope"].(string); ok {
		m.Scope = stringPointer(strings.TrimSpace(value))
	}
	if raw, ok := patch["raw_message"].([]any); ok {
		if sequence, err := messagecomponent.FromDict(raw); err == nil {
			m.RawMessage = sequence
		}
	}
	if info, ok := patch["message_info"].(map[string]any); ok {
		applyMessageInfoPatch(&m.MessageInfo, info)
	}
}

func applyMessageInfoPatch(info *MessageInfo, patch map[string]any) {
	if userInfo, ok := patch["user_info"].(map[string]any); ok {
		if value, ok := userInfo["user_id"].(string); ok {
			info.UserInfo.UserID = value
		}
		if value, ok := userInfo["user_nickname"].(string); ok {
			info.UserInfo.UserNickname = value
		}
		if value, ok := userInfo["user_cardname"]; ok {
			cardname, _ := optionalStringValue(value)
			info.UserInfo.UserCardname = cardname
		}
	}
	if groupInfo, ok := patch["group_info"]; ok {
		if groupInfo == nil {
			info.GroupInfo = nil
		} else if mapping, ok := groupInfo.(map[string]any); ok {
			group := &GroupInfo{}
			if value, ok := mapping["group_id"].(string); ok {
				group.GroupID = value
			}
			if value, ok := mapping["group_name"].(string); ok {
				group.GroupName = value
			}
			info.GroupInfo = group
		}
	}
	if config, ok := patch["additional_config"].(map[string]any); ok {
		info.AdditionalConfig = config
	}
}

func stringField(mapping map[string]any, key string) (string, bool) {
	value, ok := mapping[key]
	if !ok || value == nil {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func boolField(mapping map[string]any, key string) (bool, bool) {
	value, ok := mapping[key]
	if !ok {
		return false, false
	}
	result, ok := value.(bool)
	return result, ok
}

func stringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func optionalStringValue(value any) (*string, bool) {
	if value == nil {
		return nil, true
	}
	text, ok := value.(string)
	if !ok {
		return nil, false
	}
	return &text, true
}

func floatField(mapping map[string]any, key string) (float64, bool) {
	value, ok := mapping[key]
	if !ok || value == nil {
		return 0, false
	}
	switch item := value.(type) {
	case int:
		return float64(item), true
	case int64:
		return float64(item), true
	case float32:
		return float64(item), true
	case float64:
		return item, true
	default:
		return 0, false
	}
}
