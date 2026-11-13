package webuiapi

import (
	"net/http"

	"github.com/bytedance/sonic"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = sonic.ConfigDefault.NewEncoder(w).Encode(payload)
}

// writeStub 保留给未完成能力的占位响应；运行时缺依赖请用 writeAPIError(503/502)。
func writeStub(w http.ResponseWriter, maiBotPath, detail string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     false,
		"implemented": false,
		"message":     detail,
		"maibot_path": maiBotPath,
	})
}
