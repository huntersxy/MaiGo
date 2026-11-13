package memory

import (
	"strings"
	"time"

	"maibot-go/internal/common/media"
)

// NormalizeContent 统一记忆去重时的空白和大小写
func NormalizeContent(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// MessageLog 对应 MaiBot mai_messages（database_model.py::Messages），表名与列名逐字对齐。
//
// conversation_kind/target_id 等列是 maibot-go 的扩展（MaiBot 用 group_id 是否为空
// 隐含判断群/私聊），跟版时注意不要与官方新增列重名。
type MessageLog struct {
	ID                 uint                    `gorm:"primaryKey" json:"id"`
	MessageID          int64                   `gorm:"not null" json:"message_id"`
	ConversationKind   string                  `gorm:"type:text;not null" json:"conversation_kind"` // MaiBot chat_type: group|private
	TargetID           int64                   `gorm:"column:target_id;index;not null" json:"target_id"`
	UserID             int64                   `gorm:"index;not null" json:"user_id"`
	UserNickname       string                  `gorm:"type:text;not null" json:"user_nickname"`
	UserCardname       string                  `gorm:"column:user_cardname;type:text" json:"user_cardname,omitempty"`
	GroupID            string                  `gorm:"column:group_id;type:text;not null;default:''" json:"group_id,omitempty"`
	GroupName          string                  `gorm:"column:group_name;type:text;not null;default:''" json:"group_name,omitempty"`
	ProcessedPlainText string                  `gorm:"type:text;not null" json:"processed_plain_text"`
	ReplyTo            *int64                  `json:"reply_to,omitempty"`
	IsMentioned        bool                    `gorm:"not null" json:"is_mentioned"`
	IsAt               bool                    `gorm:"column:is_at;not null" json:"is_at"`
	IsEmoji            bool                    `gorm:"column:is_emoji;not null" json:"is_emoji"`
	IsPicture          bool                    `gorm:"column:is_picture;not null" json:"is_picture"`
	IsCommand          bool                    `gorm:"column:is_command;not null" json:"is_command"`
	IsNotify           bool                    `gorm:"column:is_notify;not null" json:"is_notify"`
	Timestamp          time.Time               `gorm:"index;not null" json:"timestamp"`
	ReceivedAt         *time.Time              `gorm:"column:received_at;index" json:"received_at,omitempty"`
	ReplyFrequency     *float64                `gorm:"column:reply_frequency" json:"reply_frequency,omitempty"`
	RawContent         []byte                  `gorm:"column:raw_content;type:bytea" json:"raw_content,omitempty"`
	AdditionalConfig   MessageAdditionalConfig `gorm:"column:additional_config;type:text" json:"additional_config,omitempty"`
	MediaRefs          media.Refs              `gorm:"column:media_refs;type:text" json:"media_refs,omitempty"`
	Platform           string                  `gorm:"type:text;not null;default:qq" json:"platform"`
	SessionID          string                  `gorm:"type:text;index;not null" json:"session_id,omitempty"`
	// 非 QQ 平台原始字符串身份；QQ 消息留空，TargetID/UserID/OneBotMessageID 保持原 int64 语义
	ExternalMessageID string `gorm:"column:external_message_id;type:text;not null;default:''" json:"external_message_id,omitempty"`
	ExternalTargetID  string `gorm:"column:external_target_id;type:text;not null;default:''" json:"external_target_id,omitempty"`
	ExternalUserID    string `gorm:"column:external_user_id;type:text;not null;default:''" json:"external_user_id,omitempty"`
	AccountID         string `gorm:"column:account_id;type:text;not null;default:''" json:"account_id,omitempty"`
	Scope             string `gorm:"column:scope;type:text;not null;default:''" json:"scope,omitempty"`
}

func (MessageLog) TableName() string { return "mai_messages" }

// MessageForward 一份合并转发的内容，句柄出现在消息正文里
type MessageForward struct {
	ID           uint         `gorm:"primaryKey" json:"id"`
	Handle       string       `gorm:"column:handle;type:text;not null;uniqueIndex" json:"handle"`
	MessageLogID uint         `gorm:"column:message_log_id;index;not null" json:"message_log_id"`
	ParentHandle string       `gorm:"column:parent_handle;type:text;index" json:"parent_handle,omitempty"`
	Nodes        ForwardNodes `gorm:"column:nodes;type:text;not null" json:"nodes"`
	CreatedAt    time.Time    `gorm:"column:created_at;not null" json:"created_at"`
}

func (MessageForward) TableName() string { return "message_forwards" }

const RecalledMessageProcessedPlainText = "【消息已撤回】"

type TopicAssignment struct {
	ID           uint  `gorm:"primaryKey" json:"id"`
	MessageLogID uint  `gorm:"uniqueIndex;not null" json:"message_log_id"`
	TopicID      *uint `gorm:"index" json:"topic_id,omitempty"`
}

func (TopicAssignment) TableName() string { return "topic_assignments" }

type TopicParticipant struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
}

type TopicSummary struct {
	Title        string             `json:"title"`
	Gist         string             `json:"gist"`
	Participants []TopicParticipant `json:"participants"`
	OpenLoops    []string           `json:"open_loops"`
	RecentTurns  []string           `json:"recent_turns"`
	Keywords     []string           `json:"keywords"`
}

type MemberProfile struct {
	UserID    int64     `gorm:"primaryKey" json:"user_id"`
	Nickname  string    `gorm:"type:text;not null" json:"nickname"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MemberProfile) TableName() string { return "member_profiles" }

type MemberName struct {
	UserID    int64     `gorm:"primaryKey" json:"user_id"`
	GroupID   int64     `gorm:"primaryKey" json:"group_id"`
	Value     string    `gorm:"primaryKey;type:text" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MemberName) TableName() string { return "member_names" }

type MoodState struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UpdatedAt   time.Time `json:"updated_at"`
	Energy      float64   `gorm:"not null;default:0.5" json:"energy"`
	Sociability float64   `gorm:"not null;default:0.5" json:"sociability"`
	LastReason  string    `gorm:"type:text" json:"last_reason,omitempty"`
}

func (MoodState) TableName() string { return "mood_state" }

type SchemaMigration struct {
	Version   int       `gorm:"primaryKey" json:"version"`
	Name      string    `gorm:"type:text;not null" json:"name"`
	AppliedAt time.Time `gorm:"not null;default:now()" json:"applied_at"`
}

func (SchemaMigration) TableName() string { return "schema_migrations" }
