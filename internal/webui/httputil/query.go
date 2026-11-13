package httputil

import (
	"net/http"
	"strconv"
	"strings"
)

// OptionalQueryBool 解析可选的布尔查询参数
//
// 返回 (值, 是否出现, 错误信息)。参数缺失时 present=false；
// 出现但无法解析时返回错误字符串，由调用方决定如何写响应。
func OptionalQueryBool(r *http.Request, key string) (bool, bool, string) {
	if r == nil {
		return false, false, ""
	}
	values, ok := r.URL.Query()[key]
	if !ok || len(values) == 0 {
		return false, false, ""
	}
	raw := strings.TrimSpace(values[len(values)-1])
	if raw == "" {
		return false, false, ""
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, true, "Input should be a valid boolean"
	}
	return parsed, true, ""
}

// OptionalQueryBoolPtr 解析可选布尔查询参数并写出校验错误
func OptionalQueryBoolPtr(w http.ResponseWriter, r *http.Request, key string) (*bool, bool) {
	value, present, verr := OptionalQueryBool(r, key)
	if verr != "" {
		WriteValidationError(w, verr)
		return nil, false
	}
	if !present {
		return nil, true
	}
	return &value, true
}
