// Package httputil 提供 WebUI 各路由包共用的 HTTP 响应工具。
//
// 拆分 webui/routers 为多个子包后，writeJSON 这类函数被父子包同时需要；
// 放在任一侧都会造成循环引用，因此抽到独立的最底层包。
// 对齐 Python src/webui/routers/ 各子目录共用的响应封装习惯。
package httputil

import (
	"net/http"

	"github.com/bytedance/sonic"
)

// WriteJSON 写出 JSON 响应
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = sonic.ConfigDefault.NewEncoder(w).Encode(payload)
}

// WriteStub 写出 implemented:false 占位响应；运行时缺依赖请用 WriteAPIError(503/502)。
func WriteStub(w http.ResponseWriter, maiBotPath, detail string) {
	WriteJSON(w, http.StatusOK, map[string]any{
		"success":     false,
		"implemented": false,
		"message":     detail,
		"maibot_path": maiBotPath,
	})
}

// WriteValidationError 写出 FastAPI 风格的 422 校验错误
func WriteValidationError(w http.ResponseWriter, message string) {
	WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": message})
}

// WriteAPIError 写出带 success=false 与 message 的错误响应
func WriteAPIError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]any{
		"detail":  message,
		"success": false,
		"message": message,
	})
}
