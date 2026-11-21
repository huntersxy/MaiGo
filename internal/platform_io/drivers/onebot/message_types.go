package onebot

import (
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/common/data_models/messagecomponent"
)

const AtAllUserID int64 = -1

// MaxForwardDepth 限制补拉和展示的转发层数，最外层转发计为第 1 层
const MaxForwardDepth = 3

// MessagePart 保存消息段的原始顺序和对应分类数据索引
type MessagePart struct {
	Kind             string
	Text             string
	AtUserID         int64
	ExternalAtUserID string // 非 QQ 平台 @ 的原始字符串 ID
	AtNickname       string
	AtCardname       string
	Index            int
	Count            int
	Forward          *messagecomponent.ForwardNodeComponent
}

// ConversationMessage 群聊与私聊共用的消息事件
type ConversationMessage struct {
	Platform         string `json:"platform,omitempty"`   // 消息平台，空值按 QQ 处理
	AccountID        string `json:"account_id,omitempty"` // 平台机器人账号 / self_id
	Scope            string `json:"scope,omitempty"`      // 平台路由作用域
	MessageID        int64  `json:"message_id"`
	ConversationKind string `json:"conversation_kind"`
	TargetID         int64  `json:"target_id"`
	UserID           int64  `json:"user_id"`
	// External* 保留非 QQ 平台的原始字符串身份；MessageID/TargetID/UserID
	// 在无法解析为数值时使用 SyntheticIdentityID 生成稳定代理键。
	ExternalMessageID string           `json:"external_message_id,omitempty"`
	ExternalTargetID  string           `json:"external_target_id,omitempty"`
	ExternalUserID    string           `json:"external_user_id,omitempty"`
	Nickname          string           `json:"nickname"`                    // QQ 原始昵称
	GroupCard         string           `json:"group_card,omitempty"`        // 当前群名片
	Content           string           `json:"content"`                     // 纯文本内容
	MessageParts      []MessagePart    `json:"-"`                           // 所有消息段的展示顺序
	IsMentioned       bool             `json:"is_mentioned"`                // 是否@机器人
	IsNotify          bool             `json:"is_notify,omitempty"`         // 平台通知事件（戳一戳、撤回、禁言等），不是用户发言
	Time              time.Time        `json:"time"`                        // 消息时间
	ReceivedAt        time.Time        `json:"-"`                           // 当前进程从 OneBot 收到事件的时间
	ArrivalSeq        uint64           `json:"-"`                           // 该会话运行时顺序序号，Agent 按此提交
	SessionID         string           `json:"-"`                           // 运行期解析出的 MaiBot 会话 ID，仅进程内使用
	ParseFailed       bool             `json:"-"`                           // 消息段解析失败占位，仅用于消费序号
	Images            []ImageInfo      `json:"images,omitempty"`            // 图片列表
	Voices            []VoiceInfo      `json:"voices,omitempty"`            // 语音列表
	Videos            []VideoInfo      `json:"videos,omitempty"`            // 视频列表
	Faces             []FaceInfo       `json:"faces,omitempty"`             // 表情列表
	AtList            []int64          `json:"at_list,omitempty"`           // 消息指向的用户；普通消息为 @ 目标，互动消息为互动目标
	ExternalAtList    []string         `json:"external_at_list,omitempty"`  // 非 QQ 平台 @ 的原始字符串 ID，与 AtList 同序
	AtNames           map[int64]string `json:"at_names,omitempty"`          // @ 目标在事件中的显示名
	Reply             *ReplyInfo       `json:"reply,omitempty"`             // 回复信息
	FileNames         []string         `json:"file_names,omitempty"`        // 文件消息名称
	Files             []FileInfo       `json:"files,omitempty"`             // WebUI 文件附件资产
	Cards             []CardMessage    `json:"cards,omitempty"`             // 卡片消息
	Dicts             []any            `json:"-"`                           // dict/json 段原始负载，保留给 DictComponent
	FinalContent      string           `json:"final_content,omitempty"`     // 处理后的最终内容
	AdditionalConfig  map[string]any   `json:"additional_config,omitempty"` // 入站附加配置
	GroupKey          string           `json:"-"`                           // WebUI 虚拟群原始 group_id
	GroupName         string           `json:"-"`                           // WebUI 虚拟群名称
}

