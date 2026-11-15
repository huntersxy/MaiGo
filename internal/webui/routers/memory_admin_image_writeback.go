package webuiapi

// 图片回填、写回任务列表与重试

import (
	"context"
	"errors"
	"fmt"
	"MaiGo/internal/webui/routers/chat"
	"net/http"
	"strconv"
	"strings"

	"gorm.io/gorm"

	imagememory "MaiGo/internal/A_memorix/core/image"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/common/database"
	"MaiGo/internal/services/memory_writeback"
)

func (d Deps) imageWritebackJournalPath() string {
	return memorywriteback.ImageWritebackJournalPath(d.InstanceDir)
}

func (d Deps) openImageWritebackJournal() (*imagememory.WritebackJournal, error) {
	return imagememory.OpenWritebackJournal(d.imageWritebackJournalPath())
}

func (d Deps) handleMemoryImageWritebackJobs(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 25, 1, 200)
	if !ok {
		return
	}
	offset, ok := mqInt(w, r, "offset", 0, 0, 1_000_000)
	if !ok {
		return
	}
	journal, err := d.openImageWritebackJournal()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer journal.Close()
	payload, err := journal.ListJobs(mqString(r, "status"), limit, offset)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	items, _ := payload["items"].([]map[string]any)
	sessionIDs := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		sessionID := anyTrimmed(item["session_id"])
		if sessionID == "" {
			continue
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		sessionIDs = append(sessionIDs, sessionID)
	}
	names, err := d.memoryChatDisplayNames(r.Context(), sessionIDs)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	for _, item := range items {
		sessionID := anyTrimmed(item["session_id"])
		if name, exists := names[sessionID]; exists && name != "" {
			item["chat_name"] = name
			continue
		}
		item["chat_name"] = "来源聊天流已移除"
	}
	writeJSON(w, http.StatusOK, payload)
}

func (d Deps) handleMemoryImageWritebackRetry(w http.ResponseWriter, r *http.Request) {
	journal, err := d.openImageWritebackJournal()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer journal.Close()
	count, err := journal.RetryFailed()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": count})
}

// memoryChatDisplayNames 批量解析真实聊天流展示名
func (d Deps) memoryChatDisplayNames(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	names := map[string]string{}
	if len(sessionIDs) == 0 {
		return names, nil
	}
	db := d.chatDeps().ChatDB()
	if db == nil {
		return names, nil
	}
	var records []chatsession.Record
	if err := db.WithContext(ctx).Where("session_id IN ?", sessionIDs).Find(&records).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return names, nil
		}
		return nil, fmt.Errorf("查询聊天流失败: %w", err)
	}
	for index := range records {
		record := &records[index]
		var latest *chat.LatestChatMessage
		if !record.IsGroup() && (record.UserCardname == nil || strings.TrimSpace(*record.UserCardname) == "") &&
			(record.UserNickname == nil || strings.TrimSpace(*record.UserNickname) == "") {
			latest = d.chatDeps().LatestChatMessage(ctx, record.SessionID)
		}
		names[record.SessionID] = chat.ChatDisplayName(record, latest)
	}
	return names, nil
}

func (d Deps) memoryChatSessions(ctx context.Context, sessionIDs []string) (map[string]*chatsession.Record, error) {
	sessions := map[string]*chatsession.Record{}
	if len(sessionIDs) == 0 {
		return sessions, nil
	}
	db := d.chatDeps().ChatDB()
	if db == nil {
		return sessions, nil
	}
	var records []chatsession.Record
	if err := db.WithContext(ctx).Where("session_id IN ?", sessionIDs).Find(&records).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return sessions, nil
		}
		return nil, fmt.Errorf("查询聊天流失败: %w", err)
	}
	for index := range records {
		record := &records[index]
		sessions[record.SessionID] = record
	}
	return sessions, nil
}

