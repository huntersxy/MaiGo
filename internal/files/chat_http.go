package filesvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"MaiGo/internal/webui/middleware"
)

// FileChat 文件页可选择的聊天流
type FileChat struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

// ChatHooks WebUI 聊天附件与发送钩子；由主程序注入，避免 filesvc 依赖 Agent
type ChatHooks struct {
	List                 func(ctx context.Context) ([]FileChat, error)
	ResolveUploadSession func(ctx context.Context, chatSessionID string) (string, error)
	Send                 func(ctx context.Context, fileRef, sessionID string) (map[string]any, error)
}

func (s *Service) handleFileChats(adminAuth *auth.Manager, hooks ChatHooks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		if err := s.requireEnabled(); err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}
		if hooks.List == nil {
			writeFileError(w, http.StatusServiceUnavailable, "文件聊天列表不可用")
			return
		}
		items, err := hooks.List(r.Context())
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		if items == nil {
			items = []FileChat{}
		}
		writeFileJSON(w, http.StatusOK, items)
	}
}

func (s *Service) handleChatUpload(adminAuth *auth.Manager, hooks ChatHooks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		if err := s.requireEnabled(); err != nil {
			writeFileError(w, http.StatusForbidden, err.Error())
			return
		}
		if hooks.ResolveUploadSession == nil {
			writeFileError(w, http.StatusServiceUnavailable, "文件聊天服务不可用")
			return
		}
		chatSessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if chatSessionID == "" {
			writeFileError(w, http.StatusBadRequest, "session_id 不能为空")
			return
		}
		if name == "" {
			writeFileError(w, http.StatusBadRequest, "name 不能为空")
			return
		}
		sessionID, err := hooks.ResolveUploadSession(r.Context(), chatSessionID)
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		s.startSessionUpload(w, r, sessionID, name)
	}
}

func (s *Service) handleSendFile(adminAuth *auth.Manager, hooks ChatHooks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFileAdmin(w, r, adminAuth) {
			return
		}
		if !validateSendFileRequestBody(w, r) {
			return
		}
		if hooks.Send == nil {
			writeFileError(w, http.StatusServiceUnavailable, "文件发送服务不可用")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeFileError(w, http.StatusBadRequest, "读取请求体失败")
			return
		}
		var payload struct {
			SessionID string `json:"session_id"`
		}
		if err := sonic.Unmarshal(body, &payload); err != nil {
			writeFileError(w, http.StatusBadRequest, "请求体格式无效")
			return
		}
		payload.SessionID = strings.TrimSpace(payload.SessionID)
		if payload.SessionID == "" {
			writeFileError(w, http.StatusBadRequest, "session_id 不能为空")
			return
		}
		fileRef := strings.TrimSpace(chi.URLParam(r, "file_ref"))
		if _, err := s.Ensure(r.Context(), fileRef, payload.SessionID, true); err != nil {
			writeFileHTTPError(w, err)
			return
		}
		result, err := hooks.Send(r.Context(), fileRef, payload.SessionID)
		if err != nil {
			writeFileHTTPError(w, err)
			return
		}
		writeFileJSON(w, http.StatusOK, result)
	}
}

func (s *Service) startSessionUpload(w http.ResponseWriter, r *http.Request, sessionID, name string) {
	mimeType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	asset, err := s.BeginUpload(sessionID, name, mimeType)
	if err != nil {
		writeFileHTTPError(w, err)
		return
	}
	s.receiveUpload(w, r, asset)
}
func appendFileChatNames(ctx context.Context, items []map[string]any, hooks ChatHooks) {
	if hooks.List == nil {
		return
	}
	chats, err := hooks.List(ctx)
	if err != nil {
		return
	}
	names := make(map[string]string, len(chats))
	for _, chat := range chats {
		names[strings.TrimSpace(chat.SessionID)] = strings.TrimSpace(chat.Name)
	}
	for _, item := range items {
		sessions, _ := item["session_ids"].([]string)
		bound := make([]map[string]string, 0, len(sessions))
		for _, sessionID := range sessions {
			name := names[sessionID]
			if name == "" {
				name = "已移除的聊天"
			}
			bound = append(bound, map[string]string{"session_id": sessionID, "name": name})
		}
		item["chats"] = bound
	}
}

