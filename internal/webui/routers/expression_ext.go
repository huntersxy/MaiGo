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

	"MaiGo/internal/learners"
)

var expressionSortValues = map[string]struct{}{"time": {}, "time_asc": {}, "created": {}, "created_asc": {}}
var expressionReviewFilterValues = map[string]struct{}{"all": {}, "user_checked": {}, "unchecked": {}}

func (d Deps) handleExpressionList(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/list"
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
	if _, valid := expressionSortValues[sortBy]; !valid {
		writeAPIError(w, http.StatusBadRequest, "不支持的表达方式排序: "+sortBy)
		return
	}
	reviewFilter := strings.TrimSpace(r.URL.Query().Get("review_filter"))
	if reviewFilter == "" {
		reviewFilter = "all"
	}
	if _, valid := expressionReviewFilterValues[reviewFilter]; !valid {
		writeAPIError(w, http.StatusBadRequest, "不支持的表达方式筛选: "+reviewFilter)
		return
	}
	includeLegacy, present, boolErr := optionalQueryBool(r, "include_legacy")
	if boolErr != nil {
		writeRequestValidationError(w, *boolErr)
		return
	}
	if !present {
		includeLegacy = false
	}
	chatIDs := r.URL.Query()["chat_ids"]
	if len(chatIDs) == 0 {
		if raw := strings.TrimSpace(r.URL.Query().Get("chat_ids")); raw != "" {
			chatIDs = []string{raw}
		}
	}
	if chatIDs != nil {
		chatIDs = splitCommaValues(chatIDs)
	}
	query := learners.ExpressionWebUIQuery{
		Page:          page,
		PageSize:      pageSize,
		Search:        strings.TrimSpace(r.URL.Query().Get("search")),
		ChatID:        strings.TrimSpace(r.URL.Query().Get("chat_id")),
		ChatIDs:       chatIDs,
		ReviewFilter:  reviewFilter,
		SortBy:        sortBy,
		IncludeLegacy: includeLegacy,
	}
	rows, total, err := store.ListExpressionRows(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取表达方式列表失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		data = append(data, expressionRowToWebUI(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"data":      data,
	})
}

// splitCommaValues 兼容重复 query 与逗号分隔两种 chat_ids 传法
func splitCommaValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			if item := strings.TrimSpace(part); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

func expressionRowToWebUI(row learners.ExpressionWebUIRow) map[string]any {
	modifiedBy := any(nil)
	if row.ModifiedBy != nil {
		modifiedBy = strings.ToLower(string(*row.ModifiedBy))
	}
	var createDate any
	if !row.CreateTime.IsZero() {
		createDate = float64(row.CreateTime.Unix())
	}
	var chatName any
	if row.ChatName != "" {
		chatName = row.ChatName
	}
	lastActiveTime := 0.0
	if !row.LastActiveTime.IsZero() {
		lastActiveTime = float64(row.LastActiveTime.Unix())
	}
	return map[string]any{
		"id":               row.ID,
		"situation":        row.Situation,
		"style":            row.Style,
		"last_active_time": lastActiveTime,
		"chat_id":          row.SessionID,
		"chat_name":        chatName,
		"create_date":      createDate,
		"checked":          row.Checked,
		"modified_by":      modifiedBy,
	}
}

func (d Deps) handleExpressionChats(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/chats"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	includeLegacy, present, boolErr := optionalQueryBool(r, "include_legacy")
	if boolErr != nil {
		writeRequestValidationError(w, *boolErr)
		return
	}
	if !present {
		includeLegacy = false
	}
	chats, err := store.ExpressionChats(r.Context(), includeLegacy)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取聊天列表失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(chats))
	for _, chat := range chats {
		data = append(data, expressionChatToWebUI(chat))
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": data})
}

func (d Deps) handleExpressionChatTargets(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/chat-targets"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	includeLegacy, present, boolErr := optionalQueryBool(r, "include_legacy")
	if boolErr != nil {
		writeRequestValidationError(w, *boolErr)
		return
	}
	if !present {
		includeLegacy = false
	}
	chats, err := store.ExpressionChatTargets(r.Context(), includeLegacy)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取导入目标聊天流失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(chats))
	for _, chat := range chats {
		data = append(data, expressionChatToWebUI(chat))
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": data})
}

func expressionChatToWebUI(chat learners.ChatInfo) map[string]any {
	var expressionCount any
	if chat.ExpressionCount != nil {
		expressionCount = *chat.ExpressionCount
	}
	return map[string]any{
		"chat_id":          chat.SessionID,
		"chat_name":        chat.ChatName,
		"platform":         nullableString(chat.Platform),
		"account_id":       nullableString(chat.AccountID),
		"target_id":        nullableString(chat.TargetID),
		"is_group":         chat.IsGroup,
		"is_legacy":        chat.IsLegacy,
		"is_other_account": chat.IsOtherAccount,
		"use_expression":   chat.UseExpression,
		"enable_learning":  chat.EnableLearning,
		"expression_count": expressionCount,
	}
}

func (d Deps) handleExpressionDetail(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/{expression_id}"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	id, ok := parsePathID(w, chi.URLParam(r, "expression_id"))
	if !ok {
		return
	}
	row, err := store.GetExpressionRow(r.Context(), id)
	if err != nil {
		if errors.Is(err, learners.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表达方式", id))
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "获取表达方式详情失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": expressionRowToWebUI(*row)})
}

func (d Deps) handleExpressionCreate(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var body struct {
		Situation *string `json:"situation"`
		Style     *string `json:"style"`
		ChatID    *string `json:"chat_id"`
		IsGlobal  bool    `json:"is_global"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if body.Situation == nil || body.Style == nil {
		writeValidationError(w, "situation 与 style 为必填字段")
		return
	}
	if body.IsGlobal && body.ChatID != nil && strings.TrimSpace(*body.ChatID) != "" {
		writeAPIError(w, http.StatusBadRequest, "全局新增不能同时指定聊天")
		return
	}
	var sessionID *string
	if !body.IsGlobal {
		chatID, ok := requireExistingChatID(store, w, r, derefString(body.ChatID))
		if !ok {
			return
		}
		sessionID = &chatID
	}
	row, err := store.CreateExpressionRow(r.Context(), learners.ExpressionCreateInput{
		Situation: *body.Situation,
		Style:     *body.Style,
		SessionID: sessionID,
		IsGlobal:  body.IsGlobal,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "创建表达方式失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "表达方式创建成功",
		"data":    expressionRowToWebUI(*row),
	})
}

func (d Deps) handleExpressionUpdate(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/{expression_id}"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	id, ok := parsePathID(w, chi.URLParam(r, "expression_id"))
	if !ok {
		return
	}
	var body struct {
		Situation *string `json:"situation"`
		Style     *string `json:"style"`
		ChatID    *string `json:"chat_id"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	update := learners.ExpressionUpdateInput{Situation: body.Situation, Style: body.Style}
	if body.ChatID != nil {
		chatID, ok := requireExistingChatID(store, w, r, *body.ChatID)
		if !ok {
			return
		}
		update.ChatID = &chatID
		update.HasChatID = true
	}
	if update.Situation == nil && update.Style == nil && !update.HasChatID {
		writeAPIError(w, http.StatusBadRequest, "未提供任何需要更新的字段")
		return
	}
	row, err := store.UpdateExpressionRow(r.Context(), id, update)
	if err != nil {
		if errors.Is(err, learners.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表达方式", id))
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "更新表达方式失败: "+err.Error())
		return
	}
	fieldCount := 0
	if update.Situation != nil {
		fieldCount++
	}
	if update.Style != nil {
		fieldCount++
	}
	if update.HasChatID {
		fieldCount++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": fmt.Sprintf("成功更新 %d 个字段", fieldCount+1),
		"data":    expressionRowToWebUI(*row),
	})
}

func (d Deps) handleExpressionDelete(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/{expression_id}"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	id, ok := parsePathID(w, chi.URLParam(r, "expression_id"))
	if !ok {
		return
	}
	row, err := store.GetExpressionRow(r.Context(), id)
	if err != nil {
		if errors.Is(err, learners.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表达方式", id))
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "删除表达方式失败: "+err.Error())
		return
	}
	if _, err := store.DeleteExpressionRows(r.Context(), []uint{id}); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "删除表达方式失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "成功删除表达方式: " + row.Situation,
	})
}

