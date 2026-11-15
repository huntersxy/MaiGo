package webuiapi

// WebUI 记忆时间线的反馈、删除、画像与维护事件采集；对齐 memory.py 的
// _timeline_feedback_events / _timeline_delete_events / _timeline_profile_events /
// _timeline_maintenance_events。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	chatsession "MaiGo/internal/chat/session"
)

// memoryTimelineSourcesForChat 对齐 _timeline_sources_for_chat
func memoryTimelineSourcesForChat(chatID string) []string {
	token := strings.TrimSpace(chatID)
	if token == "" {
		return nil
	}
	return []string{
		"chat_summary:" + token,
		"memory:" + token,
		"chat_stream:" + token,
		"chat_history:" + token,
		"maibot.chat_history:" + token,
	}
}

func memorySourceMatchesChat(source, chatID string) bool {
	token := strings.TrimSpace(source)
	if token == "" {
		return false
	}
	for _, candidate := range memoryTimelineSourcesForChat(chatID) {
		if token == candidate {
			return true
		}
	}
	return false
}

// memoryMetadataChatTokens 对齐 _metadata_chat_tokens
func memoryMetadataChatTokens(raw any) map[string]struct{} {
	metadata, _ := raw.(map[string]any)
	tokens := map[string]struct{}{}
	if metadata == nil {
		return tokens
	}
	for _, key := range []string{"chat_id", "session_id", "stream_id", "chat_ids", "session_ids", "stream_ids"} {
		switch typed := metadata[key].(type) {
		case string:
			if token := strings.TrimSpace(typed); token != "" {
				tokens[token] = struct{}{}
			}
		case []any:
			for _, item := range typed {
				if token := strings.TrimSpace(anyTrimmed(item)); token != "" {
					tokens[token] = struct{}{}
				}
			}
		case []string:
			for _, item := range typed {
				if token := strings.TrimSpace(item); token != "" {
					tokens[token] = struct{}{}
				}
			}
		}
	}
	return tokens
}

// memoryMetadataMatchesChat 对齐 _metadata_matches_chat
func memoryMetadataMatchesChat(raw any, chatID string) bool {
	token := strings.TrimSpace(chatID)
	if token == "" {
		return false
	}
	metadata, _ := raw.(map[string]any)
	if metadata == nil {
		return false
	}
	if _, ok := memoryMetadataChatTokens(metadata)[token]; ok {
		return true
	}
	for _, key := range []string{"chat", "chat_target", "source_context", "import_context"} {
		nested, _ := metadata[key].(map[string]any)
		if nested == nil {
			continue
		}
		if _, ok := memoryMetadataChatTokens(nested)[token]; ok {
			return true
		}
	}
	return false
}

// memoryParagraphMatchesChat 对齐 _paragraph_matches_chat
func memoryParagraphMatchesChat(source, metadata any, chatID string) (bool, string) {
	if memoryMetadataMatchesChat(decodeMemoryMetadata(metadata), chatID) {
		return true, "metadata.chat_id"
	}
	if memorySourceMatchesChat(anyTrimmed(source), chatID) {
		return true, "source"
	}
	return false, ""
}

func memoryTimelineEventInRange(occurred float64, start, end *float64) bool {
	if start != nil && occurred < *start {
		return false
	}
	if end != nil && occurred > *end {
		return false
	}
	return true
}

// memoryTimelineTypesMatch 对齐 _types_match：event_type 或 category 命中即可
func memoryTimelineTypesMatch(event map[string]any, accepted map[string]struct{}) bool {
	if len(accepted) == 0 {
		return true
	}
	for _, key := range []string{"event_type", "category"} {
		if _, ok := accepted[anyTrimmed(event[key])]; ok {
			return true
		}
	}
	return false
}

func memoryTimelineQueryLimit(limit, multiplier, minimum int) int {
	value := limit * multiplier
	if value < minimum {
		value = minimum
	}
	return value
}

func memoryFirstFloat(values ...any) (float64, bool) {
	for _, value := range values {
		if parsed, ok := anyFloat(value); ok {
			return parsed, true
		}
	}
	return 0, false
}

