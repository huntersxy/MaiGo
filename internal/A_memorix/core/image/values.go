package imagememory

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// rowString 读取行字典字符串列，缺失或 nil 返回空串
func rowString(row map[string]any, key string) string {
	if row == nil {
		return ""
	}
	value, ok := row[key]
	if !ok || value == nil {
		return ""
	}
	switch item := value.(type) {
	case string:
		return item
	case []byte:
		return string(item)
	default:
		return fmt.Sprint(item)
	}
}

// rowInt64 读取行字典整数列
func rowInt64(row map[string]any, key string) int64 {
	if row == nil {
		return 0
	}
	return int64(rowFloat64(row, key))
}

// rowFloat64 读取行字典浮点列
func rowFloat64(row map[string]any, key string) float64 {
	if row == nil {
		return 0
	}
	value, ok := row[key]
	if !ok || value == nil {
		return 0
	}
	number, ok := numberValue(value)
	if !ok {
		return 0
	}
	return number
}

// rowFloatPtr 读取行字典可空浮点列
func rowFloatPtr(row map[string]any, key string) *float64 {
	if row == nil {
		return nil
	}
	value, ok := row[key]
	if !ok || value == nil {
		return nil
	}
	number, ok := numberValue(value)
	if !ok {
		return nil
	}
	return &number
}

// pythonStr 对齐 Python str(value) 的常用分支
func pythonStr(value any) string {
	switch item := value.(type) {
	case nil:
		return "None"
	case string:
		return item
	case []byte:
		return string(item)
	case bool:
		if item {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(item)
	}
}

// stringOr 对齐 Python 的 str(value or default)
func stringOr(value any, fallback string) string {
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	if value != nil && truthy(value) {
		return pythonStr(value)
	}
	return fallback
}

// truthy 对齐 Python bool() 的常用分支
func truthy(value any) bool {
	if value == nil {
		return false
	}
	switch item := value.(type) {
	case bool:
		return item
	case string:
		return item != ""
	case []byte:
		return len(item) > 0
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() != 0
	case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
		return reflected.Len() > 0
	case reflect.Ptr, reflect.Interface:
		return !reflected.IsNil()
	}
	return true
}

// numberValue 把数值归一为 float64
func numberValue(value any) (float64, bool) {
	switch item := value.(type) {
	case int:
		return float64(item), true
	case int8:
		return float64(item), true
	case int16:
		return float64(item), true
	case int32:
		return float64(item), true
	case int64:
		return float64(item), true
	case uint:
		return float64(item), true
	case uint8:
		return float64(item), true
	case uint16:
		return float64(item), true
	case uint32:
		return float64(item), true
	case uint64:
		return float64(item), true
	case float32:
		return float64(item), true
	case float64:
		return item, true
	case bool:
		if item {
			return 1, true
		}
		return 0, true
	case json.Number:
		number, err := item.Float64()
		if err != nil {
			return 0, false
		}
		return number, true
	}
	return 0, false
}

// valueToInt 对齐 Python int(value) 的常用分支
func valueToInt(value any) (int, error) {
	if value == nil {
		return 0, fmt.Errorf("无法把 nil 转为整数")
	}
	switch item := value.(type) {
	case string:
		number, err := strconv.Atoi(pythonStrip(item))
		if err != nil {
			return 0, fmt.Errorf("无法把字符串 %q 转为整数", item)
		}
		return number, nil
	case json.Number:
		if number, err := item.Int64(); err == nil {
			return int(number), nil
		}
		parsed, err := item.Float64()
		if err != nil {
			return 0, err
		}
		return int(parsed), nil
	case float32:
		if math.IsNaN(float64(item)) || math.IsInf(float64(item), 0) {
			return 0, fmt.Errorf("无法把非有限浮点数转为整数")
		}
		return int(item), nil
	case float64:
		if math.IsNaN(item) || math.IsInf(item, 0) {
			return 0, fmt.Errorf("无法把非有限浮点数转为整数")
		}
		return int(item), nil
	}
	number, ok := numberValue(value)
	if !ok {
		return 0, fmt.Errorf("无法把类型 %T 转为整数", value)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("无法把非有限浮点数转为整数")
	}
	return int(number), nil
}

// valueToFloat 对齐 Python float(value) 的常用分支
func valueToFloat(value any) (float64, error) {
	if value == nil {
		return 0, fmt.Errorf("无法把 nil 转为浮点数")
	}
	if text, ok := value.(string); ok {
		number, err := strconv.ParseFloat(pythonStrip(text), 64)
		if err != nil {
			return 0, fmt.Errorf("无法把字符串 %q 转为浮点数", text)
		}
		return number, nil
	}
	number, ok := numberValue(value)
	if !ok {
		return 0, fmt.Errorf("无法把类型 %T 转为浮点数", value)
	}
	return number, nil
}

// pythonStrip 对齐 Python str.strip 的空白集合
func pythonStrip(text string) string {
	return strings.TrimFunc(text, func(symbol rune) bool {
		if unicode.IsSpace(symbol) {
			return true
		}
		return symbol >= 0x1c && symbol <= 0x1f
	})
}

// round2 对齐 Python round(value, 2)
func round2(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 2, 64), 64)
	if err != nil {
		return value
	}
	return rounded
}

// nowTimestamp 对齐 Python datetime.now().timestamp() 的浮点秒
func nowTimestamp() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// randomHex32 生成 32 位十六进制租约令牌
func randomHex32() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		sum := sha256.Sum256([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
		return hex.EncodeToString(sum[:16])
	}
	return hex.EncodeToString(buffer)
}

// asStringMap 尽量把值归一为字符串键对象
func asStringMap(value any) (map[string]any, bool) {
	switch item := value.(type) {
	case nil:
		return map[string]any{}, true
	case map[string]any:
		return item, true
	case map[string]string:
		converted := make(map[string]any, len(item))
		for key, entry := range item {
			converted[key] = entry
		}
		return converted, true
	}
	return nil, false
}

// mapFromAny 归一字符串键对象，失败返回空对象
func mapFromAny(value any) map[string]any {
	converted, ok := asStringMap(value)
	if !ok {
		return map[string]any{}
	}
	return converted
}

// fingerprintCopy 复制指纹对象
func fingerprintCopy(fingerprint map[string]any) map[string]any {
	copied := make(map[string]any, len(fingerprint))
	for key, value := range fingerprint {
		copied[key] = value
	}
	return copied
}

// mergeMaps 把 source 写入 destination
func mergeMaps(destination map[string]any, source map[string]any) {
	for key, value := range source {
		destination[key] = value
	}
}

// valuesEqual 比较两个值，数值按数值语义比较
func valuesEqual(left any, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftMap, leftIsMap := asStringMap(left)
	rightMap, rightIsMap := asStringMap(right)
	if leftIsMap || rightIsMap {
		if !leftIsMap || !rightIsMap || len(leftMap) != len(rightMap) {
			return false
		}
		for key, value := range leftMap {
			other, ok := rightMap[key]
			if !ok || !valuesEqual(value, other) {
				return false
			}
		}
		return true
	}
	leftNumber, leftIsNumber := numberValue(left)
	rightNumber, rightIsNumber := numberValue(right)
	if leftIsNumber || rightIsNumber {
		return leftIsNumber && rightIsNumber && leftNumber == rightNumber
	}
	return reflect.DeepEqual(left, right)
}

// fingerprintEqual 比较两个指纹对象
func fingerprintEqual(left map[string]any, right map[string]any) bool {
	return valuesEqual(left, right)
}

// isFiniteVector 判断向量是否全部为有限数值
func isFiniteVector(vector []float32) bool {
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}
