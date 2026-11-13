package webuiapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"maibot-go/internal/learners"
)

// 黑话列表排序与状态筛选的合法取值，对齐 Python Literal 校验
var (
	jargonSortValues   = map[string]struct{}{"time": {}, "time_asc": {}, "created": {}, "created_asc": {}}
	jargonStatusValues = map[string]struct{}{
		"confirmed_jargon": {}, "confirmed_not_jargon": {}, "manual_jargon": {}, "pending": {},
	}
)

func (d Deps) handleJargonList(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/list"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	page, pageSize, ok := queryPagination(w, r, 20)
	if !ok {
		return
	}
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort_by"))
	if sortBy == "" {
		sortBy = "time"
	}
	if _, valid := jargonSortValues[sortBy]; !valid {
		writeAPIError(w, http.StatusBadRequest, "不支持的黑话排序: "+sortBy)
		return
	}
	query := learners.JargonWebUIQuery{
		Page:       page,
		PageSize:   pageSize,
		Search:     strings.TrimSpace(r.URL.Query().Get("search")),
		SessionID:  strings.TrimSpace(r.URL.Query().Get("session_id")),
		SessionIDs: r.URL.Query()["session_ids"],
		SortBy:     sortBy,
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("jargon_status")); raw != "" {
		if _, valid := jargonStatusValues[raw]; !valid {
			writeValidationError(w, "jargon_status 取值无效")
			return
		}
		query.JargonStatus = raw
	}
	for _, key := range []string{"is_jargon", "is_complete", "is_global"} {
		value, ok := optionalQueryBoolPtr(w, r, key)
		if !ok {
			return
		}
		switch key {
		case "is_jargon":
			query.IsJargon = value
		case "is_complete":
			query.IsComplete = value
		case "is_global":
			query.IsGlobal = value
		}
	}
	rows, total, err := store.ListJargonRows(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取黑话列表失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		data = append(data, jargonRowToWebUI(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"data":      data,
	})
}

func jargonRowToWebUI(row learners.JargonWebUIRow) map[string]any {
	sessionID := ""
	if len(row.SessionIDs) > 0 {
		sessionID = row.SessionIDs[0]
	}
	var chatName any
	if row.ChatName != "" {
		chatName = row.ChatName
	}
	sessionIDs := row.SessionIDs
	if sessionIDs == nil {
		sessionIDs = []string{}
	}
	chatNames := row.ChatNames
	if chatNames == nil {
		chatNames = []string{}
	}
	return map[string]any{
		"id":                      row.ID,
		"content":                 row.Content,
		"meaning":                 row.Meaning,
		"session_id":              sessionID,
		"session_ids":             sessionIDs,
		"chat_name":               chatName,
		"chat_names":              chatNames,
		"count":                   row.Count,
		"is_jargon":               row.IsJargon,
		"is_legacy_empty_meaning": row.IsLegacyEmptyMeaning,
		"is_complete":             row.IsComplete,
		"is_global":               row.IsGlobal,
		"created_by":              string(row.CreatedBy),
		"created_timestamp":       row.CreatedTimestamp.UTC().Format(time.RFC3339),
		"updated_timestamp":       row.UpdatedTimestamp.UTC().Format(time.RFC3339),
	}
}

func (d Deps) handleJargonChats(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/chats"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	includeEmpty, present, boolErr := optionalQueryBool(r, "include_empty")
	if boolErr != nil {
		writeRequestValidationError(w, *boolErr)
		return
	}
	if !present {
		includeEmpty = false
	}
	chats, err := store.JargonChats(r.Context(), includeEmpty)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取聊天列表失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(chats))
	for _, chat := range chats {
		data = append(data, jargonChatToWebUI(chat))
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": data})
}

func jargonChatToWebUI(chat learners.JargonChat) map[string]any {
	return map[string]any{
		"session_id":   chat.SessionID,
		"chat_name":    chat.ChatName,
		"platform":     nullableString(chat.Platform),
		"account_id":   nullableString(chat.AccountID),
		"target_id":    nullableString(chat.TargetID),
		"is_group":     chat.IsGroup,
		"jargon_count": chat.JargonCount,
	}
}

func (d Deps) handleJargonStats(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/stats/summary"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	stats, err := store.JargonStats(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取黑话统计失败: "+err.Error())
		return
	}
	topChats := stats.TopChats
	if topChats == nil {
		topChats = map[string]int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"total":                stats.Total,
			"confirmed_jargon":     stats.ConfirmedJargon,
			"confirmed_not_jargon": stats.ConfirmedNotJargon,
			"pending":              stats.Pending,
			"manual_jargon":        stats.ManualJargon,
			"global_count":         stats.GlobalCount,
			"complete_count":       stats.CompleteCount,
			"chat_count":           stats.ChatCount,
			"top_chats":            topChats,
		},
	})
}

