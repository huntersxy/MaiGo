package maimmsg

// GroupInfo 群组信息
type GroupInfo struct {
	Platform  string `json:"platform,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

// UserInfo 用户信息
type UserInfo struct {
	Platform     string `json:"platform,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	UserNickname string `json:"user_nickname,omitempty"`
	UserCardname string `json:"user_cardname,omitempty"`
}

// FormatInfo 格式能力
type FormatInfo struct {
	ContentFormat []string `json:"content_format,omitempty"`
	AcceptFormat  []string `json:"accept_format,omitempty"`
}

// TemplateInfo 模板信息
type TemplateInfo struct {
	TemplateItems   map[string]string `json:"template_items,omitempty"`
	TemplateName    map[string]string `json:"template_name,omitempty"`
	TemplateDefault *bool             `json:"template_default,omitempty"`
}

// InfoBase 发送者/接收者共用嵌套
type InfoBase struct {
	GroupInfo *GroupInfo `json:"group_info,omitempty"`
	UserInfo  *UserInfo  `json:"user_info,omitempty"`
}

// SenderInfo 发送者
type SenderInfo struct {
	InfoBase
}

// ReceiverInfo 接收者
type ReceiverInfo struct {
	InfoBase
}

// BaseMessageInfo 对齐 maim_message.BaseMessageInfo
type BaseMessageInfo struct {
	Platform         string         `json:"platform,omitempty"`
	MessageID        string         `json:"message_id,omitempty"`
	Time             *float64       `json:"time,omitempty"`
	GroupInfo        *GroupInfo     `json:"group_info,omitempty"`
	UserInfo         *UserInfo      `json:"user_info,omitempty"`
	FormatInfo       *FormatInfo    `json:"format_info,omitempty"`
	TemplateInfo     *TemplateInfo  `json:"template_info,omitempty"`
	AdditionalConfig map[string]any `json:"additional_config,omitempty"`
	SenderInfo       *SenderInfo    `json:"sender_info,omitempty"`
	ReceiverInfo     *ReceiverInfo  `json:"receiver_info,omitempty"`
}
