package webuiapi

// WebUI 记忆记录检索/详情/时间线；对齐 memory.py _memory_records_search、
// _memory_record_context、_memory_timeline。查询错误必须真实暴露

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

var memoryRecordTypes = map[string]struct{}{
	"paragraph": {}, "entity": {}, "relation": {}, "fact": {}, "episode": {},
}

var memoryRecordTypeOrder = []string{"paragraph", "entity", "relation", "fact", "episode"}

func (d Deps) handleMemoryAdminRecordsSearch(w http.ResponseWriter, r *http.Request) {
	types, ok := memorySelectedTypes(w, mqString(r, "types"))
	if !ok {
		return
	}
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	includeInactive, ok := mqBool(w, r, "include_inactive", false)
	if !ok {
		return
	}
	keyword := mqString(r, "query")
	pattern := memoryLikePattern(keyword)

	rowsByType := map[string][]map[string]any{}
	// 任一类型查询失败立即终止：Python 抛 HTTPException 后不会再查后续类型，
	// 继续跑会让同一响应体里叠加多个错误 JSON，前端解析失败只剩「请求失败」
	var queryErr error
	queryFailed := false
	runQuery := func(sql string, params ...any) []map[string]any {
		if queryFailed {
			return nil
		}
		rows, err := queryMemoryRows(sql, params...)
		if err != nil {
			queryErr = err
			queryFailed = true
			return nil
		}
		return rows
	}
	if memoryTypeSelected(types, "paragraph") {
		rowsByType["paragraph"] = runQuery(
			`SELECT hash, content, source, knowledge_type, word_count, vector_index,
			        created_at, updated_at, metadata, is_deleted
			 FROM paragraphs
			 WHERE (? = 1 OR COALESCE(is_deleted, 0) = 0)
			   AND (? = '' OR LOWER(COALESCE(content, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(hash, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(source, '')) LIKE ? ESCAPE '\')
			 ORDER BY COALESCE(updated_at, created_at, 0) DESC
			 LIMIT ?`,
			boolInt(includeInactive), keyword, pattern, pattern, pattern, limit,
		)
	}
	if memoryTypeSelected(types, "entity") {
		rowsByType["entity"] = runQuery(
			`SELECT e.hash, e.name, e.appearance_count, e.vector_index, e.created_at, e.metadata, e.is_deleted,
			        (SELECT COUNT(DISTINCT pe.paragraph_hash)
			         FROM paragraph_entities pe
			         JOIN paragraphs p ON p.hash = pe.paragraph_hash
			         WHERE pe.entity_hash = e.hash AND COALESCE(p.is_deleted, 0) = 0) AS active_evidence_count
			 FROM entities e
			 WHERE (? = 1 OR COALESCE(e.is_deleted, 0) = 0)
			   AND (? = '' OR LOWER(COALESCE(e.name, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(e.hash, '')) LIKE ? ESCAPE '\')
			 ORDER BY e.appearance_count DESC, e.created_at DESC
			 LIMIT ?`,
			boolInt(includeInactive), keyword, pattern, pattern, limit,
		)
	}
	if memoryTypeSelected(types, "relation") {
		rowsByType["relation"] = runQuery(
			`SELECT r.hash, r.subject, r.predicate, r.object, r.confidence, r.source_paragraph,
			        r.vector_state, r.created_at, r.last_reinforced, r.is_inactive,
			        r.is_pinned, r.protected_until, r.metadata, p.source AS origin_source
			 FROM relations r
			 LEFT JOIN paragraphs p ON p.hash = r.source_paragraph
			 WHERE (? = 1 OR COALESCE(r.is_inactive, 0) = 0)
			   AND (? = '' OR LOWER(COALESCE(r.subject, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(r.predicate, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(r.object, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(r.hash, '')) LIKE ? ESCAPE '\')
			 ORDER BY COALESCE(r.last_reinforced, r.created_at, 0) DESC
			 LIMIT ?`,
			boolInt(includeInactive), keyword, pattern, pattern, pattern, pattern, limit,
		)
	}
	if memoryTypeSelected(types, "fact") {
		rowsByType["fact"] = runQuery(
			`SELECT * FROM fact_claims
			 WHERE (? = 1 OR LOWER(COALESCE(status, 'active')) = 'active')
			   AND (? = '' OR LOWER(COALESCE(fact_key, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(value_text, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(scope_id, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(claim_id, '')) LIKE ? ESCAPE '\')
			 ORDER BY COALESCE(updated_at, last_confirmed_at, created_at, 0) DESC
			 LIMIT ?`,
			boolInt(includeInactive), keyword, pattern, pattern, pattern, pattern, limit,
		)
	}
	if memoryTypeSelected(types, "episode") {
		rowsByType["episode"] = runQuery(
			`SELECT episode_id, source, title, summary, paragraph_count,
			        event_time_start, event_time_end, participants_json, keywords_json,
			        created_at, updated_at
			 FROM episodes
			 WHERE (? = '' OR LOWER(COALESCE(title, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(summary, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(keywords_json, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(participants_json, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(episode_id, '')) LIKE ? ESCAPE '\'
			        OR LOWER(COALESCE(source, '')) LIKE ? ESCAPE '\')
			 ORDER BY COALESCE(updated_at, event_time_end, created_at, 0) DESC
			 LIMIT ?`,
			keyword, pattern, pattern, pattern, pattern, pattern, pattern, limit,
		)
	}
	if queryFailed {
		// 对齐 Python _require_memory_metadata_store：未就绪只回一个 503 + detail，
		// 查询本身出错才归 500 并带「查询长期记忆 metadata 失败」
		if errors.Is(queryErr, errMemoryMetadataNotReady) {
			writeAPIError(w, http.StatusServiceUnavailable, queryErr.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "查询长期记忆 metadata 失败: "+queryErr.Error())
		return
	}

	items := make([]map[string]any, 0, limit)
	counts := map[string]int{}
	// 对齐 Python result_counts：每个选中的 type 都出键，没有命中时保留 0
	for _, recordType := range types {
		counts[recordType] = 0
	}
	for _, recordType := range types {
		rows := rowsByType[recordType]
		d.decorateMemoryRecordOrigins(r.Context(), rows)
		for _, row := range rows {
			items = append(items, memoryRecordPayload(recordType, row))
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return memoryRecordTimestamp(items[i]) > memoryRecordTimestamp(items[j])
	})
	if len(items) > limit {
		items = items[:limit]
	}
	for _, item := range items {
		counts[fmt.Sprint(item["type"])]++
	}
	result := map[string]any{
		"success":          true,
		"query":            keyword,
		"types":            types,
		"include_inactive": includeInactive,
		"limit":            limit,
		"count":            len(items),
		"counts":           counts,
		"items":            items,
	}
	writeJSON(w, http.StatusOK, result)
}

func memoryTypeSelected(types []string, target string) bool {
	for _, item := range types {
		if item == target {
			return true
		}
	}
	return false
}

func memorySelectedTypes(w http.ResponseWriter, raw string) ([]string, bool) {
	requested := mqCSV(strings.ToLower(raw))
	if len(requested) == 0 {
		return append([]string{}, memoryRecordTypeOrder...), true
	}
	unknown := make([]string, 0)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(requested))
	for _, item := range requested {
		if _, ok := memoryRecordTypes[item]; !ok {
			unknown = append(unknown, item)
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(unknown) > 0 {
		writeAPIError(w, http.StatusBadRequest, "不支持的记忆类型: "+strings.Join(unknown, ", "))
		return nil, false
	}
	return out, true
}

func memoryLikePattern(query string) string {
	escaped := strings.ToLower(strings.TrimSpace(query))
	escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, "%", `\%`)
	escaped = strings.ReplaceAll(escaped, "_", `\_`)
	return "%" + escaped + "%"
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func memoryRecordTimestamp(item map[string]any) float64 {
	for _, key := range []string{"updated_at", "last_confirmed_at", "created_at"} {
		if value, ok := anyFloat(item[key]); ok {
			return value
		}
	}
	return 0
}

func trimMemoryText(value any, limit int) string {
	text := strings.TrimSpace(anyTrimmed(value))
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func decodeMemoryJSONPayload(raw any, fallback any) any {
	if raw == nil {
		return fallback
	}
	text := strings.TrimSpace(anyTrimmed(raw))
	if text == "" {
		return fallback
	}
	var decoded any
	if err := sonic.Unmarshal([]byte(text), &decoded); err != nil {
		return fallback
	}
	return decoded
}

func decodeMemoryMetadata(raw any) map[string]any {
	decoded := decodeMemoryJSONPayload(raw, map[string]any{})
	if mapped, ok := decoded.(map[string]any); ok {
		return mapped
	}
	return map[string]any{}
}

func formatMemoryRelation(subject, predicate, object string) string {
	parts := make([]string, 0, 3)
	for _, item := range []string{subject, predicate, object} {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

func readableFactKey(key string) string {
	cleaned := strings.TrimSpace(key)
	if strings.HasPrefix(cleaned, "statement:") || strings.Contains(cleaned, ":paragraph:") {
		return ""
	}
	return cleaned
}

func memoryFloatOrNil(value any) any {
	if parsed, ok := anyFloat(value); ok {
		return parsed
	}
	return nil
}

func memoryIntOrNil(value any) any {
	if parsed, ok := anyFloat(value); ok {
		return int(parsed)
	}
	return nil
}

// memoryRecordPayload 对齐 _memory_record_payload；来源名称解析依赖 PG，未解析时留空
func memoryRecordPayload(recordType string, row map[string]any) map[string]any {
	switch recordType {
	case "paragraph":
		content := strings.TrimSpace(anyTrimmed(row["content"]))
		source := strings.TrimSpace(anyTrimmed(row["source"]))
		title := trimMemoryText(content, 88)
		if title == "" {
			title = source
		}
		if title == "" {
			title = "未命名段落"
		}
		status := "active"
		if anyBoolInt(row["is_deleted"]) {
			status = "deleted"
		}
		return map[string]any{
			"type": recordType, "id": anyTrimmed(row["hash"]), "title": title,
			"summary": trimMemoryText(content, 240), "source": source,
			// 段落来源可能是导入批次等本身可读的标记，允许直接展示
			"source_label": memoryRecordOriginLabel(row, true),
			"status": status, "created_at": memoryFloatOrNil(row["created_at"]),
			"updated_at": memoryFloatOrNil(row["updated_at"]),
			"metadata": map[string]any{
				"knowledge_type": firstNonEmptyText(row["knowledge_type"], "mixed"),
				"word_count":     anyInt(row["word_count"]),
				"vector_indexed": row["vector_index"] != nil,
				"raw":            decodeMemoryMetadata(row["metadata"]),
			},
		}
	case "entity":
		name := strings.TrimSpace(anyTrimmed(row["name"]))
		if name == "" {
			name = anyTrimmed(row["hash"])
		}
		status := "active"
		if anyBoolInt(row["is_deleted"]) {
			status = "deleted"
		}
		active := anyInt(row["active_evidence_count"])
		return map[string]any{
			"type": recordType, "id": anyTrimmed(row["hash"]), "title": name,
			"summary": fmt.Sprintf("由 %d 条有效段落支撑", active),
			"source":  "", "source_label": "", "status": status,
			"created_at": memoryFloatOrNil(row["created_at"]), "updated_at": nil,
			"metadata": map[string]any{
				"name": name, "appearance_count": anyInt(row["appearance_count"]),
				"active_evidence_count": active, "vector_indexed": row["vector_index"] != nil,
				"raw": decodeMemoryMetadata(row["metadata"]),
			},
		}
	case "relation":
		subject := anyTrimmed(row["subject"])
		predicate := anyTrimmed(row["predicate"])
		object := anyTrimmed(row["object"])
		status := "active"
		if anyBoolInt(row["is_inactive"]) {
			status = "inactive"
		}
		confidence, _ := anyFloat(row["confidence"])
		return map[string]any{
			"type": recordType, "id": anyTrimmed(row["hash"]),
			"title":   formatMemoryRelation(subject, predicate, object),
			"summary": fmt.Sprintf("置信度 %.2f", confidence),
			"source": strings.TrimSpace(anyTrimmed(row["source_paragraph"])),
			// 关系来源是证据段落哈希，只在解析出聊天流/人物名称时展示
			"source_label": memoryRecordOriginLabel(row, false),
			"status": status, "created_at": memoryFloatOrNil(row["created_at"]),
			"updated_at": memoryFloatOrNil(row["last_reinforced"]),
			"metadata": map[string]any{
				"subject": subject, "predicate": predicate, "object": object,
				"confidence": confidence, "vector_state": firstNonEmptyText(row["vector_state"], "none"),
				"is_pinned":       anyBoolInt(row["is_pinned"]),
				"protected_until": memoryFloatOrNil(row["protected_until"]),
				"raw":             decodeMemoryMetadata(row["metadata"]),
			},
		}
	case "episode":
		summary := strings.TrimSpace(anyTrimmed(row["summary"]))
		title := strings.TrimSpace(anyTrimmed(row["title"]))
		if title == "" {
			title = "未命名 Episode"
		}
		source := strings.TrimSpace(anyTrimmed(row["source"]))
		// 回退标题把内部来源标记写进了前缀，换成聊天流名称；换过就不再重复展示
		chatName := strings.TrimSpace(anyTrimmed(row["chat_name"]))
		originInTitle := false
		if source != "" && chatName != "" && strings.HasPrefix(title, source) {
			title = chatName + strings.TrimPrefix(title, source)
			originInTitle = true
		}
		sourceLabel := ""
		if !originInTitle {
			sourceLabel = memoryRecordOriginLabel(row, false)
		}
		return map[string]any{
			"type": recordType, "id": anyTrimmed(row["episode_id"]), "title": title,
			"summary": trimMemoryText(summary, 240), "source": source, "source_label": sourceLabel,
			"status": "active", "created_at": memoryFloatOrNil(row["created_at"]),
			"updated_at": memoryFloatOrNil(row["updated_at"]),
			"metadata": map[string]any{
				"event_time_start": memoryFloatOrNil(row["event_time_start"]),
				"event_time_end":   memoryFloatOrNil(row["event_time_end"]),
				"paragraph_count":  anyInt(row["paragraph_count"]),
				"keywords":         decodeMemoryJSONPayload(row["keywords_json"], []any{}),
				"participants":     decodeMemoryJSONPayload(row["participants_json"], []any{}),
				"raw":              map[string]any{},
			},
		}
	}
	factKey := strings.TrimSpace(anyTrimmed(row["fact_key"]))
	valueText := strings.TrimSpace(anyTrimmed(row["value_text"]))
	scopeType := strings.TrimSpace(anyTrimmed(row["scope_type"]))
	readableKey := readableFactKey(factKey)
	title := valueText
	if readableKey != "" && valueText != "" {
		title = readableKey + ": " + valueText
	}
	if title == "" {
		title = readableKey
	}
	if title == "" {
		title = "未命名事实"
	}
	scopeLabel := strings.TrimSpace(anyTrimmed(row["scope_label"]))
	scopeTypeLabel := scopeType
	if scopeType == "person" {
		scopeTypeLabel = "人物"
	} else if scopeType == "chat" {
		scopeTypeLabel = "聊天流"
	}
	summary := scopeTypeLabel
	if scopeLabel != "" {
		summary = scopeTypeLabel + " · " + scopeLabel
	}
	confidence, _ := anyFloat(row["confidence"])
	return map[string]any{
		"type": "fact", "id": anyTrimmed(row["claim_id"]), "title": title,
		"summary": summary, "source": "", "source_label": "",
		"status":     firstNonEmptyText(row["status"], "active"),
		"created_at": memoryFloatOrNil(row["created_at"]), "updated_at": memoryFloatOrNil(row["updated_at"]),
		"metadata": map[string]any{
			"scope_type": scopeType, "scope_id": anyTrimmed(row["scope_id"]),
			"fact_key": factKey, "value_text": valueText,
			"polarity": anyTrimmed(row["polarity"]), "cardinality": anyTrimmed(row["cardinality"]),
			"stability": anyTrimmed(row["stability"]), "profile_section": anyTrimmed(row["profile_section"]),
			"authority": anyTrimmed(row["authority"]), "confidence": confidence,
			"conflict_group": anyTrimmed(row["conflict_group"]),
			"valid_from":     memoryFloatOrNil(row["valid_from"]), "valid_to": memoryFloatOrNil(row["valid_to"]),
		},
	}
}

func firstNonEmptyText(value any, fallback string) string {
	if text := strings.TrimSpace(anyTrimmed(value)); text != "" {
		return text
	}
	return fallback
}

func (d Deps) handleMemoryRecordContext(w http.ResponseWriter, r *http.Request) {
	recordType := strings.ToLower(strings.TrimSpace(chiURLParam(r, "record_type")))
	recordID := strings.TrimSpace(chiURLParam(r, "record_id"))
	if _, ok := memoryRecordTypes[recordType]; !ok {
		writeAPIError(w, http.StatusBadRequest, "不支持的记忆类型: "+recordType)
		return
	}
	if recordID == "" {
		writeAPIError(w, http.StatusBadRequest, "record_id 不能为空")
		return
	}
	limit, ok := mqInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	result, status, err := memoryRecordContext(recordType, recordID, limit)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func memoryRecordContext(recordType, recordID string, limit int) (map[string]any, int, error) {
	row, err := memoryRecordDetailRow(recordType, recordID)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if row == nil {
		return nil, http.StatusNotFound, fmt.Errorf("未找到 %s 记忆记录: %s", recordType, recordID)
	}
	record := memoryRecordPayload(recordType, row)
	hashes, err := memoryRelatedParagraphHashes(recordType, row, limit)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	paragraphs := []map[string]any{}
	entities := []map[string]any{}
	relations := []map[string]any{}
	facts := []map[string]any{}
	episodes := []map[string]any{}
	if len(hashes) > 0 {
		placeholders := memoryPlaceholders(len(hashes))
		params := memoryAnyParams(hashes)
		if rows, err := queryMemoryRows(`SELECT * FROM paragraphs WHERE hash IN (`+placeholders+`) ORDER BY COALESCE(updated_at, created_at, 0) DESC LIMIT ?`, append(params, limit)...); err == nil {
			for _, item := range rows {
				paragraphs = append(paragraphs, memoryRecordPayload("paragraph", item))
			}
		} else {
			return nil, http.StatusInternalServerError, err
		}
		if rows, err := queryMemoryRows(`SELECT DISTINCT e.*, (SELECT COUNT(DISTINCT pe2.paragraph_hash) FROM paragraph_entities pe2 JOIN paragraphs p2 ON p2.hash = pe2.paragraph_hash WHERE pe2.entity_hash = e.hash AND COALESCE(p2.is_deleted, 0) = 0) AS active_evidence_count FROM paragraph_entities pe JOIN entities e ON e.hash = pe.entity_hash WHERE pe.paragraph_hash IN (`+placeholders+`) ORDER BY e.appearance_count DESC LIMIT ?`, append(params, limit)...); err == nil {
			for _, item := range rows {
				entities = append(entities, memoryRecordPayload("entity", item))
			}
		}
		if rows, err := queryMemoryRows(`SELECT DISTINCT r.*, p.source AS origin_source FROM paragraph_relations pr JOIN relations r ON r.hash = pr.relation_hash LEFT JOIN paragraphs p ON p.hash = r.source_paragraph WHERE pr.paragraph_hash IN (`+placeholders+`) ORDER BY COALESCE(r.last_reinforced, r.created_at, 0) DESC LIMIT ?`, append(params, limit)...); err == nil {
			for _, item := range rows {
				relations = append(relations, memoryRecordPayload("relation", item))
			}
		}
		if rows, err := queryMemoryRows(`SELECT DISTINCT fc.* FROM fact_evidence fe JOIN fact_claims fc ON fc.claim_id = fe.claim_id WHERE fe.evidence_id IN (`+placeholders+`) ORDER BY COALESCE(fc.updated_at, fc.last_confirmed_at, fc.created_at, 0) DESC LIMIT ?`, append(params, limit)...); err == nil {
			for _, item := range rows {
				facts = append(facts, memoryRecordPayload("fact", item))
			}
		}
		if rows, err := queryMemoryRows(`SELECT DISTINCT e.episode_id, e.title, e.summary, e.source, e.paragraph_count, e.event_time_start, e.event_time_end, e.updated_at FROM episode_paragraphs ep JOIN episodes e ON e.episode_id = ep.episode_id WHERE ep.paragraph_hash IN (`+placeholders+`) ORDER BY e.updated_at DESC LIMIT ?`, append(params, limit)...); err == nil {
			for _, item := range rows {
				episodes = append(episodes, map[string]any{
					"id": anyTrimmed(item["episode_id"]), "title": anyTrimmed(item["title"]),
					"summary": trimMemoryText(item["summary"], 200), "source": anyTrimmed(item["source"]),
					"paragraph_count":  anyInt(item["paragraph_count"]),
					"event_time_start": memoryFloatOrNil(item["event_time_start"]),
					"event_time_end":   memoryFloatOrNil(item["event_time_end"]),
					"updated_at":       memoryFloatOrNil(item["updated_at"]),
				})
			}
		}
	}
	if recordType == "entity" && !memoryItemsContain(entities, anyTrimmed(record["id"])) {
		entities = append([]map[string]any{record}, entities...)
	}
	if recordType == "relation" && !memoryItemsContain(relations, anyTrimmed(record["id"])) {
		relations = append([]map[string]any{record}, relations...)
	}
	if recordType == "fact" && !memoryItemsContain(facts, anyTrimmed(record["id"])) {
		facts = append([]map[string]any{record}, facts...)
	}

	factEvidence := []map[string]any{}
	factTransitions := []map[string]any{}
	if recordType == "fact" {
		claimID := anyTrimmed(record["id"])
		factEvidence = queryMemoryRowsOptional(`SELECT evidence_type, evidence_id, stance, weight, observed_at, metadata_json FROM fact_evidence WHERE claim_id = ? ORDER BY observed_at DESC LIMIT ?`, claimID, limit)
		for _, item := range factEvidence {
			item["metadata"] = decodeMemoryJSONPayload(item["metadata_json"], map[string]any{})
			delete(item, "metadata_json")
		}
		factTransitions = queryMemoryRowsOptional(`SELECT transition_id, old_claim_id, new_claim_id, transition_type, reason, evidence_type, evidence_id, created_at FROM fact_transitions WHERE old_claim_id = ? OR new_claim_id = ? ORDER BY created_at DESC LIMIT ?`, claimID, claimID, limit)
	}

	profiles := []map[string]any{}
	relationIDs := make([]string, 0, len(relations))
	for _, item := range relations {
		if id := anyTrimmed(item["id"]); id != "" {
			relationIDs = append(relationIDs, id)
		}
	}
	// 画像快照通过 evidence_ids_json / fact_claim_ids_json 与段落、事实关联，对齐 Python
	paragraphHashes := make([]string, 0, len(paragraphs))
	for _, item := range paragraphs {
		if hash := anyTrimmed(item["id"]); hash != "" {
			paragraphHashes = append(paragraphHashes, hash)
		}
	}
	factIDs := make([]string, 0, len(facts))
	for _, item := range facts {
		if id := anyTrimmed(item["id"]); id != "" {
			factIDs = append(factIDs, id)
		}
	}
	profileClauses := make([]string, 0, 2)
	profileParams := make([]any, 0, len(paragraphHashes)+len(factIDs)+1)
	if len(paragraphHashes) > 0 {
		profileClauses = append(profileClauses, `EXISTS (
			SELECT 1 FROM json_each(COALESCE(s.evidence_ids_json, '[]')) evidence
			WHERE CAST(evidence.value AS TEXT) IN (`+memoryPlaceholders(len(paragraphHashes))+`))`)
		profileParams = append(profileParams, memoryAnyParams(paragraphHashes)...)
	}
	if len(factIDs) > 0 {
		profileClauses = append(profileClauses, `EXISTS (
			SELECT 1 FROM json_each(COALESCE(s.fact_claim_ids_json, '[]')) claim
			WHERE CAST(claim.value AS TEXT) IN (`+memoryPlaceholders(len(factIDs))+`))`)
		profileParams = append(profileParams, memoryAnyParams(factIDs)...)
	}
	if len(profileClauses) > 0 {
		profileParams = append(profileParams, limit)
		profileRows := queryMemoryRowsOptional(`SELECT s.person_id, s.profile_version, s.profile_text,
			       s.updated_at, s.source_note
			FROM person_profile_snapshots s
			JOIN (
				SELECT person_id, MAX(profile_version) AS max_version
				FROM person_profile_snapshots GROUP BY person_id
			) latest ON latest.person_id = s.person_id AND latest.max_version = s.profile_version
			WHERE `+strings.Join(profileClauses, " OR ")+`
			ORDER BY s.updated_at DESC
			LIMIT ?`, profileParams...)
		for _, item := range profileRows {
			profiles = append(profiles, map[string]any{
				"person_id":       anyTrimmed(item["person_id"]),
				"profile_version": anyInt(item["profile_version"]),
				"profile_text":    trimMemoryText(item["profile_text"], 200),
				"updated_at":      memoryFloatOrNil(item["updated_at"]),
				"source_note":     anyTrimmed(item["source_note"]),
			})
		}
	}
	graphJobs := []map[string]any{}
	if len(relationIDs) > 0 {
		placeholders := memoryPlaceholders(len(relationIDs))
		graphJobs = queryMemoryRowsOptional(`SELECT relation_hash, desired_active, status, attempt_count, last_error, updated_at FROM relation_graph_projection_jobs WHERE relation_hash IN (`+placeholders+`) ORDER BY updated_at DESC`, memoryAnyParams(relationIDs)...)
	}

	factStatus := strings.ToLower(anyTrimmed(record["status"]))
	factActions := []string{"edit_fact", "retract_fact", "profile"}
	if factStatus == "retracted" || factStatus == "superseded" {
		factActions = []string{"restore_fact", "profile"}
	}
	actionsByType := map[string][]string{
		"paragraph": {"graph", "correct", "delete"},
		"entity":    {"graph", "delete"},
		"relation":  {"graph", "correct", "reinforce", "freeze", "protect", "delete"},
		"fact":      factActions,
		"episode":   {"graph"},
	}
	related := map[string]any{
		"paragraphs": paragraphs, "entities": entities, "relations": relations,
		"facts": facts, "episodes": episodes, "profiles": profiles,
	}
	counts := map[string]int{}
	for key, value := range related {
		counts[key] = len(value.([]map[string]any))
	}
	return map[string]any{
		"success": true, "record": record, "related": related, "counts": counts,
		"fact_evidence": factEvidence, "fact_transitions": factTransitions,
		"projection": map[string]any{
			"graph_jobs": graphJobs, "graph_pending_count": len(graphJobs),
		},
		"available_actions": actionsByType[recordType],
	}, http.StatusOK, nil
}

func memoryItemsContain(items []map[string]any, id string) bool {
	for _, item := range items {
		if anyTrimmed(item["id"]) == id {
			return true
		}
	}
	return false
}

func memoryRecordDetailRow(recordType, recordID string) (map[string]any, error) {
	var sql string
	switch recordType {
	case "paragraph":
		sql = "SELECT * FROM paragraphs WHERE hash = ? LIMIT 1"
	case "entity":
		rows, err := queryMemoryRows(`SELECT e.*, (SELECT COUNT(DISTINCT pe.paragraph_hash) FROM paragraph_entities pe JOIN paragraphs p ON p.hash = pe.paragraph_hash WHERE pe.entity_hash = e.hash AND COALESCE(p.is_deleted, 0) = 0) AS active_evidence_count FROM entities e WHERE e.hash = ? OR LOWER(TRIM(e.name)) = LOWER(TRIM(?)) LIMIT 1`, recordID, recordID)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		return rows[0], nil
	case "relation":
		sql = "SELECT * FROM relations WHERE hash = ? LIMIT 1"
	case "episode":
		sql = "SELECT * FROM episodes WHERE episode_id = ? LIMIT 1"
	default:
		sql = "SELECT * FROM fact_claims WHERE claim_id = ? LIMIT 1"
	}
	rows, err := queryMemoryRows(sql, recordID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func memoryRelatedParagraphHashes(recordType string, record map[string]any, limit int) ([]string, error) {
	switch recordType {
	case "paragraph":
		return []string{anyTrimmed(record["hash"])}, nil
	case "entity":
		rows, err := queryMemoryRows(`SELECT DISTINCT p.hash FROM paragraph_entities pe JOIN paragraphs p ON p.hash = pe.paragraph_hash WHERE pe.entity_hash = ? AND COALESCE(p.is_deleted, 0) = 0 ORDER BY COALESCE(p.updated_at, p.created_at, 0) DESC LIMIT ?`, anyTrimmed(record["hash"]), limit)
		if err != nil {
			return nil, err
		}
		return memoryHashes(rows), nil
	case "relation":
		rows, err := queryMemoryRows(`SELECT DISTINCT p.hash FROM paragraph_relations pr JOIN paragraphs p ON p.hash = pr.paragraph_hash WHERE pr.relation_hash = ? AND COALESCE(p.is_deleted, 0) = 0 ORDER BY COALESCE(p.updated_at, p.created_at, 0) DESC LIMIT ?`, anyTrimmed(record["hash"]), limit)
		if err != nil {
			return nil, err
		}
		return memoryHashes(rows), nil
	case "episode":
		rows, err := queryMemoryRows(`SELECT DISTINCT p.hash FROM episode_paragraphs ep JOIN paragraphs p ON p.hash = ep.paragraph_hash WHERE ep.episode_id = ? AND COALESCE(p.is_deleted, 0) = 0 ORDER BY ep.position ASC, COALESCE(p.updated_at, p.created_at, 0) DESC LIMIT ?`, anyTrimmed(record["episode_id"]), limit)
		if err != nil {
			return nil, err
		}
		return memoryHashes(rows), nil
	}
	evidenceRows, err := queryMemoryRows(`SELECT evidence_id FROM fact_evidence WHERE claim_id = ? AND evidence_type = 'paragraph' ORDER BY observed_at DESC LIMIT ?`, anyTrimmed(record["claim_id"]), limit)
	if err != nil {
		return nil, nil
	}
	ids := make([]string, 0, len(evidenceRows))
	for _, item := range evidenceRows {
		if id := anyTrimmed(item["evidence_id"]); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := queryMemoryRows(`SELECT hash FROM paragraphs WHERE hash IN (`+memoryPlaceholders(len(ids))+`) AND COALESCE(is_deleted, 0) = 0`, memoryAnyParams(ids)...)
	if err != nil {
		return nil, err
	}
	return memoryHashes(rows), nil
}

func memoryHashes(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, item := range rows {
		if hash := anyTrimmed(item["hash"]); hash != "" {
			out = append(out, hash)
		}
	}
	return out
}

func memoryPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func memoryAnyParams(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

func (d Deps) handleMemoryQueryAggregate(w http.ResponseWriter, r *http.Request) {
	query := mqString(r, "query")
	limit, ok := mqInt(w, r, "limit", 20, 1, 200)
	if !ok {
		return
	}
	payload := map[string]any{
		"query": query, "limit": limit, "mode": "aggregate", "respect_filter": false,
		"chat_id": mqString(r, "chat_id"), "person_id": mqString(r, "person_id"),
	}
	if raw := mqString(r, "time_start"); raw != "" {
		payload["time_start"] = raw
	}
	if raw := mqString(r, "time_end"); raw != "" {
		payload["time_end"] = raw
	}
	host := memorixDefaultHost()
	if host == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "A_Memorix 运行时尚未初始化")
		return
	}
	result, err := host.Search(r.Context(), searchRequestForAggregate(query, limit, payload))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (d Deps) handleMemoryTimeline(w http.ResponseWriter, r *http.Request) {
	chatID := mqString(r, "chat_id")
	if chatID == "" {
		writeAPIError(w, http.StatusBadRequest, "chat_id 不能为空")
		return
	}
	limit, ok := mqInt(w, r, "limit", 100, 1, 500)
	if !ok {
		return
	}
	var timeStart, timeEnd *float64
	if raw := mqString(r, "time_start"); raw != "" {
		parsed, ok := mqFloat(w, r, "time_start", 0, -1e18, 1e18)
		if !ok {
			return
		}
		timeStart = &parsed
	}
	if raw := mqString(r, "time_end"); raw != "" {
		parsed, ok := mqFloat(w, r, "time_end", 0, -1e18, 1e18)
		if !ok {
			return
		}
		timeEnd = &parsed
	}
	if timeStart != nil && timeEnd != nil && *timeStart > *timeEnd {
		writeAPIError(w, http.StatusBadRequest, "time_start 不能晚于 time_end")
		return
	}
	accepted := map[string]struct{}{}
	for _, item := range mqCSV(strings.ToLower(mqString(r, "types"))) {
		if item != "all" {
			accepted[item] = struct{}{}
		}
	}
	chatName, chatExists := d.memoryTimelineChatInfo(r.Context(), chatID)
	if !chatExists {
		writeAPIError(w, http.StatusBadRequest, "聊天流不存在: "+chatID)
		return
	}
	events := []map[string]any{}
	paragraphEvents, err := memoryTimelineParagraphs(chatID, timeStart, timeEnd, accepted, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "查询长期记忆 timeline 失败: "+err.Error())
		return
	}
	events = append(events, paragraphEvents...)
	events = append(events, memoryTimelineEpisodes(chatID, timeStart, timeEnd, accepted, limit)...)
	events = append(events, memoryTimelineFacts(chatID, timeStart, timeEnd, accepted, limit)...)
	events = append(events, memoryTimelineFeedbackEvents(chatID, timeStart, timeEnd, accepted, limit)...)
	events = append(events, memoryTimelineDeleteEvents(chatID, timeStart, timeEnd, accepted, limit)...)
	events = append(events, memoryTimelineProfileEvents(chatID, timeStart, timeEnd, accepted, limit)...)
	events = append(events, memoryTimelineMaintenanceEvents(chatID, timeStart, timeEnd, accepted, limit)...)
	events = memoryDedupeEvents(events)
	sort.SliceStable(events, func(i, j int) bool {
		left, _ := anyFloat(events[i]["occurred_at"])
		right, _ := anyFloat(events[j]["occurred_at"])
		return left > right
	})
	if len(events) > limit {
		events = events[:limit]
	}
	byType := map[string]int{}
	for _, event := range events {
		byType[fmt.Sprint(event["category"])]++
		byType[fmt.Sprint(event["event_type"])]++
	}
	var minTime, maxTime float64
	if len(events) > 0 {
		maxTime, _ = anyFloat(events[0]["occurred_at"])
		minTime, _ = anyFloat(events[len(events)-1]["occurred_at"])
	} else {
		now := float64(time.Now().Unix())
		minTime = now - 7*24*3600
		maxTime = now
		if timeStart != nil {
			minTime = *timeStart
		}
		if timeEnd != nil {
			maxTime = *timeEnd
		}
	}
	var startValue, endValue any
	if timeStart != nil {
		startValue = *timeStart
	}
	if timeEnd != nil {
		endValue = *timeEnd
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"chat":    map[string]any{"chat_id": chatID, "chat_name": chatName},
		"range": map[string]any{
			"time_start": startValue, "time_end": endValue,
			"min_time": minTime, "max_time": maxTime,
		},
		"items":   events,
		"summary": map[string]any{"total": len(events), "by_type": byType},
	})
}

func memoryTimelineParagraphs(chatID string, timeStart, timeEnd *float64, accepted map[string]struct{}, limit int) ([]map[string]any, error) {
	if !memoryTimelineAccept(accepted, "paragraph") {
		return nil, nil
	}
	sql := `SELECT hash, content, source, knowledge_type, created_at, updated_at, metadata
		FROM paragraphs
		WHERE COALESCE(is_deleted, 0) = 0
		  AND (source = ? OR LOWER(COALESCE(metadata, '')) LIKE ?)`
	params := []any{chatID, "%" + strings.ToLower(chatID) + "%"}
	sql, params = memoryTimelineTimeClause(sql, "COALESCE(updated_at, created_at)", timeStart, timeEnd, params)
	sql += " ORDER BY COALESCE(updated_at, created_at, 0) DESC LIMIT ?"
	params = append(params, limit)
	rows, err := queryMemoryRows(sql, params...)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		occurred, ok := anyFloat(row["updated_at"])
		if !ok {
			occurred, _ = anyFloat(row["created_at"])
		}
		hash := anyTrimmed(row["hash"])
		out = append(out, memoryTimelineEvent("paragraph", "content", occurred, chatID, hash,
			trimMemoryText(row["content"], 88), trimMemoryText(row["content"], 240),
			map[string]any{"tab": "graph", "params": map[string]any{"paragraph_hash": hash}},
			1, anyTrimmed(row["source"]), "", map[string]any{
				"knowledge_type": firstNonEmptyText(row["knowledge_type"], "mixed"),
				"hash":           hash,
			}))
	}
	return out, nil
}

func memoryTimelineEpisodes(chatID string, timeStart, timeEnd *float64, accepted map[string]struct{}, limit int) []map[string]any {
	if !memoryTimelineAccept(accepted, "episode") {
		return nil
	}
	sql := `SELECT episode_id, title, summary, source, paragraph_count, event_time_start, event_time_end,
		created_at, updated_at FROM episodes WHERE source = ?`
	params := []any{chatID}
	sql, params = memoryTimelineTimeClause(sql, "COALESCE(updated_at, event_time_end, created_at)", timeStart, timeEnd, params)
	sql += " ORDER BY COALESCE(updated_at, event_time_end, created_at, 0) DESC LIMIT ?"
	params = append(params, limit)
	rows := queryMemoryRowsOptional(sql, params...)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		occurred, ok := anyFloat(row["updated_at"])
		if !ok {
			occurred, _ = anyFloat(row["event_time_end"])
		}
		if !ok {
			occurred, _ = anyFloat(row["created_at"])
		}
		id := anyTrimmed(row["episode_id"])
		out = append(out, memoryTimelineEvent("episode", "content", occurred, chatID, id,
			firstNonEmptyText(row["title"], "Episode"), trimMemoryText(row["summary"], 240),
			map[string]any{"tab": "graph", "params": map[string]any{"episode_id": id}},
			anyInt(row["paragraph_count"]), id, anyTrimmed(row["source"]), map[string]any{
				"event_time_start": memoryFloatOrNil(row["event_time_start"]),
				"event_time_end":   memoryFloatOrNil(row["event_time_end"]),
			}))
	}
	return out
}

func memoryTimelineFacts(chatID string, timeStart, timeEnd *float64, accepted map[string]struct{}, limit int) []map[string]any {
	if !memoryTimelineAccept(accepted, "fact") {
		return nil
	}
	sql := `SELECT claim_id, fact_key, value_text, status, created_at, updated_at, last_confirmed_at
		FROM fact_claims WHERE scope_type = 'chat' AND scope_id = ?`
	params := []any{chatID}
	sql, params = memoryTimelineTimeClause(sql, "COALESCE(updated_at, last_confirmed_at, created_at)", timeStart, timeEnd, params)
	sql += " ORDER BY COALESCE(updated_at, last_confirmed_at, created_at, 0) DESC LIMIT ?"
	params = append(params, limit)
	rows := queryMemoryRowsOptional(sql, params...)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		occurred, ok := anyFloat(row["updated_at"])
		if !ok {
			occurred, _ = anyFloat(row["last_confirmed_at"])
		}
		if !ok {
			occurred, _ = anyFloat(row["created_at"])
		}
		id := anyTrimmed(row["claim_id"])
		title := trimMemoryText(row["value_text"], 88)
		if title == "" {
			title = "事实变更"
		}
		out = append(out, memoryTimelineEvent("fact", "fact", occurred, chatID, id, title,
			trimMemoryText(row["value_text"], 240),
			map[string]any{"tab": "facts", "params": map[string]any{"claim_id": id}},
			1, id, "", map[string]any{"status": anyTrimmed(row["status"])}))
	}
	return out
}

func memoryTimelineAccept(accepted map[string]struct{}, eventType string) bool {
	if len(accepted) == 0 {
		return true
	}
	_, ok := accepted[eventType]
	return ok
}

func memoryTimelineTimeClause(sql, expression string, start, end *float64, params []any) (string, []any) {
	if start != nil {
		sql += " AND " + expression + " >= ?"
		params = append(params, *start)
	}
	if end != nil {
		sql += " AND " + expression + " <= ?"
		params = append(params, *end)
	}
	return sql, params
}

func memoryTimelineEvent(eventType, category string, occurredAt float64, chatID, keyID, title, summary string, jumpTarget map[string]any, objectCount int, source, attribution string, metadata map[string]any) map[string]any {
	if objectCount < 1 {
		objectCount = 1
	}
	safeKey := keyID
	if safeKey == "" {
		safeKey = source
	}
	if safeKey == "" {
		safeKey = title
	}
	eventID := fmt.Sprintf("%s:%s:%.3f", eventType, safeKey, occurredAt)
	return map[string]any{
		"event_id":   eventType + ":" + safeKey + ":" + fmt.Sprintf("%.3f", occurredAt),
		"event_type": eventType, "category": category, "occurred_at": occurredAt,
		"chat_id": chatID, "chat_name": chatID, "title": title, "summary": summary,
		"object_count": objectCount, "key_id": keyID, "source": source,
		"attribution": attribution, "metadata": metadata, "jump_target": jumpTarget,
		"_event_id": eventID,
	}
}

func memoryDedupeEvents(events []map[string]any) []map[string]any {
	seen := map[string]struct{}{}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		key := fmt.Sprint(event["event_id"])
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		delete(event, "_event_id")
		out = append(out, event)
	}
	return out
}