// memoryTimelineFeedbackEvents 对齐 _timeline_feedback_events
func memoryTimelineFeedbackEvents(chatID string, start, end *float64, accepted map[string]struct{}, limit int) []map[string]any {
	queryLimit := memoryTimelineQueryLimit(limit, 3, 100)
	rows := queryMemoryRowsOptional(`SELECT * FROM memory_feedback_tasks
		WHERE session_id = ?
		ORDER BY COALESCE(updated_at, query_timestamp, created_at, 0) DESC
		LIMIT ?`, chatID, queryLimit)
	events := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		querySnapshot := decodeMemoryMetadata(row["query_snapshot_json"])
		decision := decodeMemoryMetadata(row["decision_json"])
		rollbackPlan := decodeMemoryMetadata(row["rollback_plan_json"])
		rollbackResult := decodeMemoryMetadata(row["rollback_result_json"])
		taskID := anyTrimmed(row["id"])
		toolID := anyTrimmed(row["query_tool_id"])
		status := anyTrimmed(row["status"])
		updatedAt, hasUpdated := memoryFirstFloat(row["updated_at"], row["query_timestamp"], row["created_at"])
		if hasUpdated && memoryTimelineEventInRange(updatedAt, start, end) {
			events = append(events, memoryTimelineEvent("feedback_correction_applied", "feedback", updatedAt, chatID,
				taskID, "反馈纠错处理", "纠错任务状态："+firstNonEmptyText(status, "未知"),
				map[string]any{"tab": "feedback", "params": map[string]any{"task_id": taskID}},
				1, toolID, "feedback.session_id", map[string]any{
					"task_id": taskID, "query_tool_id": toolID, "status": status,
				}))
		}
		rolledBackAt, hasRollback := anyFloat(row["rolled_back_at"])
		if hasRollback && memoryTimelineEventInRange(rolledBackAt, start, end) {
			events = append(events, memoryTimelineEvent("feedback_correction_rollback", "feedback", rolledBackAt, chatID,
				taskID, "反馈纠错回滚", firstNonEmptyText(row["rollback_reason"], "纠错任务已回滚"),
				map[string]any{"tab": "feedback", "params": map[string]any{"task_id": taskID}},
				1, toolID, "feedback.session_id", map[string]any{
					"task_id": taskID, "query_tool_id": toolID,
				}))
		}
		if !hasUpdated || !memoryTimelineEventInRange(updatedAt, start, end) {
			continue
		}
		for _, personID := range memoryFeedbackPersonIDs(row, querySnapshot, decision, rollbackPlan, rollbackResult) {
			events = append(events, memoryTimelineEvent("profile_updated", "profile", updatedAt, chatID,
				personID, "相关画像变更", "画像操作由该聊天流的反馈纠错记录关联触发",
				map[string]any{"tab": "profiles", "params": map[string]any{"person_id": personID}},
				1, toolID, "feedback.session_id", map[string]any{
					"person_id": personID, "task_id": taskID,
				}))
		}
	}
	return memoryFilterTimelineEvents(events, accepted)
}

// memoryFeedbackPersonIDs 对齐 _feedback_person_ids
func memoryFeedbackPersonIDs(row map[string]any, payloads ...map[string]any) []string {
	candidates := make([]string, 0, 4)
	collect := func(value any) {
		if value == nil {
			return
		}
		candidates = append(candidates, anyTrimmed(value))
	}
	for _, payload := range payloads {
		if payload == nil {
			continue
		}
		for _, key := range []string{"person_ids", "profile_person_ids"} {
			for _, item := range listValue(payload[key]) {
				collect(item)
			}
		}
		if profile, ok := payload["profile"].(map[string]any); ok {
			collect(profile["person_id"])
		}
	}
	normalized := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		normalized = append(normalized, candidate)
	}
	return normalized
}

func memoryFilterTimelineEvents(events []map[string]any, accepted map[string]struct{}) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if memoryTimelineTypesMatch(event, accepted) {
			out = append(out, event)
		}
	}
	return out
}

