package memory

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// MessageAdditionalConfig 消息附加配置，落库为 jsonb，语义对齐 Python Messages.additional_config
type MessageAdditionalConfig map[string]any

// Value 空配置按 NULL 落库，与 Python additional_config 为空时写 None 对齐
func (c MessageAdditionalConfig) Value() (driver.Value, error) {
	if len(c) == 0 {
		return nil, nil
	}
	return json.Marshal(c)
}

// Scan 兼容 PostgreSQL jsonb 的 []byte 与 SQLite TEXT 的 string
func (c *MessageAdditionalConfig) Scan(src any) error {
	if src == nil {
		*c = nil
		return nil
	}
	var raw []byte
	switch value := src.(type) {
	case []byte:
		raw = value
	case string:
		raw = []byte(value)
	default:
		return fmt.Errorf("无法解析消息附加配置: %T", src)
	}
	if len(raw) == 0 {
		*c = nil
		return nil
	}
	return json.Unmarshal(raw, c)
}
