package webuiapi

import (
	"net/http"
	"strings"

	"MaiGo/internal/common/self_update"
)

// 本文件对齐 ../MaiBot/src/webui/routers/system.py 的更新公告、历史记录与配置重载端点

func (d Deps) systemCurrentVersion() string {
	return d.buildVersion()
}

func (d Deps) handleSystemUpdateNotice(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	currentVersion := d.systemCurrentVersion()
	instanceDir, err := updateNoticeRoot(d.InstanceDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "获取更新公告失败: " + err.Error()})
		return
	}
	notice, err := getPendingUpdateNotice(instanceDir, currentVersion)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "获取更新公告失败: " + err.Error()})
		return
	}
	if notice == nil && r.URL.Query().Get("force") == "true" {
		debugNotice := buildDebugUpdateNotice(currentVersion, changelogPath(instanceDir))
		notice = &debugNotice
	}
	if notice == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"pending": false, "current_version": currentVersion,
			"from_version": nil, "versions": []string{}, "content": "",
			"incompatible_plugins": []selfupdate.IncompatiblePluginNotice{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending": true, "current_version": notice.CurrentVersion,
		"from_version": notice.FromVersion, "versions": notice.Versions,
		"content":              notice.Content,
		"incompatible_plugins": d.updateIncompatiblePlugins(r.Context(), notice.FromVersion, notice.CurrentVersion),
	})
}

func (d Deps) handleSystemUpdateNoticeAck(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	currentVersion := d.systemCurrentVersion()
	instanceDir, err := updateNoticeRoot(d.InstanceDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "确认更新公告失败: " + err.Error()})
		return
	}
	if err := markUpdateNoticeSeen(instanceDir, currentVersion); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "确认更新公告失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "message": "更新公告已确认", "version": currentVersion,
	})
}

func (d Deps) handleSystemUpdateHistory(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	currentVersion := d.systemCurrentVersion()
	instanceDir, err := updateNoticeRoot(d.InstanceDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "获取历史更新记录失败: " + err.Error()})
		return
	}
	offset := queryInt(r, "offset", 0, 0, 1<<20)
	limit := queryInt(r, "limit", 3, 1, 10)
	section := strings.TrimSpace(r.URL.Query().Get("section"))
	if section != "" && section != "webui" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "section 仅支持 webui"})
		return
	}
	entries := getChangelogHistory(instanceDir, currentVersion, offset, limit+1, strings.TrimSpace(r.URL.Query().Get("before_version")), section)
	visible := entries
	hasMore := false
	if len(visible) > limit {
		visible = visible[:limit]
		hasMore = true
	}
	payload := make([]map[string]any, 0, len(visible))
	for _, entry := range visible {
		payload = append(payload, map[string]any{
			"version": entry.Version, "title": entry.Title, "content": entry.Markdown,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": payload, "next_offset": offset + len(payload), "has_more": hasMore,
	})
}

func (d Deps) handleSystemReloadConfig(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "message": "配置重载功能待实现",
	})
}
