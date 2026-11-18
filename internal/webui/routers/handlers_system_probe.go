package webuiapi

import (
	"net/http"
	"strings"

	"github.com/bytedance/sonic"

	systemprobe "MaiGo/internal/services/system_probe"
)

// WriteProbeJSON 先完整编码再写响应；编码失败返回 500 detail，避免 200 空 body。
func WriteProbeJSON(w http.ResponseWriter, status int, payload any) {
	encoded, err := sonic.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"detail": "资源快照编码失败: " + err.Error(),
		})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func (d Deps) handleSystemResourceProbe(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	probe := d.systemProbe()
	if probe == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "系统资源探针未接线"})
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "", "json":
		WriteProbeJSON(w, http.StatusOK, probe.Snapshot(r.Context()))
	case "text":
		body := systemprobe.RenderText(probe.Snapshot(r.Context()))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	default:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "format 仅支持 json 或 text"})
	}
}
