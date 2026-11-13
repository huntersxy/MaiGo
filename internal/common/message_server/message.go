package maimmsg

import "encoding/json"

// MessageBase 对齐 maim_message.MessageBase（WS/API 载荷根对象）
type MessageBase struct {
	MessageInfo    BaseMessageInfo `json:"message_info"`
	MessageSegment Seg             `json:"message_segment"`
	RawMessage     *string         `json:"raw_message,omitempty"`
}

// MarshalJSON 与 Python to_dict 一致：省略 nil 字段由 encoding/json 默认处理
func (m MessageBase) MarshalJSON() ([]byte, error) {
	type alias MessageBase
	return json.Marshal(alias(m))
}

// UnmarshalMessage 从 JSON 字节解析
func UnmarshalMessage(data []byte) (MessageBase, error) {
	var msg MessageBase
	err := json.Unmarshal(data, &msg)
	return msg, err
}

// ToJSON 序列化为 JSON
func (m MessageBase) ToJSON() ([]byte, error) {
	return json.Marshal(m)
}
