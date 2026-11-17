package app

// 仅宿主 IPC 可访问的本地操作员入口，对齐 src/common/http_service/console.py。

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (a *App) handleHostConsole(w http.ResponseWriter, r *http.Request) {
	if a.consoleSubmit == nil {
		writeConsoleJSON(w, http.StatusForbidden, map[string]any{"detail": "此 Bot 未开启 debug.enable_console_input"})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&body); err != nil {
		writeConsoleJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "终端输入不能为空"})
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeConsoleJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "终端输入不能为空"})
		return
	}
	if err := a.consoleSubmit(r.Context(), text); err != nil {
		writeConsoleJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeConsoleJSON(w, http.StatusOK, map[string]any{"accepted": true})
}

func (a *App) handleHostConsoleCompletions(w http.ResponseWriter, r *http.Request) {
	if a.consoleCompletions == nil {
		writeConsoleJSON(w, http.StatusForbidden, map[string]any{"detail": "此 Bot 未开启 debug.enable_console_input"})
		return
	}
	writeConsoleJSON(w, http.StatusOK, a.consoleCompletions())
}

func writeConsoleJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
