package platformio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// DecodeWireObject 保留 JSON 整数与浮点数的区别。超出 int64 的整数仍保留原始数字文本。
func DecodeWireObject(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("JSON 响应必须为对象")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("JSON 对象后存在额外内容")
	}
	normalizeWireNumber(object)
	return object, nil
}

func normalizeWireNumber(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeWireNumber(item)
		}
	case []any:
		for index, item := range typed {
			typed[index] = normalizeWireNumber(item)
		}
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return n
		}
		if strings.ContainsAny(string(typed), ".eE") {
			if n, err := typed.Float64(); err == nil {
				return n
			}
		}
	}
	return value
}
