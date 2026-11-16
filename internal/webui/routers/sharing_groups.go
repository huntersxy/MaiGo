package webuiapi

// 独立相通组管理，对齐 MaiBot src/webui/routers/sharing_groups.py。

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"MaiGo/internal/services"
)

func (d Deps) sharingGroupDB() error {
	if d.Memory == nil || d.Memory.GetDB() == nil {
		return errors.New("数据库未接线")
	}
	return nil
}

// handleSharingGroupList 对齐 GET /sharing-groups
func (d Deps) handleSharingGroupList(w http.ResponseWriter, r *http.Request) {
	if err := d.sharingGroupDB(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
		return
	}
	groups := services.ListSharingGroups(r.Context())
	for index := range groups {
		members := services.SharingResolvedMembers(r.Context(), groups[index])
		resolved := make([]string, 0, len(members))
		for id := range members {
			resolved = append(resolved, id)
		}
		sort.Strings(resolved)
		groups[index].ResolvedIDs = resolved
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":         groups,
		"capabilities": services.SharingCapabilities,
	})
}

// handleSharingGroupCreate 对齐 POST /sharing-groups
func (d Deps) handleSharingGroupCreate(w http.ResponseWriter, r *http.Request) {
	if err := d.sharingGroupDB(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
		return
	}
	var input services.SharingGroupInput
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	group, err := services.SaveSharingGroup(r.Context(), input, nil)
	if err != nil {
		writeSharingGroupError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, group)
}

// handleSharingGroupUpdate 对齐 PUT /sharing-groups/{group_id}
func (d Deps) handleSharingGroupUpdate(w http.ResponseWriter, r *http.Request) {
	if err := d.sharingGroupDB(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
		return
	}
	groupID := chi.URLParam(r, "group_id")
	var input services.SharingGroupInput
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	if input.Revision == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "更新相通组必须提供 revision"})
		return
	}
	group, err := services.SaveSharingGroup(r.Context(), input, &groupID)
	if err != nil {
		writeSharingGroupError(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, group)
}

// handleSharingGroupDelete 对齐 DELETE /sharing-groups/{group_id}
func (d Deps) handleSharingGroupDelete(w http.ResponseWriter, r *http.Request) {
	if err := d.sharingGroupDB(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
		return
	}
	revision, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("revision")))
	if err != nil || revision < 1 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "revision 必须大于等于 1"})
		return
	}
	if err := services.DeleteSharingGroup(r.Context(), chi.URLParam(r, "group_id"), revision); err != nil {
		writeSharingGroupError(w, err, http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func writeSharingGroupError(w http.ResponseWriter, err error, fallbackStatus int) {
	switch {
	case errors.Is(err, services.ErrSharingGroupNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "相通组不存在"})
	case errors.Is(err, services.ErrSharingGroupConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
	default:
		writeJSON(w, fallbackStatus, map[string]any{"detail": err.Error()})
	}
}
