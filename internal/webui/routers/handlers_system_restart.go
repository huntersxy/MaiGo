package webuiapi

import (
	"errors"
	"net/http"
	"strings"

	"maibot-go/internal/runtime"

	"github.com/bytedance/sonic"
)

type requestRestartBody struct {
	PendingPath string `json:"pending_path"`
}

// handleSystemRequestRestart POST /api/webui/system/request-restart
//
// 写入 pending 标记（JSON body 的 pending_path，或沿用环境变量 MAIBOT_UPDATE_PENDING_PATH）并请求 Worker 以退出码 42 结束。
// 需要 MAIBOT_RUNTIME_MODE=runner 监督 Worker；未设置时 Worker 非子进程，返回 409。
// 单进程模式（无 Runner）下不会假装成功：应使用 runner 包装或在外部替换二进制后重启服务。
func (d Deps) handleSystemRequestRestart(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false,
			"message": "未登录",
		})
		return
	}

	var body requestRestartBody
	if r.Body != nil {
		_ = sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body)
	}
	pending := strings.TrimSpace(body.PendingPath)
	if pending == "" {
		pending = strings.TrimSpace(r.URL.Query().Get("pending_path"))
	}

	if err := runtime.ScheduleWorkerRestart(pending); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, runtime.ErrRestartRequiresWorker) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "麦麦正在重启中...",
	})
}

// handleSystemRestart 对齐 Dashboard POST /api/webui/system/restart（不写入 pending，仅触发 42）
func (d Deps) handleSystemRestart(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false,
			"message": "未登录",
		})
		return
	}
	if err := runtime.ScheduleWorkerRestart(""); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, runtime.ErrRestartRequiresWorker) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "麦麦正在重启中...",
	})
}