// memoryLooksLikeMemoryHash 判定 32/64 位十六进制记忆哈希
func memoryLooksLikeMemoryHash(value any) bool {
	token := strings.ToLower(strings.TrimSpace(anyTrimmed(value)))
	if len(token) != 32 && len(token) != 64 {
		return false
	}
	for _, char := range token {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// memoryCollectPayloadHashes 对齐 _collect_payload_hashes，跳过体量大的向量标识
func memoryCollectPayloadHashes(value any, hashes map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "vector_ids" || key == "deleted_relation_rows" {
				continue
			}
			if memoryLooksLikeMemoryHash(key) {
				hashes[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
			}
			memoryCollectPayloadHashes(item, hashes)
		}
	case []any:
		for _, item := range typed {
			memoryCollectPayloadHashes(item, hashes)
		}
	case string:
		if memoryLooksLikeMemoryHash(typed) {
			hashes[strings.ToLower(strings.TrimSpace(typed))] = struct{}{}
		}
	}
}

func memoryCollectItemHashes(items []map[string]any, hashes map[string]struct{}) {
	for _, item := range items {
		for _, key := range []string{"item_hash", "item_key"} {
			if memoryLooksLikeMemoryHash(item[key]) {
				hashes[strings.ToLower(strings.TrimSpace(anyTrimmed(item[key])))] = struct{}{}
			}
		}
	}
}

// memoryChunkTokens 按 SQLite 参数上限分片
func memoryChunkTokens(tokens []string, size int) [][]string {
	if size <= 0 {
		size = 300
	}
	chunks := make([][]string, 0, (len(tokens)+size-1)/size)
	for start := 0; start < len(tokens); start += size {
		end := start + size
		if end > len(tokens) {
			end = len(tokens)
		}
		chunks = append(chunks, tokens[start:end])
	}
	return chunks
}

// memoryMatchingParagraphHashes 挑出属于该聊天流的段落哈希
func memoryMatchingParagraphHashes(hashes map[string]struct{}, chatID string) map[string]struct{} {
	matched := map[string]struct{}{}
	if len(hashes) == 0 {
		return matched
	}
	tokens := make([]string, 0, len(hashes))
	for token := range hashes {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	for _, chunk := range memoryChunkTokens(tokens, 300) {
		args := make([]any, 0, len(chunk))
		for _, token := range chunk {
			args = append(args, token)
		}
		rows := queryMemoryRowsOptional(`SELECT hash, source, metadata FROM paragraphs WHERE hash IN (`+memoryPlaceholders(len(chunk))+`)`, args...)
		for _, row := range rows {
			if ok, _ := memoryParagraphMatchesChat(row["source"], row["metadata"], chatID); ok {
				matched[strings.ToLower(anyTrimmed(row["hash"]))] = struct{}{}
			}
		}
	}
	return matched
}

// memoryResolveChatHashes 把候选哈希中属于该聊天流的挑出来，对齐 _resolve_chat_hashes
func memoryResolveChatHashes(candidates map[string]struct{}, chatID string) map[string]struct{} {
	if len(candidates) == 0 {
		return map[string]struct{}{}
	}
	tokens := make([]string, 0, len(candidates))
	for token := range candidates {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	matched := memoryMatchingParagraphHashes(candidates, chatID)

	for _, chunk := range memoryChunkTokens(tokens, 300) {
		placeholders := memoryPlaceholders(len(chunk))
		args := make([]any, 0, len(chunk)*2)
		for _, token := range chunk {
			args = append(args, token)
		}
		for _, token := range chunk {
			args = append(args, token)
		}
		relationRows := queryMemoryRowsOptional(`SELECT hash, source_paragraph FROM relations WHERE hash IN (`+placeholders+`)
			UNION
			SELECT hash, source_paragraph FROM deleted_relations WHERE hash IN (`+placeholders+`)`, args...)
		relationParagraphs := map[string]struct{}{}
		for _, row := range relationRows {
			if token := strings.ToLower(strings.TrimSpace(anyTrimmed(row["source_paragraph"]))); token != "" {
				relationParagraphs[token] = struct{}{}
			}
		}
		ownedParagraphs := memoryMatchingParagraphHashes(relationParagraphs, chatID)
		for _, row := range relationRows {
			if _, ok := ownedParagraphs[strings.ToLower(strings.TrimSpace(anyTrimmed(row["source_paragraph"])))]; ok {
				matched[strings.ToLower(anyTrimmed(row["hash"]))] = struct{}{}
			}
		}

		entityArgs := make([]any, 0, len(chunk))
		for _, token := range chunk {
			entityArgs = append(entityArgs, token)
		}
		entityRows := queryMemoryRowsOptional(`SELECT pe.entity_hash, p.hash, p.metadata, p.source
			FROM paragraph_entities pe
			JOIN paragraphs p ON p.hash = pe.paragraph_hash
			WHERE pe.entity_hash IN (`+memoryPlaceholders(len(chunk))+`)`, entityArgs...)
		for _, row := range entityRows {
			if ok, _ := memoryParagraphMatchesChat(row["source"], row["metadata"], chatID); ok {
				matched[strings.ToLower(anyTrimmed(row["entity_hash"]))] = struct{}{}
			}
		}
	}
	return matched
}

// memoryOperationPayloadMatchesChat 对齐 _operation_payload_matches_chat
func memoryOperationPayloadMatchesChat(value any, chatID string, chatHashes map[string]struct{}) bool {
	switch typed := value.(type) {
	case map[string]any:
		if memoryMetadataMatchesChat(typed, chatID) {
			return true
		}
		if source, ok := typed["source"]; ok {
			if memorySourceMatchesChat(anyTrimmed(source), chatID) {
				return true
			}
		}
		if itemKey, ok := typed["item_key"]; ok {
			if memorySourceMatchesChat(anyTrimmed(itemKey), chatID) {
				return true
			}
		}
		for _, item := range typed {
			if memoryOperationPayloadMatchesChat(item, chatID, chatHashes) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if memoryOperationPayloadMatchesChat(item, chatID, chatHashes) {
				return true
			}
		}
	case string:
		if memorySourceMatchesChat(typed, chatID) {
			return true
		}
		_, ok := chatHashes[strings.ToLower(strings.TrimSpace(typed))]
		return ok
	}
	return false
}

// memoryLoadDeleteOperationItems 批量加载删除明细，可按 item_type 收窄
func memoryLoadDeleteOperationItems(operationIDs []string, itemTypes ...string) map[string][]map[string]any {
	result := map[string][]map[string]any{}
	if len(operationIDs) == 0 {
		return result
	}
	tokens := append([]string(nil), operationIDs...)
	sort.Strings(tokens)
	for _, chunk := range memoryChunkTokens(tokens, 300) {
		args := make([]any, 0, len(chunk)+len(itemTypes))
		for _, token := range chunk {
			args = append(args, token)
		}
		typeClause := ""
		if len(itemTypes) > 0 {
			typeClause = " AND item_type IN (" + memoryPlaceholders(len(itemTypes)) + ")"
			for _, itemType := range itemTypes {
				args = append(args, itemType)
			}
		}
		rows := queryMemoryRowsOptional(`SELECT operation_id, item_type, item_hash, item_key, payload_json, created_at
			FROM delete_operation_items
			WHERE operation_id IN (`+memoryPlaceholders(len(chunk))+`)`+typeClause+`
			ORDER BY operation_id ASC, id ASC`, args...)
		for _, row := range rows {
			operationID := anyTrimmed(row["operation_id"])
			row["payload"] = decodeMemoryJSONPayload(row["payload_json"], map[string]any{})
			result[operationID] = append(result[operationID], row)
		}
	}
	return result
}

func memoryDeleteOperationObjectCount(summary map[string]any, items []map[string]any) int {
	for _, key := range []string{"object_count", "count", "deleted_count"} {
		if value, ok := anyFloat(summary[key]); ok && value > 0 {
			return int(value)
		}
	}
	if counts, ok := summary["counts"].(map[string]any); ok {
		total := 0
		for _, value := range counts {
			if parsed, ok := anyFloat(value); ok {
				total += int(parsed)
			}
		}
		if total > 0 {
			return total
		}
	}
	if len(items) > 0 {
		return len(items)
	}
	return 1
}

// memoryTimelineDeleteEvents 对齐 _timeline_delete_events
func memoryTimelineDeleteEvents(chatID string, start, end *float64, accepted map[string]struct{}, limit int) []map[string]any {
	queryLimit := memoryTimelineQueryLimit(limit, 4, 200)
	rows := queryMemoryRowsOptional(`SELECT operation_id, mode, reason, requested_by, status, created_at, restored_at, summary_json
		FROM delete_operations
		ORDER BY COALESCE(restored_at, created_at, 0) DESC
		LIMIT ?`, queryLimit)
	if len(rows) == 0 {
		return nil
	}

	summaries := map[string]map[string]any{}
	selectors := map[string]any{}
	operationIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		operationID := strings.TrimSpace(anyTrimmed(row["operation_id"]))
		if operationID == "" {
			continue
		}
		operationIDs = append(operationIDs, operationID)
		summaries[operationID] = decodeMemoryMetadata(row["summary_json"])
		selectors[operationID] = nil
	}

	selectorPayload := func(operationID string) any {
		if cached, exists := selectors[operationID]; exists && cached != nil {
			return cached
		}
		selectorRows := queryMemoryRowsOptional(`SELECT selector FROM delete_operations WHERE operation_id = ?`, operationID)
		if len(selectorRows) == 0 {
			selectors[operationID] = nil
			return nil
		}
		raw := selectorRows[0]["selector"]
		decoded := decodeMemoryJSONPayload(raw, raw)
		selectors[operationID] = decoded
		return decoded
	}

	matchedOperations := map[string]struct{}{}
	candidates := map[string]struct{}{}
	for _, summary := range summaries {
		memoryCollectPayloadHashes(summary, candidates)
	}
	chatHashes := memoryResolveChatHashes(candidates, chatID)
	for operationID, summary := range summaries {
		if memoryOperationPayloadMatchesChat(summary, chatID, chatHashes) {
			matchedOperations[operationID] = struct{}{}
		}
	}

	unmatched := make([]string, 0, len(operationIDs))
	for _, operationID := range operationIDs {
		if _, ok := matchedOperations[operationID]; !ok {
			unmatched = append(unmatched, operationID)
		}
	}
	selectorHashes := map[string]struct{}{}
	for _, operationID := range unmatched {
		memoryCollectPayloadHashes(selectorPayload(operationID), selectorHashes)
	}
	newlySeen := map[string]struct{}{}
	for token := range selectorHashes {
		if _, exists := candidates[token]; !exists {
			newlySeen[token] = struct{}{}
		}
	}
	if len(newlySeen) > 0 {
		for token := range memoryResolveChatHashes(newlySeen, chatID) {
			chatHashes[token] = struct{}{}
		}
	}
	for _, operationID := range unmatched {
		if _, ok := matchedOperations[operationID]; ok {
			continue
		}
		if memoryOperationPayloadMatchesChat(selectors[operationID], chatID, chatHashes) {
			matchedOperations[operationID] = struct{}{}
		}
	}

	remaining := make([]string, 0, len(operationIDs))
	for _, operationID := range operationIDs {
		if _, ok := matchedOperations[operationID]; !ok {
			remaining = append(remaining, operationID)
		}
	}
	itemsByOperation := memoryLoadDeleteOperationItems(remaining, "paragraph")
	unscoped := make([]string, 0, len(remaining))
	for _, operationID := range remaining {
		if selector := selectors[operationID]; selector == nil {
			unscoped = append(unscoped, operationID)
		}
	}
	for operationID, items := range memoryLoadDeleteOperationItems(unscoped) {
		itemsByOperation[operationID] = append(itemsByOperation[operationID], items...)
	}
	extraHashes := map[string]struct{}{}
	for _, items := range itemsByOperation {
		memoryCollectItemHashes(items, extraHashes)
	}
	newlySeen = map[string]struct{}{}
	for token := range extraHashes {
		if _, exists := candidates[token]; exists {
			continue
		}
		if _, exists := selectorHashes[token]; exists {
			continue
		}
		newlySeen[token] = struct{}{}
	}
	if len(newlySeen) > 0 {
		for token := range memoryResolveChatHashes(newlySeen, chatID) {
			chatHashes[token] = struct{}{}
		}
	}
	for operationID, items := range itemsByOperation {
		if memoryOperationPayloadMatchesChat(items, chatID, chatHashes) {
			matchedOperations[operationID] = struct{}{}
		}
	}

	events := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		operationID := strings.TrimSpace(anyTrimmed(row["operation_id"]))
		if _, ok := matchedOperations[operationID]; !ok {
			continue
		}
		summary := summaries[operationID]
		itemCount := memoryDeleteOperationObjectCount(summary, itemsByOperation[operationID])
		mode := anyTrimmed(row["mode"])
		reason := anyTrimmed(row["reason"])
		if createdAt, ok := anyFloat(row["created_at"]); ok && memoryTimelineEventInRange(createdAt, start, end) {
			events = append(events, memoryTimelineEvent("delete_executed", "delete", createdAt, chatID,
				operationID, "删除操作执行", firstNonEmptyText(reason, "删除模式："+firstNonEmptyText(mode, "未知")),
				map[string]any{"tab": "delete", "params": map[string]any{"operation_id": operationID}},
				itemCount, mode, "delete_operation.items", map[string]any{
					"operation_id": operationID, "mode": mode,
				}))
		}
		if restoredAt, ok := anyFloat(row["restored_at"]); ok && memoryTimelineEventInRange(restoredAt, start, end) {
			events = append(events, memoryTimelineEvent("delete_restored", "delete", restoredAt, chatID,
				operationID, "删除操作恢复", "已恢复删除操作："+operationID,
				map[string]any{"tab": "delete", "params": map[string]any{"operation_id": operationID}},
				itemCount, mode, "delete_operation.items", map[string]any{
					"operation_id": operationID, "mode": mode,
				}))
		}
	}
	return memoryFilterTimelineEvents(events, accepted)
}

// memoryTimelineProfileContentChanges 对齐 _timeline_profile_content_changes
func memoryTimelineProfileContentChanges(currentText, previousText string) map[string][]string {
	contentLines := func(text string) []string {
		section := ""
		lines := make([]string, 0, 16)
		for _, rawLine := range strings.Split(text, "\n") {
			line := strings.TrimSpace(rawLine)
			if strings.HasPrefix(line, "#") {
				section = strings.TrimSpace(strings.TrimLeft(line, "#"))
				continue
			}
			if line == "" || line == "暂无" || line == "- 暂无" || strings.HasPrefix(line, "人物ID:") || section == "维护备注" {
				continue
			}
			content := strings.TrimPrefix(line, "- ")
			value := content
			if section != "" {
				value = section + "：" + content
			}
			duplicate := false
			for _, existing := range lines {
				if existing == value {
					duplicate = true
					break
				}
			}
			if !duplicate {
				lines = append(lines, value)
			}
		}
		return lines
	}
	current := contentLines(currentText)
	previous := contentLines(previousText)
	previousSet := map[string]struct{}{}
	for _, line := range previous {
		previousSet[line] = struct{}{}
	}
	currentSet := map[string]struct{}{}
	for _, line := range current {
		currentSet[line] = struct{}{}
	}
	added := make([]string, 0)
	for _, line := range current {
		if _, ok := previousSet[line]; !ok {
			added = append(added, line)
		}
	}
	removed := make([]string, 0)
	for _, line := range previous {
		if _, ok := currentSet[line]; !ok {
			removed = append(removed, line)
		}
	}
	return map[string][]string{"added": added, "removed": removed}
}

func memoryDecodeStringSet(raw any) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range listValue(decodeMemoryJSONPayload(raw, []any{})) {
		if token := strings.TrimSpace(anyTrimmed(item)); token != "" {
			out[token] = struct{}{}
		}
	}
	return out
}

