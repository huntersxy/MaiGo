package webuiapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"maibot-go/internal/learners"
)

// fastAPIValidationError 对齐 FastAPI/Pydantic 请求校验失败时的单条 detail
// input 即使是 null 也必须出现在 JSON 中，因此不能加 omitempty
type fastAPIValidationError struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx,omitempty"`
}

// writeAPIError 对齐 FastAPI HTTPException 的 detail 字段，同时保留 Go 侧 success/message
func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"detail":  message,
		"success": false,
		"message": message,
	})
}

// writeValidationError 对齐 FastAPI HTTPException(status_code=422, detail="...")
// 自定义业务 422 使用字符串 detail，参数校验错误请用 writeRequestValidationError
func writeValidationError(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": message})
}

// writeRequestValidationError 对齐 FastAPI RequestValidationError 的 detail 数组
func writeRequestValidationError(w http.ResponseWriter, errs ...fastAPIValidationError) {
	if len(errs) == 0 {
		errs = []fastAPIValidationError{}
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": errs})
}

// writeInternalError 对齐 Python 未捕获异常转 500 的行为
func writeInternalError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"success":     false,
		"implemented": true,
		"error":       err.Error(),
		"detail":      err.Error(),
	})
}

// learnerStore 返回 Dashboard 学习/资源台查询实现；未注入时为 nil
func learnerStore() learners.WebUIStore {
	return learners.DefaultRegistry().WebUIStore()
}

// requireLearnerStore 未注入 WebUIStore 时返回 503，与 chat/news/bot-accounts 缺依赖口径一致
func (d Deps) requireLearnerStore(w http.ResponseWriter, _ string, detail string) (learners.WebUIStore, bool) {
	store := learnerStore()
	if store == nil {
		writeAPIError(w, http.StatusServiceUnavailable, detail)
		return nil, false
	}
	return store, true
}

// parseQueryIntStrict 解析整数查询参数；未提供时返回 fallback
// 仅用于非 FastAPI 形状的历史调用点，统计/分页参数请用 queryIntParam
func parseQueryIntStrict(r *http.Request, key string) (int, bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, false, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true, errors.New(key + " 必须是整数")
	}
	return value, true, nil
}

// queryIntParam 按 FastAPI Query(int) 语义读取最后一个同名参数
// 返回原始字符串用于 Pydantic 错误 input；空字符串按 int_parsing 报错
func queryIntParam(r *http.Request, key string) (value int, raw string, present bool, verr *fastAPIValidationError) {
	values, ok := r.URL.Query()[key]
	if !ok || len(values) == 0 {
		return 0, "", false, nil
	}
	raw = values[len(values)-1]
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, raw, true, &fastAPIValidationError{
			Type:  "int_parsing",
			Loc:   []any{"query", key},
			Msg:   "Input should be a valid integer, unable to parse string as an integer",
			Input: raw,
		}
	}
	return parsed, raw, true, nil
}

// queryPagination 解析 page/page_size，按 FastAPI 一次性返回所有越界错误
func queryPagination(w http.ResponseWriter, r *http.Request, defaultSize int) (int, int, bool) {
	errs := make([]fastAPIValidationError, 0, 2)
	page, rawPage, hasPage, pageErr := queryIntParam(r, "page")
	if pageErr != nil {
		errs = append(errs, *pageErr)
	} else {
		if !hasPage {
			page = 1
		} else if page < 1 {
			errs = append(errs, fastAPIValidationError{
				Type:  "greater_than_equal",
				Loc:   []any{"query", "page"},
				Msg:   "Input should be greater than or equal to 1",
				Input: rawPage,
				Ctx:   map[string]any{"ge": 1},
			})
		}
	}
	size, rawSize, hasSize, sizeErr := queryIntParam(r, "page_size")
	if sizeErr != nil {
		errs = append(errs, *sizeErr)
	} else {
		switch {
		case !hasSize:
			size = defaultSize
		case size < 1:
			errs = append(errs, fastAPIValidationError{
				Type:  "greater_than_equal",
				Loc:   []any{"query", "page_size"},
				Msg:   "Input should be greater than or equal to 1",
				Input: rawSize,
				Ctx:   map[string]any{"ge": 1},
			})
		case size > 100:
			errs = append(errs, fastAPIValidationError{
				Type:  "less_than_equal",
				Loc:   []any{"query", "page_size"},
				Msg:   "Input should be less than or equal to 100",
				Input: rawSize,
				Ctx:   map[string]any{"le": 100},
			})
		}
	}
	if len(errs) > 0 {
		writeRequestValidationError(w, errs...)
		return 0, 0, false
	}
	return page, size, true
}

// parsePydanticBool 对齐 Pydantic v2 lax bool：大小写不敏感，不接受首尾空格
func parsePydanticBool(raw string) (bool, bool) {
	switch strings.ToLower(raw) {
	case "1", "on", "t", "true", "y", "yes":
		return true, true
	case "0", "off", "f", "false", "n", "no":
		return false, true
	default:
		return false, false
	}
}

// optionalQueryBool 解析可选布尔参数；present 表示查询参数存在，verr 表示值非法
func optionalQueryBool(r *http.Request, key string) (value bool, present bool, verr *fastAPIValidationError) {
	values, ok := r.URL.Query()[key]
	if !ok || len(values) == 0 {
		return false, false, nil
	}
	raw := values[len(values)-1]
	parsed, valid := parsePydanticBool(raw)
	if !valid {
		return false, true, &fastAPIValidationError{
			Type:  "bool_parsing",
			Loc:   []any{"query", key},
			Msg:   "Input should be a valid boolean, unable to interpret input",
			Input: raw,
		}
	}
	return parsed, true, nil
}

// optionalQueryBoolPtr 返回可选布尔指针，非法值按 FastAPI 422 数组返回
func optionalQueryBoolPtr(w http.ResponseWriter, r *http.Request, key string) (*bool, bool) {
	value, present, verr := optionalQueryBool(r, key)
	if verr != nil {
		writeRequestValidationError(w, *verr)
		return nil, false
	}
	if !present {
		return nil, true
	}
	return &value, true
}

// requireNonEmptyChatID 校验聊天流 ID 非空，不要求会话仍存在
func requireNonEmptyChatID(raw string) (string, error) {
	chatID := strings.TrimSpace(raw)
	if chatID == "" {
		return "", errors.New("缺少聊天流 ID")
	}
	return chatID, nil
}

// requireExistingChatID 校验聊天流 ID 为数据库中的真实会话
func requireExistingChatID(store learners.WebUIStore, w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	chatID, err := requireNonEmptyChatID(raw)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	if !store.ExistingSession(r.Context(), chatID) {
		writeAPIError(w, http.StatusBadRequest, "聊天流不存在: "+chatID)
		return "", false
	}
	return chatID, true
}

// requireAuthHandler 包装单个 handler 的登录校验，保持路由表路径不变
func (d Deps) requireAuthHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.requireAuth(w, r) {
			return
		}
		if !validateRequestBodyForRoute(w, r) {
			return
		}
		next(w, r)
	}
}

// requireAuthMiddleware 对齐 FastAPI 路由级 dependencies=[Depends(require_auth)]
func (d Deps) requireAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.requireAuth(w, r) {
			return
		}
		if !validateRequestBodyForRoute(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth 对齐 MaiBot require_auth 依赖：未认证返回 401
func (d Deps) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if d.authenticated(r) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
	return false
}
