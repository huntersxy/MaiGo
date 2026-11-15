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

const expressionReviewLogMaxLimit = 200

func expressionReviewLogTimestamp(value any) float64 {
	text := strings.TrimSpace(expressionAnyText(value))
	if text == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999-07:00"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return float64(parsed.Unix())
		}
	}
	return 0
}

func expressionReviewLogOptionalInt(value any) any {
	text := strings.TrimSpace(expressionAnyText(value))
	if text == "" {
		return nil
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return nil
	}
	return parsed
}

func (d Deps) expressionReviewLogPayload(r *http.Request, store learners.WebUIStore, entry map[string]any) map[string]any {
	sessionID := strings.TrimSpace(expressionAnyText(entry["session_id"]))
	var chatName any
	if sessionID != "" && store != nil {
		if name := strings.TrimSpace(store.ChatName(r.Context(), sessionID)); name != "" {
			chatName = name
		}
	}
	var rescuedAt any
	if strings.TrimSpace(expressionAnyText(entry["rescued_at"])) != "" {
		rescuedAt = expressionReviewLogTimestamp(entry["rescued_at"])
	}
	var errorText any
	if text := expressionAnyText(entry["error"]); text != "" {
		errorText = text
	}
	return map[string]any{
		"id":                    strings.TrimSpace(expressionAnyText(entry["id"])),
		"created_at":            expressionReviewLogTimestamp(entry["created_at"]),
		"expression_id":         expressionReviewLogOptionalInt(entry["expression_id"]),
		"session_id":            sessionID,
		"chat_name":             chatName,
		"passed":                boolValue(entry["passed"]),
		"reason":                expressionAnyText(entry["reason"]),
		"situation":             expressionAnyText(entry["situation"]),
		"style":                 expressionAnyText(entry["style"]),
		"source":                expressionAnyText(entry["source"]),
		"error":                 errorText,
		"rescued":               boolValue(entry["rescued"]),
		"rescued_expression_id": expressionReviewLogOptionalInt(entry["rescued_expression_id"]),
		"rescued_at":            rescuedAt,
	}
}

func (d Deps) handleExpressionReviewLogs(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/review/logs"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > expressionReviewLogMaxLimit {
			writeValidationError(w, fmt.Sprintf("limit 必须在 1 到 %d 之间", expressionReviewLogMaxLimit))
			return
		}
		limit = parsed
	}
	var passed *bool
	if raw := strings.TrimSpace(r.URL.Query().Get("passed")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeValidationError(w, "passed 必须是布尔值")
			return
		}
		passed = &value
	}
	entries, err := learners.DefaultExpressionReviewStore().GetRecentAIReviewLogs(limit, passed, strings.TrimSpace(r.URL.Query().Get("chat_id")))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取表达方式 AI 审核日志失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		data = append(data, d.expressionReviewLogPayload(r, store, entry))
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total": len(data), "data": data})
}

func (d Deps) handleExpressionReviewLogApprove(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/review/logs/{review_log_id}/approve"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	reviewLogID := strings.TrimSpace(chi.URLParam(r, "review_log_id"))
	logStore := learners.DefaultExpressionReviewStore()
	reviewLog, err := logStore.GetAIReviewLog(reviewLogID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "从表达方式 AI 审核日志恢复失败: "+err.Error())
		return
	}
	if reviewLog == nil {
		writeAPIError(w, http.StatusNotFound, "未找到审核日志: "+reviewLogID)
		return
	}
	sessionID := strings.TrimSpace(expressionAnyText(reviewLog["session_id"]))
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "缺少聊天流 ID")
		return
	}
	situation := strings.TrimSpace(expressionAnyText(reviewLog["situation"]))
	style := strings.TrimSpace(expressionAnyText(reviewLog["style"]))
	if situation == "" || style == "" {
		writeAPIError(w, http.StatusBadRequest, "审核日志缺少表达方式内容，无法恢复")
		return
	}
	var row *learners.ExpressionWebUIRow
	if expressionID := intValue(reviewLog["expression_id"], 0); expressionID > 0 {
		existing, err := store.GetExpressionRow(r.Context(), uint(expressionID))
		if err != nil && !errors.Is(err, learners.ErrNotFound) {
			writeAPIError(w, http.StatusInternalServerError, "从表达方式 AI 审核日志恢复失败: "+err.Error())
			return
		}
		row = existing
	}
	if row == nil {
		page := 1
		for row == nil {
			rows, total, err := store.ListExpressionRows(r.Context(), learners.ExpressionWebUIQuery{
				Page: page, PageSize: 100, ChatID: sessionID, ReviewFilter: "all", SortBy: "time", IncludeLegacy: true,
			})
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "从表达方式 AI 审核日志恢复失败: "+err.Error())
				return
			}
			for index := range rows {
				if strings.TrimSpace(rows[index].Situation) == situation && strings.TrimSpace(rows[index].Style) == style {
					row = &rows[index]
					break
				}
			}
			if row != nil || page*100 >= total {
				break
			}
			page++
		}
	}
	created := false
	if row == nil {
		created = true
		contentList, _ := sonic.Marshal([]string{situation})
		modifiedBy := learners.ModifiedByUser
		createdRow, err := store.CreateExpressionRow(r.Context(), learners.ExpressionCreateInput{
			Situation:   situation,
			Style:       style,
			SessionID:   &sessionID,
			ContentList: string(contentList),
			Count:       1,
			Checked:     true,
			ModifiedBy:  &modifiedBy,
		})
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "从表达方式 AI 审核日志恢复失败: "+err.Error())
			return
		}
		row = createdRow
	}
	row, err = store.SetExpressionReviewStatus(r.Context(), row.ID, true)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "从表达方式 AI 审核日志恢复失败: "+err.Error())
		return
	}
	if _, err := logStore.AppendManualRescueLog(reviewLogID, row.ID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "从表达方式 AI 审核日志恢复失败: "+err.Error())
		return
	}
	message := "已设为人工审核通过"
	if created {
		message = "已从 AI 审核日志救回表达方式并设为人工通过"
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": message, "data": expressionRowToWebUI(*row)})
}
