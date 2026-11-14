package maimmsg

import (
	"encoding/json"
)

// GroupInfo 群组信息
//
// ID 字段用宽松解析：数字型 group_id 的适配器不会被整帧拒绝，
// 与 Python bot.py 把 group_id 强制 str() 的行为一致。
type GroupInfo struct {
	Platform  string `json:"platform,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

// UnmarshalJSON 接受字符串或数字形式的 group_id
func (g *GroupInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		Platform  string          `json:"platform"`
		GroupID   json.RawMessage `json:"group_id"`
		GroupName string          `json:"group_name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	g.Platform = raw.Platform
	g.GroupName = raw.GroupName
	g.GroupID = flexText(raw.GroupID)
	return nil
}

// UserInfo 用户信息
type UserInfo struct {
	Platform     string `json:"platform,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	UserNickname string `json:"user_nickname,omitempty"`
	UserCardname string `json:"user_cardname,omitempty"`
}

// UnmarshalJSON 接受字符串或数字形式的 user_id
func (u *UserInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		Platform     string          `json:"platform"`
		UserID       json.RawMessage `json:"user_id"`
		UserNickname string          `json:"user_nickname"`
		UserCardname string          `json:"user_cardname"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	u.Platform = raw.Platform
	u.UserNickname = raw.UserNickname
	u.UserCardname = raw.UserCardname
	u.UserID = flexText(raw.UserID)
	return nil
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

// UnmarshalJSON 接受字符串或数字形式的 message_id 与 time
func (i *BaseMessageInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		Platform         string          `json:"platform"`
		MessageID        json.RawMessage `json:"message_id"`
		Time             json.RawMessage `json:"time"`
		GroupInfo        *GroupInfo      `json:"group_info"`
		UserInfo         *UserInfo       `json:"user_info"`
		FormatInfo       *FormatInfo     `json:"format_info"`
		TemplateInfo     *TemplateInfo   `json:"template_info"`
		AdditionalConfig map[string]any  `json:"additional_config"`
		SenderInfo       *SenderInfo     `json:"sender_info"`
		ReceiverInfo     *ReceiverInfo   `json:"receiver_info"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	i.Platform = raw.Platform
	i.MessageID = flexText(raw.MessageID)
	i.GroupInfo = raw.GroupInfo
	i.UserInfo = raw.UserInfo
	i.FormatInfo = raw.FormatInfo
	i.TemplateInfo = raw.TemplateInfo
	i.AdditionalConfig = raw.AdditionalConfig
	i.SenderInfo = raw.SenderInfo
	i.ReceiverInfo = raw.ReceiverInfo
	i.Time = nil
	if len(raw.Time) > 0 {
		var value flexFloat
		if err := json.Unmarshal(raw.Time, &value); err == nil && float64(value) > 0 {
			converted := float64(value)
			i.Time = &converted
		}
	}
	return nil
}