// memoryPersonNames 按 person_id 批量解析姓名
func memoryPersonNames(personIDs []string) map[string]string {
	names := map[string]string{}
	tokens := make([]string, 0, len(personIDs))
	seen := map[string]struct{}{}
	for _, item := range personIDs {
		token := strings.TrimSpace(item)
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	if len(tokens) == 0 {
		return names
	}
	sort.Strings(tokens)
	for _, chunk := range memoryChunkTokens(tokens, 300) {
		args := make([]any, 0, len(chunk))
		for _, token := range chunk {
			args = append(args, token)
		}
		rows := queryMemoryRowsOptional(`SELECT person_id, person_name FROM person_info WHERE person_id IN (`+memoryPlaceholders(len(chunk))+`)`, args...)
		for _, row := range rows {
			personID := strings.TrimSpace(anyTrimmed(row["person_id"]))
			name := strings.TrimSpace(anyTrimmed(row["person_name"]))
			if personID != "" && name != "" {
				names[personID] = name
			}
		}
	}
	return names
}

// memoryTimelineProfileEvents 对齐 _timeline_profile_events
func memoryTimelineProfileEvents(chatID string, start, end *float64, accepted map[string]struct{}, limit int) []map[string]any {
	queryLimit := memoryTimelineQueryLimit(limit, 3, 100)
	sources := memoryTimelineSourcesForChat(chatID)
	if len(sources) == 0 {
		return nil
	}
	sourceArgs := make([]any, 0, len(sources))
	for _, source := range sources {
		sourceArgs = append(sourceArgs, source)
	}
	chatFilter := `(source IN (` + memoryPlaceholders(len(sources)) + `) OR metadata LIKE ?)`
	chatParams := append(append([]any{}, sourceArgs...), "%"+chatID+"%")

	candidateParagraphs := queryMemoryRowsOptional(`SELECT hash, metadata, source FROM paragraphs WHERE `+chatFilter, chatParams...)
	paragraphsByHash := map[string]map[string]any{}
	for _, row := range candidateParagraphs {
		paragraphsByHash[strings.TrimSpace(anyTrimmed(row["hash"]))] = row
	}

	timeClause := ""
	timeParams := []any{}
	if start != nil {
		timeClause += " AND pps.updated_at >= ?"
		timeParams = append(timeParams, *start)
	}
	if end != nil {
		timeClause += " AND pps.updated_at <= ?"
		timeParams = append(timeParams, *end)
	}
	args := append(append([]any{}, chatParams...), timeParams...)
	args = append(args, queryLimit)
	rows := queryMemoryRowsOptional(`SELECT DISTINCT pps.person_id, pps.profile_version, pps.updated_at, pps.source_note, pps.evidence_ids_json
		FROM person_profile_snapshots pps, json_each(pps.evidence_ids_json) je
		JOIN (SELECT DISTINCT hash FROM paragraphs WHERE `+chatFilter+`) candidate ON candidate.hash = je.value
		WHERE 1=1`+timeClause+`
		ORDER BY pps.updated_at DESC
		LIMIT ?`, args...)

	personIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		personIDs = append(personIDs, anyTrimmed(row["person_id"]))
	}
	personNames := memoryPersonNames(personIDs)

	type snapshotKey struct {
		personID string
		version  int
	}
	details := map[snapshotKey]map[string]any{}
	if len(rows) > 0 {
		wanted := make([]string, 0, len(rows))
		detailArgs := make([]any, 0, len(rows)*2)
		for _, row := range rows {
			wanted = append(wanted, "(?, ?)")
			detailArgs = append(detailArgs, anyTrimmed(row["person_id"]), anyInt(row["profile_version"]))
		}
		detailRows := queryMemoryRowsOptional(`WITH wanted(person_id, profile_version) AS (VALUES `+strings.Join(wanted, ",")+`)
			SELECT current.person_id, current.profile_version, current.profile_text,
			       previous.profile_version AS previous_version,
			       previous.profile_text AS previous_text,
			       previous.evidence_ids_json AS previous_evidence_ids_json
			FROM wanted
			JOIN person_profile_snapshots current
			  ON current.person_id = wanted.person_id AND current.profile_version = wanted.profile_version
			LEFT JOIN person_profile_snapshots previous ON previous.snapshot_id = (
				SELECT snapshot_id FROM person_profile_snapshots
				WHERE person_id = current.person_id AND profile_version < current.profile_version
				ORDER BY profile_version DESC LIMIT 1
			)`, detailArgs...)
		for _, row := range detailRows {
			details[snapshotKey{anyTrimmed(row["person_id"]), anyInt(row["profile_version"])}] = row
		}
	}

	events := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		personID := strings.TrimSpace(anyTrimmed(row["person_id"]))
		evidenceIDs := memoryDecodeStringSet(row["evidence_ids_json"])
		matchedParagraphs := make([]map[string]any, 0)
		for evidenceID := range evidenceIDs {
			paragraph, ok := paragraphsByHash[evidenceID]
			if !ok {
				continue
			}
			if ok, _ := memoryParagraphMatchesChat(paragraph["source"], paragraph["metadata"], chatID); ok {
				matchedParagraphs = append(matchedParagraphs, paragraph)
			}
		}
		if len(matchedParagraphs) == 0 {
			continue
		}
		updatedAt, ok := anyFloat(row["updated_at"])
		if !ok || !memoryTimelineEventInRange(updatedAt, start, end) {
			continue
		}
		snapshot := details[snapshotKey{personID, anyInt(row["profile_version"])}]
		previousVersion := 0
		previousText := ""
		previousEvidence := map[string]struct{}{}
		if snapshot != nil {
			previousVersion = anyInt(snapshot["previous_version"])
			previousText = anyTrimmed(snapshot["previous_text"])
			previousEvidence = memoryDecodeStringSet(snapshot["previous_evidence_ids_json"])
		}
		changes := memoryTimelineProfileContentChanges(anyTrimmed(snapshot["profile_text"]), previousText)
		evidenceAdded := 0
		for token := range evidenceIDs {
			if _, exists := previousEvidence[token]; !exists {
				evidenceAdded++
			}
		}
		evidenceRemoved := 0
		for token := range previousEvidence {
			if _, exists := evidenceIDs[token]; !exists {
				evidenceRemoved++
			}
		}
		title := ""
		summary := ""
		switch {
		case previousVersion == 0 && anyInt(row["profile_version"]) != 1:
			title = "画像快照"
			summary = "缺少前一版本，无法还原具体变更；详情可查看该版本正文。"
			changes = map[string][]string{"added": {}, "removed": {}}
		case previousVersion == 0:
			title = "画像生成"
			summary = strings.Join(firstN(changes["added"], 2), "；")
			if summary == "" {
				summary = "首次生成画像，暂无可展示的正文内容。"
			}
		case len(changes["added"]) > 0 || len(changes["removed"]) > 0:
			title = "画像内容更新"
			parts := make([]string, 0, 4)
			for _, line := range firstN(changes["added"], 2) {
				parts = append(parts, "新增："+line)
			}
			for _, line := range firstN(changes["removed"], 2) {
				parts = append(parts, "移除："+line)
			}
			summary = strings.Join(parts, "；")
		case evidenceAdded > 0 || evidenceRemoved > 0:
			title = "画像证据更新"
			summary = fmt.Sprintf("正文未变化；证据新增 %d 条、移除 %d 条。", evidenceAdded, evidenceRemoved)
		default:
			title = "画像快照刷新"
			summary = "与前一版本相比，正文和段落证据未变化。"
		}
		if name := personNames[personID]; name != "" {
			title = name + " · " + title
		}
		profileText := ""
		if snapshot != nil {
			profileText = anyTrimmed(snapshot["profile_text"])
		}
		events = append(events, memoryTimelineEvent("profile_updated", "profile", updatedAt, chatID,
			personID, title, summary,
			map[string]any{"tab": "profiles", "params": map[string]any{"person_id": personID}},
			len(matchedParagraphs), anyTrimmed(row["source_note"]), "profile.evidence_paragraph",
			map[string]any{
				"person_id": personID, "profile_version": anyInt(row["profile_version"]),
				"previous_version": previousVersion, "profile_changes": changes,
				"profile_text": profileText, "evidence_added": evidenceAdded, "evidence_removed": evidenceRemoved,
				"time_note": "记录时间为该版本最后刷新时间，可能包含缓存续期；正文差异按画像版本比较。",
			}))
	}

	overrideLimit := memoryTimelineQueryLimit(limit, 1, 100)
	overrideRows := queryMemoryRowsOptional(`SELECT person_id, updated_at, updated_by, source
		FROM person_profile_overrides
		ORDER BY updated_at DESC
		LIMIT ?`, overrideLimit)
	for _, row := range overrideRows {
		source := strings.TrimSpace(anyTrimmed(row["source"]))
		personID := strings.TrimSpace(anyTrimmed(row["person_id"]))
		updatedAt, ok := anyFloat(row["updated_at"])
		if !ok || !memoryTimelineEventInRange(updatedAt, start, end) {
			continue
		}
		if !memorySourceMatchesChat(source, chatID) && !strings.Contains(source, chatID) {
			continue
		}
		events = append(events, memoryTimelineEvent("profile_override_set", "profile", updatedAt, chatID,
			personID, "画像覆写设置", "人物画像手动覆写与该聊天流来源相关",
			map[string]any{"tab": "profiles", "params": map[string]any{"person_id": personID}},
			1, source, "profile.override.source", map[string]any{"person_id": personID}))
	}
	return memoryFilterTimelineEvents(events, accepted)
}

