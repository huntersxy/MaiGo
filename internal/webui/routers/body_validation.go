package webuiapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// validateRequestBodyForRoute 在鉴权通过后按 Python 侧路由对应的 Pydantic 模型校验请求体
// 校验失败时写出 FastAPI 形状的 422 detail 数组并返回 false；无规格或方法不匹配时直接放行
func validateRequestBodyForRoute(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return true
	}
	spec, ok := lookupBodyRouteForRequest(r.Method, r.URL.Path)
	if !ok {
		return true
	}
	return validateRequestBody(w, r, spec)
}

// validateRequestBody 读取并校验请求体；body 会被完整恢复供后续处理器再次解析
func validateRequestBody(w http.ResponseWriter, r *http.Request, spec bodyRouteSpec) bool {
	if r.Body == nil {
		if spec.Required {
			writeRequestValidationError(w, missingBodyError(nil))
			return false
		}
		return true
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeRequestValidationError(w, bodyReadError(err))
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	// Python 以 body_bytes 的真假判断请求体是否提供（fastapi/routing.py:458-460），空 body 直接走 missing
	if len(raw) == 0 {
		if spec.Required {
			writeRequestValidationError(w, missingBodyError(nil))
			return false
		}
		return true
	}
	// 只有 application/json 与 application/*+json 才解析 JSON（fastapi/routing.py:461-471）
	// 其余 content-type 与缺失 content-type 都按原始字节交给 Pydantic
	if !isJSONBodyContentType(r.Header.Get("Content-Type")) {
		writeRequestValidationError(w, rawBodyTypeError(spec.Schema, string(raw)))
		return false
	}

	value, parseErr := decodeRequestBodyJSON(raw)
	if parseErr != nil {
		writeRequestValidationError(w, jsonInvalidBodyError(raw, parseErr))
		return false
	}
	// FastAPI 把 JSON null 视为未提供请求体，即使该请求体参数可选
	if value == nil {
		writeRequestValidationError(w, missingBodyError(nil))
		return false
	}
	validator := newBodySchemaValidator(spec.Schema)
	errs := validator.validateBody(value)
	if len(errs) > 0 {
		writeRequestValidationError(w, errs...)
		return false
	}
	return true
}

// isJSONBodyContentType 判断 FastAPI 是否会解析请求体 JSON
// Python 用 email 头取 maintype/subtype，仅 application/json 与 application/*+json 解析
func isJSONBodyContentType(header string) bool {
	value := strings.TrimSpace(header)
	if value == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	}
	mainType, subType, ok := strings.Cut(mediaType, "/")
	if !ok {
		return false
	}
	mainType = strings.ToLower(strings.TrimSpace(mainType))
	subType = strings.ToLower(strings.TrimSpace(subType))
	return mainType == "application" && (subType == "json" || strings.HasSuffix(subType, "+json"))
}

// rawBodyTypeError 构造非 JSON 请求体的 Pydantic 类型错误
// dict[str, Any] 路由得到 dict_type，Pydantic 模型路由得到 model_attributes_type
func rawBodyTypeError(schema map[string]any, input string) fastAPIValidationError {
	if isPlainDictSchema(schema) {
		return fastAPIValidationError{
			Type:  "dict_type",
			Loc:   []any{"body"},
			Msg:   "Input should be a valid dictionary",
			Input: input,
		}
	}
	return fastAPIValidationError{
		Type:  "model_attributes_type",
		Loc:   []any{"body"},
		Msg:   "Input should be a valid dictionary or object to extract fields from",
		Input: input,
	}
}

func missingBodyError(_ any) fastAPIValidationError {
	return fastAPIValidationError{
		Type:  "missing",
		Loc:   []any{"body"},
		Msg:   "Field required",
		Input: nil,
	}
}

func bodyReadError(err error) fastAPIValidationError {
	return fastAPIValidationError{
		Type:  "body_read_error",
		Loc:   []any{"body"},
		Msg:   err.Error(),
		Input: nil,
	}
}

// decodeRequestBodyJSON 解析请求体并复用 UseNumber 保留数字原始形态
func decodeRequestBodyJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("多余的数据")
		}
		return nil, err
	}
	return value, nil
}

// jsonInvalidBodyError 把 Go 的 JSON 语法错误映射为 Pydantic json_invalid 形状
func jsonInvalidBodyError(raw []byte, err error) fastAPIValidationError {
	pos, message := jsonErrorPosition(raw, err)
	return fastAPIValidationError{
		Type:  "json_invalid",
		Loc:   []any{"body", pos},
		Msg:   "JSON decode error",
		Input: map[string]any{},
		Ctx:   map[string]any{"error": message},
	}
}

// jsonErrorPosition 对齐 Python json 模块的字符位置（0 基）与错误文案
func jsonErrorPosition(raw []byte, err error) (int, string) {
	syntax := &json.SyntaxError{}
	if errors.As(err, &syntax) {
		offset := int(syntax.Offset)
		if offset < 1 {
			offset = 1
		}
		pos := offset - 1
		message := syntax.Error()
		if strings.Contains(message, "looking for beginning of object key string") && offset-1 <= len(raw) && offset-1 >= 1 && raw[offset-1] == '}' {
			return max(offset-2, 0), "Illegal trailing comma before end of object"
		}
		if strings.Contains(message, "looking for beginning of object key string") {
			return pos, "Expecting property name enclosed in double quotes"
		}
		if strings.Contains(message, "looking for beginning of value") {
			return pos, "Expecting value"
		}
		if strings.Contains(message, "after object key:value pair") || strings.Contains(message, "after array element") {
			return pos, "Expecting ',' delimiter"
		}
		if strings.Contains(message, "in string escape code") {
			return pos, "Invalid \\escape"
		}
		if strings.Contains(message, "in string literal") {
			return pos, "Invalid control character at"
		}
		return pos, message
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return len(raw), "Expecting ',' delimiter"
	}
	if errors.Is(err, io.EOF) {
		// 纯空白 body 在 Python 里是跳过空白后到达文末，位置等于长度
		return len(raw), "Expecting value"
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return 0, "Expecting value"
	}
	last := trimmed[len(trimmed)-1]
	if last == ',' || last == ':' || last == '"' || last == '{' || last == '[' {
		return len(raw), "Expecting value"
	}
	return len(raw), err.Error()
}

// requestBodyFieldOrder 返回 Pydantic 模型声明顺序；缺失时按字段名排序保证确定性
func requestBodyFieldOrder(schema map[string]any) []string {
	properties, _ := schema["properties"].(map[string]any)
	order := make([]string, 0, len(properties))
	if rawOrder, ok := schema["x-order"].([]any); ok {
		for _, item := range rawOrder {
			name, ok := item.(string)
			if !ok {
				continue
			}
			if _, exists := properties[name]; exists {
				order = append(order, name)
			}
		}
	}
	if len(order) == len(properties) {
		return order
	}
	seen := make(map[string]struct{}, len(order))
	for _, name := range order {
		seen[name] = struct{}{}
	}
	extra := make([]string, 0, len(properties))
	for name := range properties {
		if _, ok := seen[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}

func intLocString(value int) string { return strconv.Itoa(value) }
