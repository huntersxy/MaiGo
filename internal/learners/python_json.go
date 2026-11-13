package learners

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// marshalPythonJSON 复刻 Python json.dumps(value, ensure_ascii=False, sort_keys=True)
// 的默认输出：键递归排序、UTF-8 原样、分隔符为 ", " 与 ": "
// 该格式用于表达向量 embedding profile marker，必须与 Python 字节级一致
func marshalPythonJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writePythonJSONValue(&buffer, value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// pythonJSONSHA256 返回 Python 序列化结果的 sha256 十六进制摘要
func pythonJSONSHA256(value any) (string, error) {
	encoded, err := marshalPythonJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func writePythonJSONValue(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case string:
		return writePythonJSONString(buffer, typed)
	case int:
		buffer.WriteString(strconv.FormatInt(int64(typed), 10))
	case int8:
		buffer.WriteString(strconv.FormatInt(int64(typed), 10))
	case int16:
		buffer.WriteString(strconv.FormatInt(int64(typed), 10))
	case int32:
		buffer.WriteString(strconv.FormatInt(int64(typed), 10))
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
	case uint:
		buffer.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint8:
		buffer.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint16:
		buffer.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint32:
		buffer.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint64:
		buffer.WriteString(strconv.FormatUint(typed, 10))
	case float32:
		buffer.WriteString(pythonJSONFloat(float64(typed), 32))
	case float64:
		buffer.WriteString(pythonJSONFloat(typed, 64))
	case json.Number:
		text := strings.TrimSpace(typed.String())
		if text == "" {
			return fmt.Errorf("json.Number 为空")
		}
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return fmt.Errorf("json.Number 非法: %s", text)
		}
		buffer.WriteString(text)
	case []any:
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteString(", ")
			}
			if err := writePythonJSONValue(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	case []string:
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteString(", ")
			}
			if err := writePythonJSONString(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteString(", ")
			}
			if err := writePythonJSONString(buffer, key); err != nil {
				return err
			}
			buffer.WriteString(": ")
			if err := writePythonJSONValue(buffer, typed[key]); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	case map[string]string:
		values := make(map[string]any, len(typed))
		for key, item := range typed {
			values[key] = item
		}
		return writePythonJSONValue(buffer, values)
	default:
		return fmt.Errorf("Python JSON 序列化不支持类型 %T", value)
	}
	return nil
}

func writePythonJSONString(buffer *bytes.Buffer, value string) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	text := strings.TrimSuffix(encoded.String(), "\n")
	// Go 总会转义 U+2028/U+2029，Python ensure_ascii=False 原样输出
	text = strings.ReplaceAll(text, "\\u2028", "\u2028")
	text = strings.ReplaceAll(text, "\\u2029", "\u2029")
	buffer.WriteString(text)
	return nil
}

func pythonJSONFloat(value float64, bits int) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	text := strconv.FormatFloat(value, 'g', -1, bits)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}
