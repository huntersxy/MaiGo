package webuiapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"MaiGo/internal/person_info"
)

func (d Deps) handlePersonUpdate(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	if d.Memory == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}
	personID := strings.TrimSpace(chi.URLParam(r, "person_id"))
	if personID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "message": "person_id 不能为空",
		})
		return
	}

	var body personUpdateRequest
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "message": "请求格式错误",
		})
		return
	}
	upd, fieldCount, err := body.toProfileUpdate()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "message": err.Error(),
		})
		return
	}
	if fieldCount == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "message": "未提供任何需要更新的字段",
		})
		return
	}

	prof, err := personinfo.UpdateProfileFields(r.Context(), personID, upd)
	if err != nil {
		if errors.Is(err, personinfo.ErrPersonNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"success": false, "message": "未找到 ID 为 " + personID + " 的人物信息",
			})
			return
		}
		if errors.Is(err, personinfo.ErrMemoryUnavailable) {
			writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "implemented": true, "error": err.Error(),
		})
		return
	}

	rec := profileToPersonInfoRecord(prof)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "成功更新 " + strconv.Itoa(fieldCount+1) + " 个字段",
		"data":    personInfoToWebUI(rec),
	})
}

type personUpdateRequest struct {
	PersonName   *string `json:"person_name"`
	NameReason   *string `json:"name_reason"`
	Nickname     *string `json:"nickname"`
	MemoryPoints *string `json:"memory_points"`
	IsKnown      *bool   `json:"is_known"`
}

func (req personUpdateRequest) toProfileUpdate() (personinfo.ProfileUpdate, int, error) {
	var upd personinfo.ProfileUpdate
	count := 0
	if req.PersonName != nil {
		upd.PersonName = req.PersonName
		count++
	}
	if req.NameReason != nil {
		upd.NameReason = req.NameReason
		count++
	}
	if req.Nickname != nil {
		upd.Nickname = req.Nickname
		count++
	}
	if req.IsKnown != nil {
		upd.IsKnown = req.IsKnown
		count++
	}
	if req.MemoryPoints != nil {
		points, err := parseMemoryPointsField(*req.MemoryPoints)
		if err != nil {
			return upd, count, err
		}
		upd.MemoryPoints = &points
		count++
	}
	return upd, count, nil
}

func parseMemoryPointsField(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		empty := []string{}
		return empty, nil
	}
	var points []string
	if err := json.Unmarshal([]byte(raw), &points); err != nil {
		return nil, errors.New("memory_points 必须是 JSON 字符串数组")
	}
	return points, nil
}

func profileToPersonInfoRecord(prof *personinfo.Profile) *personinfo.PersonInfo {
	if prof == nil {
		return &personinfo.PersonInfo{}
	}
	memRaw, _ := json.Marshal(prof.MemoryPoints)
	groupRaw, _ := json.Marshal(prof.GroupCards)
	memText, groupText := string(memRaw), string(groupRaw)
	return &personinfo.PersonInfo{
		IsKnown:        prof.IsKnown,
		PersonID:       prof.PersonID,
		PersonName:     prof.PersonName,
		NameReason:     prof.NameReason,
		Platform:       prof.Platform,
		UserID:         prof.UserID,
		UserNickname:   prof.Nickname,
		GroupCardname:  &groupText,
		MemoryPoints:   &memText,
		KnowCounts:     prof.KnowCounts,
		FirstKnownTime: prof.FirstKnownTime,
		LastKnownTime:  prof.LastKnownTime,
	}
}
