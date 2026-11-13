package webuiapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"gorm.io/gorm"

	"maibot-go/internal/person_info"
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

	if db := d.personDB(); db != nil {
		data, total, err := listPersonsPythonShape(r.Context(), db, page, pageSize, search, isKnown, platform, userID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "获取人物列表失败: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":   true,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
			"data":      data,
		})
		return
	}

	filter := personinfo.PersonListFilter{
		Page:     page,
		PageSize: pageSize,
		Search:   strings.TrimSpace(search),
		Platform: strings.TrimSpace(platform),
		UserID:   strings.TrimSpace(userID),
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
	if db := d.personDB(); db != nil {
		record, err := getPersonPythonShape(r.Context(), db, personID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "获取人物详情失败: "+err.Error())
			return
		}
		if record == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到 ID 为 " + personID + " 的人物信息"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": record})
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
	total, known, platforms, err := personinfo.DefaultRegistry().PersonStats(r.Context())
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
			"total":     total,
			"known":     known,
			"unknown":   total - known,
			"platforms": platforms,
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

// personInfoToWebUI 对齐 person_to_response 字段
func personInfoToWebUI(rec *personinfo.PersonInfo) map[string]any {
	p := personinfo.ProfileFromRecord(rec)
	var knowSince, lastKnow any
	if p.FirstKnownTime != nil {
		knowSince = float64(p.FirstKnownTime.Unix())
	}
	if p.LastKnownTime != nil {
		lastKnow = float64(p.LastKnownTime.Unix())
	}
	var memoryPoints any
	if raw := profRawMemoryPoints(rec); raw != "" {
		memoryPoints = raw
	}
	groupNick := p.GroupCards
	if groupNick == nil {
		groupNick = []personinfo.GroupCardEntry{}
	}
	return map[string]any{
		"id":              rec.ID,
		"is_known":        p.IsKnown,
		"person_id":       p.PersonID,
		"person_name":     p.PersonName,
		"name_reason":     p.NameReason,
		"platform":        p.Platform,
		"user_id":         p.UserID,
		"nickname":        p.Nickname,
		"group_nick_name": groupNick,
		"memory_points":   memoryPoints,
		"know_times":      p.KnowCounts,
		"know_since":      knowSince,
		"last_know":       lastKnow,
	}
}

// profRawMemoryPoints 返回数据库中保存的 memory_points JSON 原文
func profRawMemoryPoints(rec *personinfo.PersonInfo) string {
	if rec == nil || rec.MemoryPoints == nil || *rec.MemoryPoints == "" {
		return ""
	}
	var items []string
	if err := json.Unmarshal([]byte(*rec.MemoryPoints), &items); err != nil {
		return ""
	}
	if len(items) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	return string(raw)
}

// personDB 返回人物信息直读所需的数据库；Memory 未接线时回退旧 Registry 路径
func (d Deps) personDB() *gorm.DB {
	if d.Memory == nil {
		return nil
	}
	return d.Memory.GetDB()
}

// pythonPersonRow 以可空指针承接 person_info 原始列，避免 NOT NULL 模型吞掉 NULL
// 对齐 Python person_to_response 的字段可空性
type pythonPersonRow struct {
	ID             uint       `gorm:"column:id"`
	IsKnown        *bool      `gorm:"column:is_known"`
	PersonID       *string    `gorm:"column:person_id"`
	PersonName     *string    `gorm:"column:person_name"`
	NameReason     *string    `gorm:"column:name_reason"`
	Platform       *string    `gorm:"column:platform"`
	UserID         *string    `gorm:"column:user_id"`
	UserNickname   *string    `gorm:"column:user_nickname"`
	GroupCardname  *string    `gorm:"column:group_cardname"`
	MemoryPoints   *string    `gorm:"column:memory_points"`
	KnowCounts     *int       `gorm:"column:know_counts"`
	FirstKnownTime *time.Time `gorm:"column:first_known_time"`
	LastKnownTime  *time.Time `gorm:"column:last_known_time"`
}

func applyPythonPersonFilter(q *gorm.DB, search string, isKnown *bool, platform, userID string) *gorm.DB {
	if search != "" {
		like := "%" + search + "%"
		q = q.Where("person_name LIKE ? OR user_nickname LIKE ? OR user_id LIKE ?", like, like, like)
	}
	if isKnown != nil {
		q = q.Where("is_known = ?", *isKnown)
	}
	if platform != "" {
		q = q.Where("platform = ?", platform)
	}
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	return q
}

func listPersonsPythonShape(ctx context.Context, db *gorm.DB, page, pageSize int, search string, isKnown *bool, platform, userID string) ([]map[string]any, int, error) {
	base := applyPythonPersonFilter(db.WithContext(ctx).Table("person_info"), search, isKnown, platform, userID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []pythonPersonRow
	if err := base.
		Order("CASE WHEN last_known_time IS NULL THEN 1 ELSE 0 END").
		Order("last_known_time DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	data := make([]map[string]any, 0, len(rows))
	for index := range rows {
		data = append(data, pythonPersonResponse(&rows[index]))
	}
	return data, int(total), nil
}

func getPersonPythonShape(ctx context.Context, db *gorm.DB, personID string) (map[string]any, error) {
	var rows []pythonPersonRow
	if err := db.WithContext(ctx).Table("person_info").
		Where("person_id = ?", personID).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return pythonPersonResponse(&rows[0]), nil
}

func nullableStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTimestampValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return float64(value.UnixNano()) / 1e9
}

// pythonPersonResponse 对齐 Python person.py:117-143 person_to_response
func pythonPersonResponse(row *pythonPersonRow) map[string]any {
	var groupNick any
	if row.GroupCardname != nil && *row.GroupCardname != "" {
		var parsed any
		if err := json.Unmarshal([]byte(*row.GroupCardname), &parsed); err == nil {
			groupNick = parsed
		}
	}
	personID := ""
	if row.PersonID != nil {
		personID = *row.PersonID
	}
	platform := ""
	if row.Platform != nil {
		platform = *row.Platform
	}
	userID := ""
	if row.UserID != nil {
		userID = *row.UserID
	}
	isKnown := false
	if row.IsKnown != nil {
		isKnown = *row.IsKnown
	}
	var knowTimes any
	if row.KnowCounts != nil {
		knowTimes = *row.KnowCounts
	}
	return map[string]any{
		"id":              row.ID,
		"is_known":        isKnown,
		"person_id":       personID,
		"person_name":     nullableStringValue(row.PersonName),
		"name_reason":     nullableStringValue(row.NameReason),
		"platform":        platform,
		"user_id":         userID,
		"nickname":        nullableStringValue(row.UserNickname),
		"group_nick_name": groupNick,
		"memory_points":   nullableStringValue(row.MemoryPoints),
		"know_times":      knowTimes,
		"know_since":      nullableTimestampValue(row.FirstKnownTime),
		"last_know":       nullableTimestampValue(row.LastKnownTime),
	}
}
