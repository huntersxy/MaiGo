package maimmsg

import "encoding/json"

// MessageBase 对齐 maim_message.MessageBase（WS/API 载荷根对象）
//
// AccountID / Scope 是适配器在消息顶层声明的路由归属。Python 侧
// MessageBase.from_dict 不保留这两个字段，但 RouteKeyFactory 会从原始字典读取，
// 因此 Go 需要显式接收，否则顶层声明归属的适配器会失去账号/作用域信息。
type MessageBase struct {
	MessageInfo    BaseMessageInfo `json:"message_info"`
	MessageSegment Seg             `json:"message_segment"`
	RawMessage     *string         `json:"raw_message,omitempty"`
	AccountID      string          `json:"account_id,omitempty"`
	Scope          string          `json:"scope,omitempty"`
}

// UnmarshalJSON 接受字符串或数字形式的顶层 account_id/scope
func (m *MessageBase) UnmarshalJSON(data []byte) error {
	var raw struct {
		MessageInfo    BaseMessageInfo `json:"message_info"`
		MessageSegment Seg             `json:"message_segment"`
		RawMessage     *string         `json:"raw_message"`
		AccountID      json.RawMessage `json:"account_id"`
		Scope          json.RawMessage `json:"scope"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.MessageInfo = raw.MessageInfo
	m.MessageSegment = raw.MessageSegment
	m.RawMessage = raw.RawMessage
	m.AccountID = flexText(raw.AccountID)
	m.Scope = flexText(raw.Scope)
	return nil
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
