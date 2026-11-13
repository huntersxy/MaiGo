package maimmsg

import "encoding/json"

// MessageDim 新 API 消息维度：API Key 与平台标识
type MessageDim struct {
	APIKey   string `json:"api_key"`
	Platform string `json:"platform"`
}

// APIBaseMessageInfo 新 API 信封的消息元信息，对齐 maim_message.api_message_base.BaseMessageInfo
// 与旧版 BaseMessageInfo 的差异：platform/message_id/time 必填，群/用户信息收敛到 sender/receiver
type APIBaseMessageInfo struct {
	Platform         string         `json:"platform"`
	MessageID        string         `json:"message_id"`
	Time             float64        `json:"time"`
	FormatInfo       *FormatInfo    `json:"format_info,omitempty"`
	TemplateInfo     *TemplateInfo  `json:"template_info,omitempty"`
	AdditionalConfig map[string]any `json:"additional_config,omitempty"`
	SenderInfo       *SenderInfo    `json:"sender_info,omitempty"`
	ReceiverInfo     *ReceiverInfo  `json:"receiver_info,omitempty"`
}

// APIMessageBase 新 API 消息根对象，对齐 maim_message.APIMessageBase
type APIMessageBase struct {
	MessageInfo    APIBaseMessageInfo `json:"message_info"`
	MessageSegment Seg                `json:"message_segment"`
	MessageDim     MessageDim         `json:"message_dim"`
}

// GetAPIKey 返回消息所属 API Key
func (m APIMessageBase) GetAPIKey() string { return m.MessageDim.APIKey }

// GetPlatform 返回消息维度中的平台标识
func (m APIMessageBase) GetPlatform() string { return m.MessageDim.Platform }

// GetMessagePlatform 返回消息元信息中的平台标识
func (m APIMessageBase) GetMessagePlatform() string { return m.MessageInfo.Platform }

// GetMessageID 返回消息 ID
func (m APIMessageBase) GetMessageID() string { return m.MessageInfo.MessageID }

// GetMessageTime 返回消息时间戳（秒）
func (m APIMessageBase) GetMessageTime() float64 { return m.MessageInfo.Time }

// SetMessageDim 设置消息维度
func (m *APIMessageBase) SetMessageDim(apiKey, platform string) {
	m.MessageDim = MessageDim{APIKey: apiKey, Platform: platform}
}

// ToMap 转换为可 JSON 序列化的字典
func (m APIMessageBase) ToMap() map[string]any {
	info := map[string]any{
		"platform":   m.MessageInfo.Platform,
		"message_id": m.MessageInfo.MessageID,
		"time":       m.MessageInfo.Time,
	}
	if m.MessageInfo.FormatInfo != nil {
		info["format_info"] = m.MessageInfo.FormatInfo
	}
	if m.MessageInfo.TemplateInfo != nil {
		info["template_info"] = m.MessageInfo.TemplateInfo
	}
	if len(m.MessageInfo.AdditionalConfig) > 0 {
		info["additional_config"] = m.MessageInfo.AdditionalConfig
	}
	if m.MessageInfo.SenderInfo != nil {
		info["sender_info"] = m.MessageInfo.SenderInfo
	}
	if m.MessageInfo.ReceiverInfo != nil {
		info["receiver_info"] = m.MessageInfo.ReceiverInfo
	}
	return map[string]any{
		"message_info":    info,
		"message_segment": m.MessageSegment,
		"message_dim":     m.MessageDim,
	}
}

// FromMap 从字典解析新 API 消息；缺失字段按 Python from_dict 行为保留零值
func (m *APIMessageBase) FromMap(payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, m)
}

// ToJSON 序列化为 JSON 字节
func (m APIMessageBase) ToJSON() ([]byte, error) {
	return json.Marshal(m)
}

// UnmarshalAPIMessage 解析新 API 消息 JSON
func UnmarshalAPIMessage(data []byte) (APIMessageBase, error) {
	var msg APIMessageBase
	err := json.Unmarshal(data, &msg)
	return msg, err
}
