package snowluma

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// decodeJSONPayload 按 Python json.loads 语义解码对端帧：
// 整数字面量保持 int64，只有带小数或指数的才是 float64。
// 默认 json.Unmarshal 会把所有数字变 float64，插件 str(user_id) 就多出 ".0"。
func decodeJSONPayload(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("JSON 帧尾存在多余内容")
	}
	normalized, _ := normalizeJSONNumbers(payload).(map[string]any)
	return normalized, nil
}

func normalizeJSONNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeJSONNumbers(item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = normalizeJSONNumbers(item)
		}
		return typed
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if float, err := typed.Float64(); err == nil {
			return float
		}
		return typed.String()
	default:
		return value
	}
}
