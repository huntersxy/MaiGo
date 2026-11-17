package host

// 宿主维护接口：自更新与插件依赖；对齐 src/host/webui.py 的维护路由。

import (
	"encoding/json"
	"net/http"
	"strings"

	"MaiGo/internal/common/self_update"

	httputil2 "MaiGo/internal/webui/httputil"
)

func (w *WebUI) handleUpdateStatus(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, w.maintenance.UpdateStatus(r.Context()))
}

func (w *WebUI) handleUpdateCheck(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	check, err := w.maintenance.CheckUpdate(r.Context())
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, map[string]any{
		"local": check.Local, "checked_at": check.CheckedAt, "latest_commit": check.LatestCommit,
		"latest_version": check.LatestVersion, "behind": check.Behind, "ahead": check.Ahead,
		"dependencies_changed": check.DependenciesChanged, "commits": check.Commits,
		"blockers": check.Blockers, "source": check.Source,
		"can_apply": check.CanApply(), "blocker_messages": updateBlockerMessages(check.Blockers),
	})
}

func (w *WebUI) handleUpdateApply(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Target) == "" {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "target 不能为空"})
		return
	}
	result, err := w.maintenance.PrepareUpdate(strings.TrimSpace(body.Target))
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, result)
	go func() {
		// 回执发送后再关闭 HTTP；更新过程与浏览器连接存续无关
		w.controlStop()
	}()
}

func (w *WebUI) controlStop() {
	select {
	case <-w.stoppingCh:
	default:
		close(w.stoppingCh)
	}
}

func updateBlockerMessages(blockers []string) []string {
	messages := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		messages = append(messages, selfupdate.BlockerMessage(blocker))
	}
	return messages
}

func (w *WebUI) handlePluginDependencies(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	report, err := w.maintenance.AnalyzePluginDependencies(r.Context())
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, map[string]any{
		"bots": report.Bots, "requirements": report.Requirements, "all_requirements": report.AllRequirements,
		"conflicts": report.Conflicts, "busy": w.maintenance.Busy(), "error": w.maintenance.DependencyError(),
	})
}

func (w *WebUI) handleInstallPluginDependencies(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	result, err := w.maintenance.InstallPluginDependencies(r.Context())
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, result)
}
