package maimmsg

import (
	"encoding/json"
	"fmt"
)

// Seg 消息片段，对齐 maim_message.Seg
type Seg struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// SegText 构造 text 段
func SegText(text string) Seg {
	raw, _ := json.Marshal(text)
	return Seg{Type: "text", Data: raw}
}

// Text 解析 text 段内容
func (s Seg) Text() (string, error) {
	if s.Type != "text" {
		return "", fmt.Errorf("maimmsg: 段类型不是 text")
	}
	var text string
	if err := json.Unmarshal(s.Data, &text); err != nil {
		return "", err
	}
	return text, nil
}

// SegList 解析 seglist 嵌套段
func (s Seg) SegList() ([]Seg, error) {
	if s.Type != "seglist" {
		return nil, fmt.Errorf("maimmsg: 段类型不是 seglist")
	}
	var items []Seg
	if err := json.Unmarshal(s.Data, &items); err != nil {
		return nil, err
	}
	return items, nil
}
