package httputil

import (
	"io"
	"net/http"
	"strconv"
	"strings"
)

// maxJSONBodyBytes 请求体上限，对齐 webui 侧既有约束
const maxJSONBodyBytes = 8 << 20

// ReadJSONBody 读取请求体；失败时已写出错误响应并返回 ok=false
func ReadJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r == nil || r.Body == nil {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "缺少请求体"})
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes))
	if err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "读取请求体失败"})
		return nil, false
	}
	return raw, true
}

// QueryInt 读取整型查询参数，缺失或非法时返回默认值
//
// 对齐 Python 侧 Query(default=..., ge=..., le=...) 的钳制语义。
func QueryInt(r *http.Request, key string, def, min, max int) int {
	if r == nil {
		return def
	}
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// ParsePathID 解析路径上的正整数 ID；失败时写出 422 并返回 ok=false
func ParsePathID(w http.ResponseWriter, raw string) (uint, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		WriteValidationError(w, "ID 必须是正整数")
		return 0, false
	}
	return uint(value), true
}

// NullableString 把 *string 转成可 JSON 序列化的值：nil → null
func NullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