// fileBodyValidationError 对齐 FastAPI RequestValidationError 的 detail 单条
// 与 webuiapi 保持同形，但 filesvc 不能反向依赖 webuiapi
type fileBodyValidationError struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx,omitempty"`
}

func writeFileValidationError(w http.ResponseWriter, errs ...fileBodyValidationError) {
	writeFileJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": errs})
}

// validateSendFileRequestBody 对齐 Python files.py:72-78 的 SendFileRequest 校验
func validateSendFileRequestBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		writeFileValidationError(w, fileBodyValidationError{Type: "missing", Loc: []any{"body"}, Msg: "Field required", Input: nil})
		return false
	}
	// FastAPI 会完整读取请求体（fastapi/routing.py:458），这里不做长度截断
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeFileError(w, http.StatusBadRequest, "读取请求体失败")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	// 空 body 由 FastAPI 的 body_bytes 真假判断直接进入 missing（fastapi/routing.py:458-460）
	if len(raw) == 0 {
		writeFileValidationError(w, fileBodyValidationError{Type: "missing", Loc: []any{"body"}, Msg: "Field required", Input: nil})
		return false
	}
	// 只有 application/json 与 application/*+json 解析 JSON，其余 content-type 把原始字节交给模型
	if !isJSONFileContentType(r.Header.Get("Content-Type")) {
		writeFileValidationError(w, fileBodyValidationError{
			Type: "model_attributes_type", Loc: []any{"body"},
			Msg: "Input should be a valid dictionary or object to extract fields from", Input: string(raw),
		})
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		pos, message := fileJSONErrorPosition(raw, err)
		writeFileValidationError(w, fileBodyValidationError{
			Type: "json_invalid", Loc: []any{"body", pos}, Msg: "JSON decode error", Input: map[string]any{},
			Ctx: map[string]any{"error": message},
		})
		return false
	}
	// FastAPI 把 JSON null 视为未提供请求体
	if value == nil {
		writeFileValidationError(w, fileBodyValidationError{Type: "missing", Loc: []any{"body"}, Msg: "Field required", Input: nil})
		return false
	}
	object, ok := value.(map[string]any)
	if !ok {
		writeFileValidationError(w, fileBodyValidationError{
			Type: "model_attributes_type", Loc: []any{"body"},
			Msg: "Input should be a valid dictionary or object to extract fields from", Input: value,
		})
		return false
	}
	sessionValue, present := object["session_id"]
	if !present {
		writeFileValidationError(w, fileBodyValidationError{Type: "missing", Loc: []any{"body", "session_id"}, Msg: "Field required", Input: object})
		return false
	}
	if _, ok := sessionValue.(string); !ok {
		writeFileValidationError(w, fileBodyValidationError{Type: "string_type", Loc: []any{"body", "session_id"}, Msg: "Input should be a valid string", Input: sessionValue})
		return false
	}
	return true
}

// isJSONFileContentType 判断 FastAPI 是否会解析 JSON 请求体
// 仅 application/json 与 application/*+json 走 JSON，其余 content-type 按原始字节校验
func isJSONFileContentType(header string) bool {
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

func fileJSONErrorPosition(raw []byte, err error) (int, string) {
	syntax := &json.SyntaxError{}
	if errors.As(err, &syntax) {
		offset := int(syntax.Offset)
		if offset < 1 {
			offset = 1
		}
		message := syntax.Error()
		if strings.Contains(message, "looking for beginning of object key string") {
			if offset-1 < len(raw) && offset-1 >= 1 && raw[offset-1] == '}' {
				return offset - 2, "Illegal trailing comma before end of object"
			}
			return offset - 1, "Expecting property name enclosed in double quotes"
		}
		return offset - 1, message
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return len(raw), "Expecting ',' delimiter"
	}
	return len(raw), "Expecting value"
}