type jargonExportRequestBody struct {
	IDs             []uint   `json:"ids"`
	SessionIDs      []string `json:"session_ids"`
	IncludeChatInfo bool     `json:"include_chat_info"`
	idsPresent      bool
	sessionPresent  bool
}

func (d Deps) handleJargonExport(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/export"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	raw, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	var req jargonExportRequestBody
	if len(raw) > 0 {
		if err := sonic.ConfigDefault.Unmarshal(raw, &req); err != nil {
			writeValidationError(w, "请求格式错误")
			return
		}
	}
	req.idsPresent = bodyHasKey(raw, "ids")
	req.sessionPresent = bodyHasKey(raw, "session_ids")
	query := learners.JargonExportQuery{
		IDs:             req.IDs,
		HasIDs:          req.idsPresent,
		SessionIDs:      req.SessionIDs,
		HasSessionIDs:   req.sessionPresent,
		IncludeChatInfo: req.IncludeChatInfo,
	}
	rows, err := store.ExportJargons(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "导出黑话失败: "+err.Error())
		return
	}
	if query.HasIDs {
		unique := uniqueUintIDs(query.IDs)
		if len(rows) != len(unique) {
			missing, err := store.MissingJargonIDs(r.Context(), query)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "导出黑话失败: "+err.Error())
				return
			}
			writeAPIError(w, http.StatusBadRequest, "部分黑话不存在: "+formatIDList(missing))
			return
		}
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{
			"content":     row.Content,
			"meaning":     row.Meaning,
			"count":       row.Count,
			"is_jargon":   row.IsJargon,
			"is_complete": row.IsComplete,
			"is_global":   row.IsGlobal,
			"created_by":  string(row.CreatedBy),
			"targets":     nil,
		}
		if query.IncludeChatInfo {
			targets := make([]map[string]any, 0, len(row.Targets))
			for _, target := range row.Targets {
				targets = append(targets, map[string]any{
					"platform":   target.Platform,
					"id":         target.ID,
					"type":       target.Type,
					"account_id": nullableString(target.AccountID),
					"scope":      nullableString(target.Scope),
					"count":      target.Count,
				})
			}
			item["targets"] = targets
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"version":           1,
		"type":              "maibot.jargon.export",
		"exported_at":       time.Now().Format(time.RFC3339),
		"include_chat_info": query.IncludeChatInfo,
		"count":             len(items),
		"jargons":           items,
	})
}

type jargonImportBody struct {
	TargetSessionIDs []string `json:"target_session_ids"`
	IsGlobal         bool     `json:"is_global"`
	ConflictStrategy string   `json:"conflict_strategy"`
	Jargons          *[]struct {
		Content    *string `json:"content"`
		Meaning    string  `json:"meaning"`
		Count      int     `json:"count"`
		IsJargon   bool    `json:"is_jargon"`
		IsComplete bool    `json:"is_complete"`
		IsGlobal   bool    `json:"is_global"`
	} `json:"jargons"`
}

