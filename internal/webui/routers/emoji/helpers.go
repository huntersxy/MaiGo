package emoji

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"strconv"
	"strings"

	"MaiGo/internal/webui/authutil"
	"MaiGo/internal/webui/httputil"
)

// parseQueryIntStrict 严格解析整型查询参数：缺失返回 present=false，
// 非法返回 error（不静默回落默认值）
func parseQueryIntStrict(r *http.Request, key string) (int, bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, false, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true, errInvalidInt(key)
	}
	return value, true, nil
}

type invalidIntError string

func (e invalidIntError) Error() string { return string(e) + " 必须是整数" }

func errInvalidInt(key string) error { return invalidIntError(key) }

// optionalQueryBool 解析可选布尔查询参数
func optionalQueryBool(r *http.Request, key string) (bool, bool, error) {
	value, present, verr := httputil.OptionalQueryBool(r, key)
	if verr != "" {
		return false, present, invalidBoolError(verr)
	}
	return value, present, nil
}

type invalidBoolError string

func (e invalidBoolError) Error() string { return string(e) }

// queryPagination 解析 page/page_size，越界按 FastAPI 语义返回 422
//
// 对齐父包 api_shared.go：page 默认 1、page_size 默认 defaultSize，
// 两者都必须是正整数；非法时一次性写出 detail 数组。
func queryPagination(w http.ResponseWriter, r *http.Request, defaultSize int) (int, int, bool) {
	page := 1
	if value, present, err := parseQueryIntStrict(r, "page"); err != nil {
		writeValidationDetail(w, "page", "Input should be a valid integer")
		return 0, 0, false
	} else if present {
		if value < 1 {
			writeValidationDetail(w, "page", "Input should be greater than or equal to 1")
			return 0, 0, false
		}
		page = value
	}
	size := defaultSize
	if value, present, err := parseQueryIntStrict(r, "page_size"); err != nil {
		writeValidationDetail(w, "page_size", "Input should be a valid integer")
		return 0, 0, false
	} else if present {
		if value < 1 {
			writeValidationDetail(w, "page_size", "Input should be greater than or equal to 1")
			return 0, 0, false
		}
		size = value
	}
	return page, size, true
}

// writeValidationDetail 写出 FastAPI 风格的 detail 数组
func writeValidationDetail(w http.ResponseWriter, field, message string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"detail": []any{map[string]any{
			"type": "value_error",
			"loc":  []any{"query", field},
			"msg":  message,
		}},
	})
}

// writeValidationError 写出 422 校验错误
func writeValidationError(w http.ResponseWriter, message string) {
	httputil.WriteValidationError(w, message)
}

// writeRequestValidationError 写出 FastAPI 风格的请求校验错误
func writeRequestValidationError(w http.ResponseWriter, message string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"detail": []any{map[string]any{"msg": message}},
	})
}

// transferError 带 HTTP 状态码的业务错误
//
// emoji 上传也要带状态码返回，因此在本包提供一份等价实现。
type transferError struct {
	Status int
	Detail string
}

func (e *transferError) Error() string { return e.Detail }

// newTransferError 构造带状态码的业务错误
func newTransferError(status int, detail string) error {
	return &transferError{Status: status, Detail: detail}
}

// webuiUserIDPattern 校验 WebUI 虚拟用户 ID 格式
var webuiUserIDPattern = regexp.MustCompile(`^webui_[A-Za-z0-9_-]+$`)

// authenticated 判断请求是否已通过认证，供需要区分「是否登录」的分支使用
func (d Deps) authenticated(r *http.Request) bool {
	return authutil.ValidSessionToken(d.AdminAuth, authutil.SessionToken(r))
}

// chiURLParam 读取路径参数
func chiURLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// detectAvatarSuffix 按魔数推断图片后缀，无法识别返回空串
//
// 必须返回空串而不是兜底 .png：调用方靠空串判断「不是合法图片」并回 415，
// 兜底成 .png 会让任意二进制内容被当作图片放行。
func detectAvatarSuffix(imageBytes []byte) string {
	switch {
	case len(imageBytes) >= 3 && imageBytes[0] == 0xff && imageBytes[1] == 0xd8 && imageBytes[2] == 0xff:
		return ".jpg"
	case len(imageBytes) >= 8 && string(imageBytes[:8]) == "\x89PNG\r\n\x1a\n":
		return ".png"
	case len(imageBytes) >= 6 && (string(imageBytes[:6]) == "GIF87a" || string(imageBytes[:6]) == "GIF89a"):
		return ".gif"
	case len(imageBytes) >= 12 && string(imageBytes[:4]) == "RIFF" && string(imageBytes[8:12]) == "WEBP":
		return ".webp"
	case len(imageBytes) >= 2 && string(imageBytes[:2]) == "BM":
		return ".bmp"
	}
	return ""
}

// intValueFromAny 把任意数值转为 int，无法识别时返回 0
func intValueFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
		return 0
	default:
		return 0
	}
}
