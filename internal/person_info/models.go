package personinfo

import (
	"bytes"
	"encoding/json"
	"strings"

	"maibot-go/internal/person_info/schema"
)

// PersonInfo 复用 schema 子包的模型定义
// 类型别名保持既有调用方不变
type PersonInfo = schema.PersonInfo

// GroupCardEntry 群名片 JSON 元素，键名与 MaiBot 一致
type GroupCardEntry struct {
	GroupID       string `json:"group_id"`
	GroupCardname string `json:"group_cardname"`
}

// decodeGroupCards 对齐 parse_group_cardname_json：逐条 trim，缺 group_id 或名片则丢弃
func decodeGroupCards(raw *string) []GroupCardEntry {
	if raw == nil || *raw == "" {
		return nil
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(*raw), &items); err != nil {
		return nil
	}
	out := make([]GroupCardEntry, 0, len(items))
	for _, item := range items {
		entry := GroupCardEntry{
			GroupID:       anyText(item["group_id"]),
			GroupCardname: anyText(item["group_cardname"]),
		}
		if entry.GroupID == "" || entry.GroupCardname == "" {
			continue
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// encodeGroupCards 对齐 dump_group_cardname_records：空列表写 "[]"，不转义非 ASCII 与 HTML 字符
func encodeGroupCards(items []GroupCardEntry) (string, error) {
	clean := make([]GroupCardEntry, 0, len(items))
	for _, item := range items {
		groupID := strings.TrimSpace(item.GroupID)
		card := strings.TrimSpace(item.GroupCardname)
		if groupID == "" || card == "" {
			continue
		}
		clean = append(clean, GroupCardEntry{GroupID: groupID, GroupCardname: card})
	}
	return marshalJSONText(clean)
}

// decodeMemoryPoints 对齐 Person.load_from_database：只过滤 None，其余原样保留
func decodeMemoryPoints(raw *string) []string {
	if raw == nil || *raw == "" {
		return nil
	}
	var items []any
	if err := json.Unmarshal([]byte(*raw), &items); err != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// encodeMemoryPoints 对齐 Person.sync_to_database：空列表写 "[]"，不转义非 ASCII
func encodeMemoryPoints(items []string) (string, error) {
	if items == nil {
		items = []string{}
	}
	return marshalJSONText(items)
}

// jsonTextPtr 返回 JSON 文本列的可空指针
func jsonTextPtr(text string) *string {
	return &text
}

// marshalJSONText 输出与 Python json.dumps(ensure_ascii=False) 一致的文本
func marshalJSONText(value any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func anyText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case nil:
		return ""
	default:
		raw, err := marshalJSONText(typed)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(raw)
	}
}
