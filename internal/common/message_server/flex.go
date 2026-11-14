package maimmsg

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// flexString 宽松字符串：接受 JSON 字符串、数字与布尔
//
// 对齐 Python maim_message 的入站处理：BaseMessageInfo.from_dict 原样保留字段，
// 随后 bot.py 会把 group_id/user_id 强制 str()，数字 ID 的适配器仍可正常入站。
// Go 若用 string 字段直接解析，数字 ID 会让整帧反序列化失败并被丢弃。
type flexString string

func (s *flexString) UnmarshalJSON(data []byte) error {
	*s = flexString(flexText(data))
	return nil
}

// flexFloat 宽松浮点：接受数字与数字字符串
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*f = 0
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			*f = 0
			return nil
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			*f = 0
			return nil
		}
		*f = flexFloat(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		*f = 0
		return nil
	}
	value, err := number.Float64()
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexFloat(value)
	return nil
}

// flexText 把任意 JSON 标量转成字符串；对象与数组按空串处理
func flexText(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return ""
		}
		return text
	case '{', '[':
		return ""
	}
	if string(trimmed) == "true" {
		return "true"
	}
	if string(trimmed) == "false" {
		return "false"
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return ""
	}
	return number.String()
}