func (d Deps) handleExpressionBatchDelete(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/batch/delete"
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
		writeAPIError(w, http.StatusBadRequest, "未提供要删除的表达方式ID")
		return
	}
	deleted, err := store.DeleteExpressionRows(r.Context(), uniqueUintIDs(*body.IDs))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "批量删除表达方式失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": fmt.Sprintf("成功删除 %d 个表达方式", deleted),
	})
}

func (d Deps) handleExpressionReviewStatus(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/{expression_id}/review-status"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	id, ok := parsePathID(w, chi.URLParam(r, "expression_id"))
	if !ok {
		return
	}
	var body struct {
		Approved *bool `json:"approved"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if body.Approved == nil {
		writeValidationError(w, "approved 为必填字段")
		return
	}
	row, err := store.SetExpressionReviewStatus(r.Context(), id, *body.Approved)
	if err != nil {
		if errors.Is(err, learners.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到 ID 为 %d 的表达方式", id))
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "更新表达方式审核状态失败: "+err.Error())
		return
	}
	message := "已设为拒绝"
	if *body.Approved {
		message = "已设为人工通过"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": message,
		"data":    expressionRowToWebUI(*row),
	})
}

func (d Deps) handleExpressionStats(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/stats/summary"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	includeLegacy, present, boolErr := optionalQueryBool(r, "include_legacy")
	if boolErr != nil {
		writeRequestValidationError(w, *boolErr)
		return
	}
	if !present {
		includeLegacy = false
	}
	stats, err := store.ExpressionStats(r.Context(), includeLegacy)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取统计数据失败: "+err.Error())
		return
	}
	topChats := stats.TopChats
	if topChats == nil {
		topChats = map[string]int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"total":        stats.Total,
			"recent_7days": stats.Recent7Days,
			"chat_count":   stats.ChatCount,
			"top_chats":    topChats,
		},
	})
}

func (d Deps) handleExpressionReviewStats(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/review/stats"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	stats, err := store.ExpressionReviewStats(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取审核统计失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":        stats.Total,
		"unchecked":    stats.Unchecked,
		"passed":       stats.Passed,
		"ai_checked":   stats.AIChecked,
		"user_checked": stats.UserChecked,
	})
}

func (d Deps) handleExpressionReviewList(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/review/list"
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
	filterType := strings.TrimSpace(r.URL.Query().Get("filter_type"))
	if filterType == "" {
		filterType = "unchecked"
	}
	order := strings.TrimSpace(r.URL.Query().Get("order"))
	if order == "" {
		order = "latest"
	}
	excludeIDs, ok := parseQueryUintList(w, r, "exclude_ids")
	if !ok {
		return
	}
	query := learners.ReviewListQuery{
		Page:       page,
		PageSize:   pageSize,
		FilterType: filterType,
		Order:      order,
		Search:     strings.TrimSpace(r.URL.Query().Get("search")),
		ChatID:     strings.TrimSpace(r.URL.Query().Get("chat_id")),
		ExcludeIDs: excludeIDs,
	}
	rows, total, err := store.ExpressionReviewRows(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取审核列表失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		data = append(data, expressionRowToWebUI(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"data":      data,
	})
}

func (d Deps) handleExpressionReviewBatch(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/review/batch"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var body struct {
		Items *[]struct {
			ID               *uint `json:"id"`
			Approved         *bool `json:"approved"`
			RequireUnchecked *bool `json:"require_unchecked"`
		} `json:"items"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if body.Items == nil {
		writeValidationError(w, "items 为必填字段")
		return
	}
	if len(*body.Items) == 0 {
		writeAPIError(w, http.StatusBadRequest, "未提供要审核的表达方式")
		return
	}
	items := make([]learners.ReviewBatchItem, 0, len(*body.Items))
	for _, item := range *body.Items {
		if item.ID == nil {
			writeValidationError(w, "id 为必填字段")
			return
		}
		if item.Approved == nil {
			writeValidationError(w, "approved 为必填字段")
			return
		}
		items = append(items, learners.ReviewBatchItem{ID: *item.ID, Approved: *item.Approved})
	}
	results := store.BatchReviewExpressions(r.Context(), items)
	succeeded, failed := 0, 0
	payload := make([]map[string]any, 0, len(results))
	for _, result := range results {
		if result.Success {
			succeeded++
		} else {
			failed++
		}
		payload = append(payload, map[string]any{
			"id":      result.ID,
			"success": result.Success,
			"message": result.Message,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"total":     len(items),
		"succeeded": succeeded,
		"failed":    failed,
		"results":   payload,
	})
}

func (d Deps) handleExpressionExport(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/export"
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
	var body struct {
		ChatID  *string  `json:"chat_id"`
		ChatIDs []string `json:"chat_ids"`
		IDs     []uint   `json:"ids"`
	}
	if len(raw) > 0 {
		if err := sonic.ConfigDefault.Unmarshal(raw, &body); err != nil {
			writeValidationError(w, "请求格式错误")
			return
		}
	}
	query := learners.ExpressionExportQuery{
		HasChatID:  bodyHasKey(raw, "chat_id"),
		HasChatIDs: bodyHasKey(raw, "chat_ids"),
		IDs:        body.IDs,
		HasIDs:     bodyHasKey(raw, "ids"),
	}
	if query.HasChatID {
		chatID, err := requireNonEmptyChatID(derefString(body.ChatID))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		query.ChatID = chatID
	}
	if query.HasChatIDs {
		query.ChatIDs = body.ChatIDs
	}
	rows, err := store.ExportExpressions(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "导出表达方式失败: "+err.Error())
		return
	}
	if query.HasIDs {
		unique := uniqueUintIDs(query.IDs)
		if len(rows) != len(unique) {
			missing, err := store.MissingExpressionIDs(r.Context(), query)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "导出表达方式失败: "+err.Error())
				return
			}
			writeAPIError(w, http.StatusBadRequest, "部分表达方式不属于所选范围或不存在: "+formatIDList(missing))
			return
		}
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		modifiedBy := any(nil)
		if row.ModifiedBy != nil {
			modifiedBy = string(*row.ModifiedBy)
		}
		item := map[string]any{
			"situation":    row.Situation,
			"style":        row.Style,
			"content_list": row.ContentList,
			"count":        row.Count,
			"checked":      row.Checked,
			"modified_by":  modifiedBy,
		}
		if row.LastActiveTime != nil {
			item["last_active_time"] = formatPyDateTime(*row.LastActiveTime)
		}
		if row.CreateTime != nil {
			item["create_time"] = formatPyDateTime(*row.CreateTime)
		}
		items = append(items, item)
	}
	sourceChatName := ""
	if query.HasChatID {
		sourceChatName = store.ChatName(r.Context(), query.ChatID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"version":          1,
		"type":             "maibot.expression.export",
		"exported_at":      formatPyDateTime(time.Now()),
		"source_chat_name": sourceChatName,
		"count":            len(items),
		"expressions":      items,
	})
}

