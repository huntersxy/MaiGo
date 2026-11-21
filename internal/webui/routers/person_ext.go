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

func (d Deps) handlePersonList(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	page, pageSize, ok := queryPagination(w, r, 20)
	if !ok {
		return
	}
	search := r.URL.Query().Get("search")
	platform := r.URL.Query().Get("platform")
	userID := r.URL.Query().Get("user_id")
	isKnown, ok := optionalQueryBoolPtr(w, r, "is_known")
	if !ok {
		return
	}
	excludeNumeric, ok := optionalQueryBoolPtr(w, r, "exclude_numeric_user_ids")
	if !ok {
		return
	}
	if d.Memory == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}

	filter := personinfo.PersonListFilter{
		Page:                  page,
		PageSize:              pageSize,
		Search:                strings.TrimSpace(search),
		Platform:              strings.TrimSpace(platform),
		UserID:                strings.TrimSpace(userID),
		ExcludeNumericUserIDs: excludeNumeric != nil && *excludeNumeric,
	}
	filter.IsKnown = isKnown
	rows, total, err := personinfo.DefaultRegistry().ListPersons(r.Context(), filter)
	if err != nil {
		if errors.Is(err, personinfo.ErrMemoryUnavailable) {
			writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化，无法读取人物列表")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "获取人物列表失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for i := range rows {
		data = append(data, personInfoToWebUI(&rows[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"data":      data,
	})
}

func (d Deps) handlePersonDetail(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	personID := strings.TrimSpace(chi.URLParam(r, "person_id"))
	if personID == "" {
		writeValidationError(w, "person_id 不能为空")
		return
	}
	if d.Memory == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		return
	}

	rec, err := personinfo.DefaultRegistry().GetPersonRecord(r.Context(), personID)
	if err != nil {
		switch {
		case errors.Is(err, personinfo.ErrPersonNotFound):
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到 ID 为 " + personID + " 的人物信息"})
		case errors.Is(err, personinfo.ErrMemoryUnavailable):
			writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		default:
			writeAPIError(w, http.StatusInternalServerError, "获取人物详情失败: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    personInfoToWebUI(rec),
	})
}

func (d Deps) handlePersonDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	personID := strings.TrimSpace(chi.URLParam(r, "person_id"))
	if personID == "" {
		writeValidationError(w, "person_id 不能为空")
		return
	}
	name, err := personinfo.DefaultRegistry().DeletePerson(r.Context(), personID)
	if err != nil {
		switch {
		case errors.Is(err, personinfo.ErrPersonNotFound):
			writeAPIError(w, http.StatusNotFound, "未找到 ID 为 "+personID+" 的人物信息")
		case errors.Is(err, personinfo.ErrMemoryUnavailable):
			writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
		default:
			writeAPIError(w, http.StatusInternalServerError, "删除人物信息失败: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "成功删除人物信息: " + name,
	})
}

func (d Deps) handlePersonStats(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	total, known, individuals, linked, platforms, err := personinfo.DefaultRegistry().PersonStats(r.Context())
	if err != nil {
		if errors.Is(err, personinfo.ErrMemoryUnavailable) {
			writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "获取统计数据失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"total":              total,
			"known":              known,
			"unknown":            total - known,
			"individuals":        individuals,
			"linked_individuals": linked,
			"platforms":          platforms,
		},
	})
}

func (d Deps) handlePersonBatchDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	const path = "/api/webui/person/batch/delete"
	var body struct {
		PersonIDs []string `json:"person_ids"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if len(body.PersonIDs) == 0 {
		writeAPIError(w, http.StatusBadRequest, "未提供要删除的人物ID")
		return
	}
	deleted, failedIDs := personinfo.DefaultRegistry().BatchDeletePersons(r.Context(), body.PersonIDs)
	if failedIDs == nil {
		failedIDs = []string{}
	}
	failedCount := len(failedIDs)
	message := "成功删除 " + strconv.Itoa(deleted) + " 个人物"
	if failedCount > 0 {
		message += "，" + strconv.Itoa(failedCount) + " 个失败"
	}
	_ = path
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       message,
		"deleted_count": deleted,
		"failed_count":  failedCount,
		"failed_ids":    failedIDs,
	})
}

func personInfoToWebUI(rec *personinfo.PersonInfo) map[string]any {
	return personProfileToWebUI(personinfo.ProfileFromRecord(rec))
}

func personProfileToWebUI(profile personinfo.Profile) map[string]any {
	var knowSince, lastKnow any
	if profile.FirstKnownTime != nil {
		knowSince = float64(profile.FirstKnownTime.UnixNano()) / 1e9
	}
	if profile.LastKnownTime != nil {
		lastKnow = float64(profile.LastKnownTime.UnixNano()) / 1e9
	}
	var groupNick any
	if profile.GroupCards != nil {
		groupNick = profile.GroupCards
	}
	return map[string]any{
		"id":                 profile.ID,
		"is_known":           profile.IsKnown,
		"person_id":          profile.PersonID,
		"individual_id":      profile.IndividualID,
		"source_person_name": profile.SourceName,
		"person_name":        profile.PersonName,
		"platform":           profile.Platform,
		"user_id":            profile.UserID,
		"nickname":           profile.Nickname,
		"group_nick_name":    groupNick,
		"know_times":         profile.KnowCounts,
		"know_since":         knowSince,
		"last_know":          lastKnow,
	}
}
