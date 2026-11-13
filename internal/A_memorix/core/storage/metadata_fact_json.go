package storage

// A_memorix 事实账本的确定性 JSON 与稳定哈希，对齐 Python json.dumps(sort_keys=True)
// claim_id 与 conflict_group 都由哈希决定，编码必须与 Python 逐字节一致

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// factPyJSONString 对齐 Python json.dumps 的字符串转义（ensure_ascii=False）
func factPyJSONString(text string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			builder.WriteString("\\\"")
		case '\\':
			builder.WriteString("\\\\")
		case '\b':
			builder.WriteString("\\b")
		case '\f':
			builder.WriteString("\\f")
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		case '\t':
			builder.WriteString("\\t")
		default:
			if r < 0x20 {
				builder.WriteString(fmt.Sprintf("\\u%04x", r))
				continue
			}
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

// factWritePyJSON 按 Python json.dumps 规则递归编码，键固定升序
func factWritePyJSON(builder *strings.Builder, value any, itemSep, keySep string) error {
	switch v := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if v {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		builder.WriteString(factPyJSONString(v))
	case []byte:
		builder.WriteString(factPyJSONString(string(v)))
	case int:
		builder.WriteString(strconv.Itoa(v))
	case int64:
		builder.WriteString(strconv.FormatInt(v, 10))
	case json.Number:
		if _, err := strconv.ParseFloat(v.String(), 64); err != nil {
			return fmt.Errorf("无法编码的 JSON 数值 %q", v.String())
		}
		builder.WriteString(v.String())
	case float64:
		switch {
		case math.IsNaN(v):
			builder.WriteString("NaN")
		case math.IsInf(v, 1):
			builder.WriteString("Infinity")
		case math.IsInf(v, -1):
			builder.WriteString("-Infinity")
		default:
			builder.WriteString(factPyFloatRepr(v))
		}
	case []string:
		builder.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				builder.WriteString(itemSep)
			}
			builder.WriteString(factPyJSONString(item))
		}
		builder.WriteByte(']')
	case []any:
		builder.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				builder.WriteString(itemSep)
			}
			if err := factWritePyJSON(builder, item, itemSep, keySep); err != nil {
				return err
			}
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				builder.WriteString(itemSep)
			}
			builder.WriteString(factPyJSONString(key))
			builder.WriteString(keySep)
			if err := factWritePyJSON(builder, v[key], itemSep, keySep); err != nil {
				return err
			}
		}
		builder.WriteByte('}')
	default:
		return fmt.Errorf("无法编码的 JSON 类型 %T", value)
	}
	return nil
}

// factPyJSON 对齐 Python json.dumps(value, ensure_ascii=False, sort_keys=True)
func factPyJSON(value any, itemSep, keySep string) (string, error) {
	var builder strings.Builder
	if err := factWritePyJSON(&builder, value, itemSep, keySep); err != nil {
		return "", err
	}
	return builder.String(), nil
}

// factCompactJSON 对齐 Python json.dumps(separators=(",", ":"), sort_keys=True)
func factCompactJSON(value any) (string, error) {
	return factPyJSON(value, ",", ":")
}

// factSortedJSONDumps 对齐 Python json.dumps(value, ensure_ascii=False, sort_keys=True)
func factSortedJSONDumps(value any) (string, error) {
	return factPyJSON(value, ", ", ": ")
}

// factStableHash 对齐 Python _stable_hash：sha256 十六进制小写
func factStableHash(payload map[string]any) (string, error) {
	encoded, err := factCompactJSON(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:]), nil
}

// factClaimIdentity 对齐 Python _claim_identity
func factClaimIdentity(scopeType, scopeID, factKey, valueNormalized, polarity string) (string, error) {
	return factStableHash(map[string]any{
		"scope_type": scopeType,
		"scope_id":   scopeID,
		"fact_key":   factKey,
		"value":      valueNormalized,
		"polarity":   polarity,
	})
}

// factConflictGroup 对齐 Python _conflict_group：只有 set 基数把值纳入冲突组
func factConflictGroup(scopeType, scopeID, factKey, valueNormalized, cardinality string) (string, error) {
	payload := map[string]any{
		"scope_type": scopeType,
		"scope_id":   scopeID,
		"fact_key":   factKey,
	}
	if cardinality == "set" {
		payload["value"] = valueNormalized
	}
	return factStableHash(payload)
}