func (d Deps) handleExpressionImport(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/import"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var body struct {
		ChatID      *string `json:"chat_id"`
		IsGlobal    bool    `json:"is_global"`
		Expressions *[]struct {
			Situation      *string `json:"situation"`
			Style          *string `json:"style"`
			ContentList    string  `json:"content_list"`
			Count          int     `json:"count"`
			LastActiveTime *string `json:"last_active_time"`
			CreateTime     *string `json:"create_time"`
			Checked        bool    `json:"checked"`
			ModifiedBy     *string `json:"modified_by"`
		} `json:"expressions"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if body.IsGlobal && body.ChatID != nil && strings.TrimSpace(*body.ChatID) != "" {
		writeAPIError(w, http.StatusBadRequest, "全局导入不能同时指定聊天")
		return
	}
	var sessionID *string
	if !body.IsGlobal {
		chatID, ok := requireExistingChatID(store, w, r, derefString(body.ChatID))
		if !ok {
			return
		}
		sessionID = &chatID
	}
	if body.Expressions == nil {
		writeValidationError(w, "expressions 为必填字段")
		return
	}
	if len(*body.Expressions) == 0 {
		writeAPIError(w, http.StatusBadRequest, "导入文件中没有表达方式")
		return
	}
	items := make([]learners.ExpressionImportItem, 0, len(*body.Expressions))
	for _, item := range *body.Expressions {
		if item.Situation == nil {
			writeValidationError(w, "expressions.situation 为必填字段")
			return
		}
		if item.Style == nil {
			writeValidationError(w, "expressions.style 为必填字段")
			return
		}
		items = append(items, learners.ExpressionImportItem{
			Situation:      *item.Situation,
			Style:          *item.Style,
			ContentList:    item.ContentList,
			Count:          item.Count,
			LastActiveTime: parsePyDateTime(item.LastActiveTime),
			CreateTime:     parsePyDateTime(item.CreateTime),
			Checked:        item.Checked,
			ModifiedBy:     parseModifiedBy(item.ModifiedBy),
		})
	}
	result, err := store.ImportExpressions(r.Context(), sessionID, items)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "导入表达方式失败: "+err.Error())
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

func (d Deps) handleExpressionClear(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/clear"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var body struct {
		ChatID *string `json:"chat_id"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	chatID, err := requireNonEmptyChatID(derefString(body.ChatID))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	deleted, err := store.ClearExpressionsBySession(r.Context(), chatID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "清除表达方式失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       fmt.Sprintf("成功清除 %d 个表达方式", deleted),
		"deleted_count": deleted,
	})
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// parseQueryUintList 解析重复 query 参数或逗号分隔的整数列表
func parseQueryUintList(w http.ResponseWriter, r *http.Request, key string) ([]uint, bool) {
	rawValues := r.URL.Query()[key]
	if len(rawValues) == 0 {
		return nil, true
	}
	out := make([]uint, 0, len(rawValues))
	for _, raw := range splitCommaValues(rawValues) {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeValidationError(w, key+" 必须是整数列表")
			return nil, false
		}
		out = append(out, uint(value))
	}
	return out, true
}

// parseModifiedBy 对齐 parse_modified_by：容忍 JSON 引号包裹，非法值返回 nil
func parseModifiedBy(raw *string) *learners.ModifiedBy {
	if raw == nil {
		return nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		var decoded string
		if err := sonic.ConfigDefault.Unmarshal([]byte(value), &decoded); err == nil {
			value = strings.TrimSpace(decoded)
		}
	}
	normalized := learners.ModifiedBy(strings.ToUpper(value))
	if normalized != learners.ModifiedByAI && normalized != learners.ModifiedByUser {
		return nil
	}
	return &normalized
}

// parsePyDateTime 解析导出文件中的 ISO 时间，失败时返回当前时间
func parsePyDateTime(raw *string) *time.Time {
	now := time.Now()
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return &now
	}
	value := strings.TrimSpace(*raw)
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return &now
}

// formatPyDateTime 对齐 Python datetime.isoformat（无时区、微秒省略）
func formatPyDateTime(value time.Time) string {
	return value.Format("2006-01-02T15:04:05.999999")
}