func (d Deps) handleMemoryImageBackfill(w http.ResponseWriter, r *http.Request) {
	limit, ok := mqInt(w, r, "limit", 500, 1, 5000)
	if !ok {
		return
	}
	beforeID, ok := optionalPositiveQueryInt(w, r, "before_id")
	if !ok {
		return
	}
	upperID, ok := optionalPositiveQueryInt(w, r, "upper_id")
	if !ok {
		return
	}
	preview, ok := mqBool(w, r, "preview", false)
	if !ok {
		return
	}
	kernel := runtimeKernel()
	if kernel == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "图片记忆未启用")
		return
	}
	runtime, ok := kernel.ImageMemoryRuntime().(*imagememory.ImageMemoryRuntime)
	if !ok || runtime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "图片记忆未启用")
		return
	}
	db := d.chatDeps().ChatDB()
	if db == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "会话数据库尚未就绪")
		return
	}
	query := db.WithContext(r.Context()).Model(&memory.MessageLog{})
	if beforeID != nil {
		query = query.Where("id < ?", *beforeID)
	}
	if upperID != nil {
		query = query.Where("id <= ?", *upperID)
	}
	var records []memory.MessageLog
	if err := query.Order("id DESC").Limit(limit).Find(&records).Error; err != nil {
		writeInternalError(w, fmt.Errorf("查询历史消息失败: %w", err))
		return
	}
	sessionIDs := make([]string, 0, len(records))
	seenSessions := map[string]struct{}{}
	for index := range records {
		sessionID := strings.TrimSpace(records[index].SessionID)
		if sessionID == "" {
			continue
		}
		if _, exists := seenSessions[sessionID]; exists {
			continue
		}
		seenSessions[sessionID] = struct{}{}
		sessionIDs = append(sessionIDs, sessionID)
	}
	sessions, err := d.memoryChatSessions(r.Context(), sessionIDs)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	chatNames, err := d.memoryChatDisplayNames(r.Context(), sessionIDs)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	items := []map[string]any{}
	counts := map[string]int{}
	processedIDs := map[int]struct{}{}
	failedIDs := map[int]struct{}{}
	recordIDs := make([]int, 0, len(records))
	for index := range records {
		record := &records[index]
		recordID := int(record.ID)
		recordIDs = append(recordIDs, recordID)
		sequence, err := messagecomponent.DecodeDBRecord(record.RawContent)
		if err != nil {
			items = append(items, map[string]any{
				"record_id": recordID, "category": "missing_source", "error": err.Error(),
			})
			counts["missing_source"]++
			failedIDs[recordID] = struct{}{}
			continue
		}
		sessionID := strings.TrimSpace(record.SessionID)
		session := sessions[sessionID]
		chatName := "来源聊天流无法解析"
		if session != nil {
			if name := chatNames[sessionID]; name != "" {
				chatName = name
			}
		}
		messageID := memory.MessageLogMessageID(record)
		userStatement := memorywriteback.ExplicitMessageText(sequence.Components)
		for _, component := range memorywriteback.CollectImageWritebackComponents(sequence.Components) {
			item := map[string]any{
				"record_id":      recordID,
				"message_id":     messageID,
				"component_path": component.Path,
				"chat_name":      chatName,
			}
			category, failure := "missing_source", "缺少来源消息ID"
			if messageID != "" {
				if session == nil {
					category, failure = "missing_chat", "无法解析已注册的真实聊天流"
				} else {
					category, failure = memorywriteback.BackfillImageComponent(r.Context(), runtime, record, sessionID, messageID, component, userStatement, preview, nil)
				}
			}
			if failure != "" {
				item["error"] = failure
				failedIDs[recordID] = struct{}{}
			}
			if category == "processed" {
				processedIDs[recordID] = struct{}{}
			}
			item["category"] = category
			counts[category]++
			items = append(items, item)
		}
	}
	payload := map[string]any{
		"success":            len(failedIDs) == 0,
		"preview":            preview,
		"scanned_messages":   len(records),
		"processed_messages": len(processedIDs),
		"failed_messages":    len(failedIDs),
		"items":              items,
		"counts":             counts,
		"next_before_id":     nil,
	}
	if upperID != nil {
		payload["upper_id"] = *upperID
	} else if len(recordIDs) > 0 {
		payload["upper_id"] = maxIntSlice(recordIDs)
	} else {
		payload["upper_id"] = nil
	}
	if len(records) == limit && len(recordIDs) > 0 {
		payload["next_before_id"] = minIntSlice(recordIDs)
	}
	writeJSON(w, http.StatusOK, payload)
}

func optionalPositiveQueryInt(w http.ResponseWriter, r *http.Request, key string) (*int, bool) {
	raw := mqString(r, key)
	if raw == "" {
		return nil, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		writeValidationError(w, key+" 必须是大于等于 1 的整数")
		return nil, false
	}
	return &value, true
}

func minIntSlice(values []int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func maxIntSlice(values []int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}