func firstN(values []string, count int) []string {
	if count <= 0 || len(values) == 0 {
		return nil
	}
	if len(values) < count {
		count = len(values)
	}
	return values[:count]
}

// memoryTimelineMaintenanceEvents 对齐 _timeline_maintenance_events
func memoryTimelineMaintenanceEvents(chatID string, start, end *float64, accepted map[string]struct{}, limit int) []map[string]any {
	queryLimit := memoryTimelineQueryLimit(limit, 4, 200)
	sources := memoryTimelineSourcesForChat(chatID)
	if len(sources) == 0 {
		return nil
	}
	sourceArgs := make([]any, 0, len(sources)+2)
	for _, source := range sources {
		sourceArgs = append(sourceArgs, source)
	}
	sourceArgs = append(sourceArgs, "%"+chatID+"%", queryLimit)
	rows := queryMemoryRowsOptional(`SELECT r.hash, r.subject, r.predicate, r.object, r.source_paragraph,
			r.last_reinforced, r.created_at, r.last_access_reinforced_at, r.reinforcement_count,
			r.inactive_since, r.protected_until, r.metadata, p.source, p.metadata AS paragraph_metadata
		FROM relations r
		JOIN paragraphs p ON p.hash = r.source_paragraph
		WHERE p.source IN (`+memoryPlaceholders(len(sources))+`) OR p.metadata LIKE ?
		ORDER BY MAX(COALESCE(r.last_reinforced, 0), COALESCE(r.last_access_reinforced_at, 0),
		             COALESCE(r.inactive_since, 0), COALESCE(r.protected_until, 0), r.created_at) DESC
		LIMIT ?`, sourceArgs...)
	if len(rows) == 0 {
		return nil
	}

	type manualReinforcement struct {
		occurredAt  float64
		operationID string
		updatedBy   string
		reason      string
	}
	manual := map[string]manualReinforcement{}
	operationRows := queryMemoryRowsOptional(`SELECT operation_id, reason, updated_by, resolved_hashes_json, result_json
		FROM memory_v5_operations
		WHERE action IN ('reinforce', 'remember_forever')
		ORDER BY created_at DESC
		LIMIT ?`, queryLimit)
	for _, operation := range operationRows {
		result := decodeMemoryMetadata(operation["result_json"])
		if success, _ := result["success"].(bool); !success {
			continue
		}
		statuses, _ := result["statuses"].(map[string]any)
		if statuses == nil {
			continue
		}
		for _, relationHash := range listValue(decodeMemoryJSONPayload(operation["resolved_hashes_json"], []any{})) {
			token := strings.TrimSpace(anyTrimmed(relationHash))
			if token == "" {
				continue
			}
			if _, exists := manual[token]; exists {
				continue
			}
			status, _ := statuses[token].(map[string]any)
			if status == nil {
				continue
			}
			reinforcedAt, ok := anyFloat(status["last_reinforced"])
			if !ok {
				continue
			}
			manual[token] = manualReinforcement{
				occurredAt:  reinforcedAt,
				operationID: anyTrimmed(operation["operation_id"]),
				updatedBy:   anyTrimmed(operation["updated_by"]),
				reason:      firstNonEmptyText(operation["reason"], "通过强化接口执行的显式强化"),
			}
		}
	}

	events := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		relationHash := strings.TrimSpace(anyTrimmed(row["hash"]))
		matched, attribution := memoryParagraphMatchesChat(row["source"], row["paragraph_metadata"], chatID)
		if !matched {
			continue
		}
		relationText := strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(anyTrimmed(row["subject"])),
			strings.TrimSpace(anyTrimmed(row["predicate"])),
			strings.TrimSpace(anyTrimmed(row["object"])),
		}, " "))
		source := strings.TrimSpace(anyTrimmed(row["source"]))
		jumpTarget := map[string]any{"tab": "maintenance", "params": map[string]any{"target": relationHash}}

		for _, spec := range []struct {
			eventType string
			timeKey   string
			title     string
		}{
			{"relation_reinforced", "last_reinforced", "关系强化"},
			{"relation_frozen", "inactive_since", "关系冻结"},
			{"relation_protected", "protected_until", "关系保护"},
		} {
			occurredAt, ok := anyFloat(row[spec.timeKey])
			if !ok || !memoryTimelineEventInRange(occurredAt, start, end) {
				continue
			}
			title := spec.title
			metadata := map[string]any{
				"relation_hash": relationHash, "source_paragraph": anyTrimmed(row["source_paragraph"]),
			}
			if spec.eventType == "relation_reinforced" {
				if entry, exists := manual[relationHash]; exists && entry.occurredAt == occurredAt {
					title = "手动强化"
					metadata["operation_id"] = entry.operationID
					metadata["updated_by"] = entry.updatedBy
					metadata["monitor_reason"] = entry.reason
					metadata["reinforcement_kind"] = "manual"
				} else if anyInt(row["reinforcement_count"]) == 1 {
					createdAt, hasCreated := anyFloat(row["created_at"])
					if !hasCreated || !memoryTimelineEventInRange(createdAt, start, end) {
						continue
					}
					occurredAt = createdAt
					title = "新增关系"
					reason := "新证据段落入库，首次建立关系关联"
					paragraphMetadata := decodeMemoryMetadata(row["paragraph_metadata"])
					if anyTrimmed(paragraphMetadata["trigger"]) == "message_threshold" {
						reason = "聊天消息数量达到阈值，自动聊天总结入库并建立关系关联"
					}
					metadata["reinforcement_kind"] = "added"
					metadata["reason"] = reason
				} else {
					metadata["reinforcement_kind"] = "unknown"
					metadata["reason"] = "这条历史记录未保存触发途径，无法区分新增证据与显式强化"
				}
			}
			events = append(events, memoryTimelineEvent(spec.eventType, "maintenance", occurredAt, chatID,
				relationHash, title, firstNonEmptyText(relationText, "维护操作影响了该聊天流证据关系"),
				jumpTarget, 1, source, attribution, metadata))
		}

		if accessedAt, ok := anyFloat(row["last_access_reinforced_at"]); ok && memoryTimelineEventInRange(accessedAt, start, end) {
			events = append(events, memoryTimelineEvent("relation_accessed", "maintenance", accessedAt, chatID,
				relationHash, "使用强化", relationText, jumpTarget, 1, source, attribution,
				map[string]any{
					"relation_hash": relationHash, "source_paragraph": anyTrimmed(row["source_paragraph"]),
					"reinforcement_kind": "access",
					"reason":             "记忆检索最终采用了这条关系，满足访问加强冷却条件后提升保留强度",
				}))
		}
	}
	return memoryFilterTimelineEvents(events, accepted)
}

// memoryTimelineChatInfo 解析聊天流展示信息，chat 不存在时返回 false
func (d Deps) memoryTimelineChatInfo(ctx context.Context, chatID string) (string, bool) {
	db := d.chatDeps().ChatDB()
	if db == nil {
		return "", false
	}
	if _, err := chatsession.NewManager(db).GetExistingBySessionID(ctx, chatID); err != nil {
		return "", false
	}
	names, err := d.memoryChatDisplayNames(ctx, []string{chatID})
	if err != nil {
		return chatID, true
	}
	if name := strings.TrimSpace(names[chatID]); name != "" {
		return name, true
	}
	return chatID, true
}