func (d Deps) handleJargonImport(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/import"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var body jargonImportBody
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if body.ConflictStrategy == "" {
		body.ConflictStrategy = "skip"
	}
	if body.ConflictStrategy != "skip" && body.ConflictStrategy != "overwrite" {
		writeValidationError(w, "conflict_strategy 仅支持 skip 或 overwrite")
		return
	}
	if body.IsGlobal && len(body.TargetSessionIDs) > 0 {
		writeAPIError(w, http.StatusBadRequest, "全局导入不能同时指定聊天")
		return
	}
	var targets []string
	if !body.IsGlobal {
		normalized, ok := requireExistingSessionIDs(store, w, r, body.TargetSessionIDs)
		if !ok {
			return
		}
		targets = normalized
	}
	if body.Jargons == nil {
		writeValidationError(w, "jargons 为必填字段")
		return
	}
	if len(*body.Jargons) == 0 {
		writeAPIError(w, http.StatusBadRequest, "导入文件中没有黑话")
		return
	}
	items := make([]learners.JargonImportItem, 0, len(*body.Jargons))
	for _, item := range *body.Jargons {
		if item.Content == nil {
			writeValidationError(w, "jargons.content 为必填字段")
			return
		}
		items = append(items, learners.JargonImportItem{
			Content:    *item.Content,
			Meaning:    item.Meaning,
			Count:      item.Count,
			IsJargon:   item.IsJargon,
			IsComplete: item.IsComplete,
			IsGlobal:   item.IsGlobal,
		})
	}
	result, err := store.ImportJargons(r.Context(), learners.JargonImportInput{
		TargetSessionIDs: targets,
		IsGlobal:         body.IsGlobal,
		Items:            items,
		ConflictStrategy: body.ConflictStrategy,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "导入黑话失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"message":        fmt.Sprintf("导入完成：成功 %d 个，跳过 %d 个，失败 %d 个", result.Imported, result.Skipped, result.Failed),
		"imported_count": result.Imported,
		"skipped_count":  result.Skipped,
		"failed_count":   result.Failed,
	})
}

func (d Deps) handleJargonDetail(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/{jargon_id}"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	id, ok := parsePathID(w, chi.URLParam(r, "jargon_id"))
	if !ok {
		return
	}
	row, err := store.GetJargonRow(r.Context(), id)
	if err != nil {
		if errors.Is(err, learners.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "黑话不存在")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "获取黑话详情失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": jargonRowToWebUI(*row)})
}

type jargonCreateBody struct {
	Content    *string  `json:"content"`
	Meaning    *string  `json:"meaning"`
	SessionID  *string  `json:"session_id"`
	SessionIDs []string `json:"session_ids"`
	IsGlobal   bool     `json:"is_global"`
}

