package webuiapi

import (
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "person_id 不能为空"})
		return
	}
	var request personUpdateRequest
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式错误"})
		return
	}
	update, fields := request.profileUpdate()
	if fields == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "未提供任何需要更新的字段"})
		return
	}
	profile, err := personinfo.UpdateProfileFields(r.Context(), personID, update)
	if err != nil {
		switch {
		case errors.Is(err, personinfo.ErrPersonNotFound):
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "未找到 ID 为 " + personID + " 的人物信息"})
		case errors.Is(err, personinfo.ErrIndividualChanged):
			writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
		case errors.Is(err, personinfo.ErrMemoryUnavailable):
			writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "implemented": true, "error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "成功更新 " + strconv.Itoa(fields) + " 个字段",
		"data":    personProfileToWebUI(*profile),
	})
}

type personUpdateRequest struct {
	ExpectedIndividualID *string                       `json:"expected_individual_id"`
	ExpectedProfile      *personinfo.IndividualProfile `json:"expected_profile"`
	PersonName           *string                       `json:"person_name"`
	Nickname             *string                       `json:"nickname"`
	IsKnown              *bool                         `json:"is_known"`
}

func (request personUpdateRequest) profileUpdate() (personinfo.ProfileUpdate, int) {
	update := personinfo.ProfileUpdate{
		ExpectedIndividualID: request.ExpectedIndividualID,
		ExpectedProfile:      request.ExpectedProfile,
		PersonName:           request.PersonName,
		Nickname:             request.Nickname,
		IsKnown:              request.IsKnown,
	}
	fields := 0
	if request.PersonName != nil {
		fields++
	}
	if request.Nickname != nil {
		fields++
	}
	if request.IsKnown != nil {
		fields++
	}
	return update, fields
}
