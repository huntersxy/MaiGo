package chat

import (
	"context"
	"maibot-go/internal/webui/httputil"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/platform_io"
)

type chatSessionItem struct {
	SessionID       string `json:"session_id"`
	ConversationKey string `json:"conversation_key"`
	Kind            string `json:"kind"`
	TargetID        int64  `json:"target_id"`
	Name            string `json:"name"`
	Active          bool   `json:"active"`
	Blocked         bool   `json:"blocked"`
}

func (d Deps) handleChatSessions(w http.ResponseWriter, r *http.Request) {
	if d.Memory == nil {
		writeChatFailure(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}
	limit := queryInt(r, "limit", 200, 1, 1000)
	offset := queryInt(r, "offset", 0, 0, 1_000_000)
	rows, _, err := d.listChatSessionRows(r.Context(), limit, offset)
	if err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "获取聊天流列表失败: "+err.Error())
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.response())
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "sessions": items, "total": len(items)})
}

type chatSessionMessageItem struct {
	ID                 uint   `json:"id"`
	Platform           string `json:"platform"`
	ChatType           string `json:"chat_type"`
	SessionID          string `json:"session_id"`
	MessageID          int64  `json:"message_id"`
	UserID             int64  `json:"user_id"`
	AccountID          string `json:"account_id"`
	Scope              string `json:"scope"`
	ExternalMessageID  string `json:"external_message_id"`
	ExternalTargetID   string `json:"external_target_id"`
	ExternalUserID     string `json:"external_user_id"`
	ProcessedPlainText string `json:"processed_plain_text"`
	Timestamp          string `json:"timestamp"`
}

func (d Deps) handleChatSessionMessages(w http.ResponseWriter, r *http.Request) {
	if d.Memory == nil {
		writeChatFailure(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "implemented": true, "error": "session_id 不能为空",
		})
		return
	}
	ctx := r.Context()
	sessionMgr := chatsession.NewManager(d.Memory.GetDB())
	if _, err := sessionMgr.GetExistingBySessionID(ctx, sessionID); err != nil {
		httputil.WriteJSON(w, http.StatusNotFound, map[string]any{
			"success": false, "implemented": true, "error": "未知 session_id",
		})
		return
	}
	limit := queryInt(r, "limit", 50, 1, 500)
	offset := queryInt(r, "offset", 0, 0, 1_000_000)
	rows, err := chatsession.ListRecentMessages(ctx, d.Memory, sessionID, limit, offset)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "implemented": true, "error": err.Error(),
		})
		return
	}
	items := make([]chatSessionMessageItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, chatSessionMessageItem{
			ID:                 row.ID,
			Platform:           row.Platform,
			ChatType:           row.ConversationKind,
			SessionID:          row.SessionID,
			MessageID:          row.MessageID,
			UserID:             row.UserID,
			AccountID:          row.AccountID,
			Scope:              row.Scope,
			ExternalMessageID:  row.ExternalMessageID,
			ExternalTargetID:   row.ExternalTargetID,
			ExternalUserID:     row.ExternalUserID,
			ProcessedPlainText: row.ProcessedPlainText,
			Timestamp:          row.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"session_id": sessionID,
		"messages":   items,
		"count":      len(items),
	})
}

func queryInt(r *http.Request, key string, def, min, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// resolveChatSessionID 供其它 WebUI  handler 解析真实 session_id
func (d Deps) resolveChatSessionID(ctx context.Context, kind string, targetID int64) string {
	if d.Memory == nil || targetID <= 0 {
		return ""
	}
	mgr := chatsession.NewManager(d.Memory.GetDB())
	rec, err := mgr.ResolveSession(ctx, platformio.PlatformQQ, strconv.FormatInt(targetID, 10), kind)
	if err != nil || rec == nil {
		return ""
	}
	return rec.SessionID
}