func (d Deps) handleJargonCreate(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var body jargonCreateBody
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if body.Content == nil {
		writeValidationError(w, "content 为必填字段")
		return
	}
	content := strings.TrimSpace(*body.Content)
	if content == "" {
		writeAPIError(w, http.StatusBadRequest, "黑话内容不能为空")
		return
	}
	meaning := ""
	if body.Meaning != nil {
		meaning = strings.TrimSpace(*body.Meaning)
	}
	rawSessionIDs := body.SessionIDs
	if body.SessionIDs == nil && body.SessionID != nil && strings.TrimSpace(*body.SessionID) != "" {
		rawSessionIDs = []string{*body.SessionID}
	}
	var sessionIDs []string
	if len(rawSessionIDs) > 0 || !body.IsGlobal {
		normalized, ok := requireExistingSessionIDs(store, w, r, rawSessionIDs)
		if !ok {
			return
		}
		sessionIDs = normalized
	}
	row, err := store.CreateJargonRow(r.Context(), learners.JargonCreateInput{
		Content:    content,
		Meaning:    meaning,
		SessionIDs: sessionIDs,
		IsGlobal:   body.IsGlobal,
	})
	if err != nil {
		switch {
		case errors.Is(err, learners.ErrJargonConflict):
			writeAPIError(w, http.StatusBadRequest, "该范围中已存在相同内容的手动黑话")
		case errors.Is(err, learners.ErrJargonContentEmpty):
			writeAPIError(w, http.StatusBadRequest, "黑话内容不能为空")
		default:
			writeAPIError(w, http.StatusInternalServerError, "创建黑话失败: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "创建成功",
		"data":    jargonRowToWebUI(*row),
	})
}

type jargonUpdateBody struct {
	Content    *string  `json:"content"`
	Meaning    *string  `json:"meaning"`
	SessionID  *string  `json:"session_id"`
	SessionIDs []string `json:"session_ids"`
	IsGlobal   *bool    `json:"is_global"`
	IsJargon   *bool    `json:"is_jargon"`
	CreatedBy  *string  `json:"created_by"`
}

func (d Deps) handleJargonUpdate(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/{jargon_id}"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	id, ok := parsePathID(w, chi.URLParam(r, "jargon_id"))
	if !ok {
		return
	}
	raw, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	if len(raw) == 0 {
		writeValidationError(w, "请求格式错误")
		return
	}
	var body jargonUpdateBody
	if err := sonic.ConfigDefault.Unmarshal(raw, &body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	update := learners.JargonUpdateInput{
		Content:     body.Content,
		Meaning:     body.Meaning,
		HasMeaning:  bodyHasKey(raw, "meaning"),
		IsGlobal:    body.IsGlobal,
		IsJargon:    body.IsJargon,
		HasIsJargon: bodyHasKey(raw, "is_jargon"),
	}
	if body.SessionIDs != nil {
		normalized, ok := requireExistingSessionIDs(store, w, r, body.SessionIDs)
		if !ok {
			return
		}
		update.SessionIDs = normalized
		update.HasSessionIDs = true
	} else if body.SessionID != nil && strings.TrimSpace(*body.SessionID) != "" {
		normalized, ok := requireExistingSessionIDs(store, w, r, []string{*body.SessionID})
		if !ok {
			return
		}
		update.SessionID = &normalized[0]
		update.HasSessionID = true
	}
	if body.CreatedBy != nil {
		createdBy := learners.JargonCreatedBy(strings.ToUpper(strings.TrimSpace(*body.CreatedBy)))
		if createdBy != learners.JargonCreatedByManual && createdBy != learners.JargonCreatedByAI {
			writeValidationError(w, "created_by 仅支持 AI 或 MANUAL")
			return
		}
		update.CreatedBy = &createdBy
	}
	row, err := store.UpdateJargonRow(r.Context(), id, update)
	if err != nil {
		switch {
		case errors.Is(err, learners.ErrNotFound):
			writeAPIError(w, http.StatusNotFound, "黑话不存在")
		case errors.Is(err, learners.ErrJargonContentEmpty):
			writeAPIError(w, http.StatusBadRequest, "黑话内容不能为空")
		default:
			writeAPIError(w, http.StatusInternalServerError, "更新黑话失败: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "更新成功",
		"data":    jargonRowToWebUI(*row),
	})
}

func (d Deps) handleJargonDelete(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/{jargon_id}"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	id, ok := parsePathID(w, chi.URLParam(r, "jargon_id"))
	if !ok {
		return
	}
	if _, err := store.GetJargonRow(r.Context(), id); err != nil {
		if errors.Is(err, learners.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "黑话不存在")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "删除黑话失败: "+err.Error())
		return
	}
	deleted, err := store.DeleteJargonRows(r.Context(), []uint{id})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "删除黑话失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       "删除成功",
		"deleted_count": deleted,
	})
}

func (d Deps) handleJargonBatchDelete(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/batch/delete"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var body struct {
		IDs *[]uint `json:"ids"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if body.IDs == nil {
		writeValidationError(w, "ids 为必填字段")
		return
	}
	if len(*body.IDs) == 0 {
		writeAPIError(w, http.StatusBadRequest, "ID列表不能为空")
		return
	}
	deleted, err := store.DeleteJargonRows(r.Context(), *body.IDs)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "批量删除黑话失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       fmt.Sprintf("成功删除 %d 条黑话", deleted),
		"deleted_count": deleted,
	})
}

func (d Deps) handleJargonBatchSetJargon(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/jargon/batch/set-jargon"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	rawIDs, present := r.URL.Query()["ids"]
	if !present {
		writeValidationError(w, "ids 为必填字段")
		return
	}
	if len(rawIDs) == 0 {
		writeAPIError(w, http.StatusBadRequest, "ID列表不能为空")
		return
	}
	ids := make([]uint, 0, len(rawIDs))
	for _, raw := range rawIDs {
		value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		if err != nil || value == 0 {
			writeValidationError(w, "ids 必须是正整数")
			return
		}
		ids = append(ids, uint(value))
	}
	isJargon, present, boolErr := optionalQueryBool(r, "is_jargon")
	if boolErr != nil {
		writeRequestValidationError(w, *boolErr)
		return
	}
	if !present {
		writeValidationError(w, "缺少或无效的 is_jargon 参数")
		return
	}
	updated, err := store.SetJargonStatus(r.Context(), ids, isJargon)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "批量更新黑话状态失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": fmt.Sprintf("成功更新 %d 条黑话状态", updated),
		"data":    nil,
	})
}

// requireExistingSessionIDs 校验并去重聊天流 ID，顺序保持不变