// FileInfo WebUI 文件附件，携带 filesvc 资产引用
type FileInfo struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
	FileRef  string `json:"file_ref,omitempty"`
}

// ImageInfo 图片信息
type ImageInfo struct {
	URL             string `json:"url"`
	File            string `json:"file"`
	SubType         int    `json:"sub_type"` // 0普通图片 1表情包
	Desc            string `json:"desc,omitempty"`
	RecognitionHash string `json:"-"`
}

// VoiceInfo 语音信息；File 保留平台原始文件标识，URL 为可直接下载的媒体地址
type VoiceInfo struct {
	File string `json:"file,omitempty"`
	URL  string `json:"url,omitempty"`
}

// VoiceSource 返回可用于解析语音字节的媒体引用，URL 优先
func (v VoiceInfo) VoiceSource() string {
	if value := strings.TrimSpace(v.URL); value != "" {
		return value
	}
	return strings.TrimSpace(v.File)
}

// VideoInfo 视频信息
type VideoInfo struct {
	URL  string `json:"url"`
	File string `json:"file"`
}

// FaceInfo 表情信息
type FaceInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ReplyInfo 回复信息
type ReplyInfo struct {
	MessageID         int64  `json:"message_id"`
	ExternalMessageID string `json:"external_message_id,omitempty"` // 非 QQ 平台被回复消息原始字符串 ID
	Content           string `json:"content,omitempty"`             // 被回复消息内容
	SenderID          int64  `json:"sender_id,omitempty"`           // 被回复消息发送者 ID
	ExternalSenderID  string `json:"external_sender_id,omitempty"`  // 非 QQ 平台被回复发送者原始字符串 ID
	Nickname          string `json:"nickname,omitempty"`            // 被回复消息发送者原始昵称
	GroupCard         string `json:"group_card,omitempty"`
}

// CardMessage 卡片消息解析结果
type CardMessage struct {
	Title string `json:"title"` // 标题
	Desc  string `json:"desc"`  // 描述
	URL   string `json:"url"`   // 链接
}

// Format 格式化卡片消息为可读文本
func (c *CardMessage) Format() string {
	if c.URL != "" {
		return fmt.Sprintf("[卡片:%s - %s 链接:%s]", c.Title, c.Desc, c.URL)
	}
	if c.Desc != "" {
		return fmt.Sprintf("[卡片:%s - %s]", c.Title, c.Desc)
	}
	return fmt.Sprintf("[卡片:%s]", c.Title)
}

// GroupNotice 群公告
type GroupNotice struct {
	NoticeID    string `json:"notice_id"`
	SenderID    int64  `json:"sender_id"`
	PublishTime int64  `json:"publish_time"`
	Content     string `json:"content"`
}

// EssenceMessage 群精华消息
type EssenceMessage struct {
	MessageID    int64  `json:"message_id"`
	SenderNick   string `json:"sender_nick"`
	OperatorNick string `json:"operator_nick"`
	OperatorTime int64  `json:"operator_time"`
	Content      string `json:"content"`
}

// GroupInfo 群信息
type GroupInfo struct {
	GroupID        int64  `json:"group_id"`
	GroupName      string `json:"group_name"`
	MemberCount    int    `json:"member_count"`
	MaxMemberCount int    `json:"max_member_count"`
}

// GroupMemberInfo 群成员信息
type GroupMemberInfo struct {
	GroupID      int64  `json:"group_id"`
	UserID       int64  `json:"user_id"`
	Nickname     string `json:"nickname"`
	Card         string `json:"card"`
	Role         string `json:"role"` // 群主（owner）、管理员（admin）或成员（member）
	JoinTime     int64  `json:"join_time"`
	LastSentTime int64  `json:"last_sent_time"`
	Level        string `json:"level"`
	Title        string `json:"title"` // 专属头衔
}
