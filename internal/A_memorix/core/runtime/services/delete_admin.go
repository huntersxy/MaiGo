package services

// 删除管理服务，对齐 Python core/runtime/services/delete_admin_service.py 的选择器与计划段

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/utils"
)

var hashPattern = regexp.MustCompile("^[0-9a-f]{64}$")

// DeleteAdminOptions 汇总删除服务依赖
type DeleteAdminOptions struct {
	Base                   *Base
	RelationVectorsEnabled bool
	Cleanup                CleanupRunner
	GraphRebuild           GraphRebuilder
	ImportInvalidator      ImportManifestInvalidator
}

// DeleteAdminService 对齐 MemoryDeleteAdminService
type DeleteAdminService struct {
	*Base
	relationVectorsEnabled bool
	cleanup                CleanupRunner
	graphRebuild           GraphRebuilder
	importInvalidator      ImportManifestInvalidator
}

// NewDeleteAdminService 构造删除管理服务
func NewDeleteAdminService(options DeleteAdminOptions) *DeleteAdminService {
	return &DeleteAdminService{
		Base:                   options.Base,
		relationVectorsEnabled: options.RelationVectorsEnabled,
		cleanup:                options.Cleanup,
		graphRebuild:           options.GraphRebuild,
		importInvalidator:      options.ImportInvalidator,
	}
}

// ProcessPendingStorageCleanupJobs 执行跨存储清理 outbox，对齐 process_pending_storage_cleanup_jobs
func (s *DeleteAdminService) ProcessPendingStorageCleanupJobs(ctx context.Context, operationID string, limit int) (map[string]any, error) {
	if s.cleanup == nil {
		return map[string]any{"completed": 0, "cancelled": 0, "failed": 0, "deleted_vectors": 0}, nil
	}
	return s.cleanup.ProcessPendingStorageCleanupJobs(ctx, operationID, limit)
}

// MemoryDeleteAdmin 对齐 memory_delete_admin
func (s *DeleteAdminService) MemoryDeleteAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	if s.Meta() == nil {
		return map[string]any{"success": false, "error": "metadata store 未初始化"}, nil
	}
	act := strings.ToLower(strings.TrimSpace(action))
	selector := s.selectorDict(payload)
	mode := strings.ToLower(strings.TrimSpace(asString(payload["mode"])))
	reason := strings.TrimSpace(asString(payload["reason"]))
	requestedBy := strings.TrimSpace(asString(payload["requested_by"]))
	operationID := strings.TrimSpace(asString(payload["operation_id"]))

	switch act {
	case "preview":
		return s.previewDeleteAction(mode, selector), nil
	case "execute":
		result, err := s.executeDeleteAction(ctx, mode, selector, reason, requestedBy, payload)
		if err != nil {
			return result, nil
		}
		if s.importInvalidator != nil {
			s.importInvalidator.InvalidateImportManifestForSources(ctx, asStringSlice(result["sources"]))
		}
		return result, nil
	case "restore":
		return s.restoreDeleteAction(ctx, mode, selector, operationID, requestedBy, reason), nil
	case "get_operation":
		operation, err := s.Meta().GetDeleteOperation(operationID)
		if err != nil {
			return map[string]any{"success": false, "operation": nil, "error": err.Error()}, nil
		}
		message := ""
		if operation == nil {
			message = "operation 不存在"
		}
		return map[string]any{"success": operation != nil, "operation": operation, "error": message}, nil
	case "list_operations":
		limit := intOr(payload["limit"], 50, 1)
		items, err := s.Meta().ListDeleteOperations(limit, mode)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}, nil
		}
		if items == nil {
			items = []map[string]any{}
		}
		return map[string]any{"success": true, "items": items, "count": len(items)}, nil
	case "purge":
		graceHours := optionalFloat(payload["grace_hours"])
		limit := intOr(payload["limit"], 1000, 1)
		return s.purgeDeletedMemory(graceHours, limit), nil
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的 delete action: %s", act)}, nil
	}
}

// selectorDict 对齐 _selector_dict，payload 缺省时整体作为选择器
func (s *DeleteAdminService) selectorDict(payload map[string]any) map[string]any {
	if raw, ok := payload["selector"]; ok {
		switch typed := raw.(type) {
		case map[string]any:
			return typed
		case []any:
			return map[string]any{"items": typed}
		case string:
			return map[string]any{"query": strings.TrimSpace(typed)}
		case nil:
			return map[string]any{}
		}
	}
	reserved := map[string]struct{}{
		"action": {}, "mode": {}, "dry_run": {}, "cascade": {}, "operation_id": {},
		"reason": {}, "requested_by": {}, "selector": {}, "expected_relation_states": {},
	}
	out := map[string]any{}
	for key, value := range payload {
		if _, skip := reserved[key]; skip {
			continue
		}
		out[key] = value
	}
	return out
}

// resolveParagraphTargets 对齐 _resolve_paragraph_targets
func (s *DeleteAdminService) resolveParagraphTargets(selector map[string]any, includeDeleted bool) []map[string]any {
	out := []map[string]any{}
	seen := map[string]struct{}{}
	appendRow := func(row map[string]any) {
		if row == nil {
			return
		}
		hashValue := rowString(row, "hash")
		if hashValue == "" {
			return
		}
		if _, ok := seen[hashValue]; ok {
			return
		}
		if rowBool(row, "is_deleted") && !includeDeleted {
			return
		}
		seen[hashValue] = struct{}{}
		out = append(out, row)
	}
	for _, token := range mergeTokens(selector["hashes"], selector["items"], selector["hash"]) {
		row, err := s.Meta().GetParagraph(token)
		if err == nil {
			appendRow(row)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, token := range mergeTokens(selector["query"], selector["content"]) {
		if hashPattern.MatchString(strings.ToLower(token)) {
			if row, err := s.Meta().GetParagraph(token); err == nil {
				appendRow(row)
			}
		}
		rows, err := s.Meta().SearchParagraphsByContent(token)
		if err == nil {
			for _, row := range rows {
				appendRow(row)
			}
		}
	}
	return out
}

// resolveEntityTargets 对齐 _resolve_entity_targets
func (s *DeleteAdminService) resolveEntityTargets(selector map[string]any) []map[string]any {
	out := []map[string]any{}
	seen := map[string]struct{}{}
	appendRow := func(row map[string]any) {
		if row == nil {
			return
		}
		hashValue := rowString(row, "hash")
		if hashValue == "" || rowBool(row, "is_deleted") {
			return
		}
		if _, ok := seen[hashValue]; ok {
			return
		}
		seen[hashValue] = struct{}{}
		out = append(out, row)
	}
	for _, token := range mergeTokens(selector["hashes"], selector["items"], selector["hash"]) {
		if row, err := s.Meta().GetEntity(token); err == nil {
			appendRow(row)
		}
	}
	for _, token := range mergeTokens(selector["names"], selector["name"], selector["query"]) {
		rows, err := s.Meta().Query(
			`SELECT * FROM entities WHERE LOWER(TRIM(name)) = LOWER(TRIM(?)) OR hash = ?
				 ORDER BY appearance_count DESC, created_at ASC`, token, utils.ComputeEntityHash(token))
		if err != nil {
			continue
		}
		for _, row := range rows {
			appendRow(row)
		}
	}
	return out
}

// resolveSourceTargets 对齐 _resolve_source_targets
func (s *DeleteAdminService) resolveSourceTargets(selector map[string]any) []string {
	return dedupeStrings(mergeTokens(selector["sources"], selector["source"], selector["query"], selector["items"]))
}

// resolveRelationHashes 对齐 _resolve_relation_hashes
func (s *DeleteAdminService) resolveRelationHashes(selector map[string]any) []string {
	out := []string{}
	for _, token := range mergeTokens(selector["hashes"], selector["items"], selector["hash"]) {
		lowered := strings.ToLower(token)
		if hashPattern.MatchString(lowered) {
			if row, err := s.Meta().GetRelation(token, true); err == nil && row != nil {
				out = append(out, token)
				continue
			}
		}
		hashes, err := s.Meta().SearchRelationHashesByText(token, 10)
		if err == nil && len(hashes) > 0 {
			out = append(out, hashes...)
			continue
		}
		rows, err := s.Meta().GetRelations(token, "", "", false)
		if err == nil {
			for index, row := range rows {
				if index >= 10 {
					break
				}
				out = append(out, rowString(row, "hash"))
			}
		}
	}
	return dedupeStrings(out)
}

// resolveDeletedRelationHashes 对齐 _resolve_deleted_relation_hashes
func (s *DeleteAdminService) resolveDeletedRelationHashes(selector map[string]any) []string {
	out := []string{}
	for _, token := range mergeTokens(selector["hashes"], selector["items"], selector["hash"], selector["query"], selector["target"]) {
		if hashPattern.MatchString(strings.ToLower(token)) {
			if row, err := s.Meta().GetDeletedRelation(token); err == nil && row != nil {
				out = append(out, token)
				continue
			}
		}
		hashes, err := s.Meta().SearchDeletedRelationHashesByText(token, 10)
		if err == nil {
			out = append(out, hashes...)
		}
	}
	return dedupeStrings(out)
}

// snapshotRelationItem 对齐 _snapshot_relation_item
func (s *DeleteAdminService) snapshotRelationItem(hashValue string) map[string]any {
	row, err := s.Meta().GetRelation(hashValue, true)
	if err != nil || row == nil {
		row, err = s.Meta().GetDeletedRelation(hashValue)
		if err != nil || row == nil {
			return nil
		}
	}
	paragraphHashes := []string{}
	if rows, err := s.Meta().Query(
		"SELECT paragraph_hash FROM paragraph_relations WHERE relation_hash = ? ORDER BY paragraph_hash ASC", hashValue); err == nil {
		for _, item := range rows {
			paragraphHashes = append(paragraphHashes, rowString(item, "paragraph_hash"))
		}
	}
	itemKey := firstNonEmpty(row["hash"], hashValue)
	return map[string]any{
		"item_type": "relation",
		"item_hash": hashValue,
		"item_key":  itemKey,
		"payload":   map[string]any{"relation": row, "paragraph_hashes": paragraphHashes},
	}
}

// snapshotParagraphItem 对齐 _snapshot_paragraph_item
func (s *DeleteAdminService) snapshotParagraphItem(row map[string]any) map[string]any {
	hashValue := rowString(row, "hash")
	entityLinks := []any{}
	if rows, err := s.Meta().GetParagraphEntities(hashValue); err == nil {
		for _, entityRow := range rows {
			entityLinks = append(entityLinks, map[string]any{
				"paragraph_hash": hashValue,
				"entity_hash":    rowString(entityRow, "entity_hash"),
				"mention_count":  rowInt(entityRow, "mention_count"),
			})
		}
	}
	relationHashes := []string{}
	if rows, err := s.Meta().GetParagraphRelations(hashValue); err == nil {
		for _, relationRow := range rows {
			relationHashes = append(relationHashes, rowString(relationRow, "relation_hash"))
		}
	}
	var externalRefs any = []any{}
	if refs, err := s.Meta().ListExternalMemoryRefsByParagraphs([]string{hashValue}); err == nil {
		externalRefs = refs
	}
	factSnapshot := map[string]any{
		"paragraph_hashes": []string{hashValue},
		"claims":           []any{},
		"evidence":         []any{},
	}
	if snapshot, err := s.Meta().SnapshotFactEvidenceForParagraphs([]string{hashValue}, nil); err == nil && snapshot != nil {
		factSnapshot = snapshot
	}
	return map[string]any{
		"item_type": "paragraph",
		"item_hash": hashValue,
		"item_key":  firstNonEmpty(row["hash"], hashValue),
		"payload": map[string]any{
			"paragraph":              row,
			"entity_links":           entityLinks,
			"relation_hashes":        relationHashes,
			"external_refs":          externalRefs,
			"fact_evidence_snapshot": factSnapshot,
		},
	}
}

// snapshotEntityItem 对齐 _snapshot_entity_item
func (s *DeleteAdminService) snapshotEntityItem(row map[string]any) map[string]any {
	hashValue := rowString(row, "hash")
	paragraphLinks := []any{}
	if rows, err := s.Meta().GetParagraphsByEntity(rowString(row, "name")); err == nil {
		for _, paragraphRow := range rows {
			if rowString(paragraphRow, "hash") == "" {
				continue
			}
			paragraphLinks = append(paragraphLinks, map[string]any{
				"paragraph_hash": rowString(paragraphRow, "hash"),
				"entity_hash":    hashValue,
				"mention_count":  rowInt(row, "appearance_count"),
			})
		}
	}
	return map[string]any{
		"item_type": "entity",
		"item_hash": hashValue,
		"item_key":  firstNonEmpty(row["name"], hashValue),
		"payload":   map[string]any{"entity": row, "paragraph_links": paragraphLinks},
	}
}

// relationHasRemainingParagraphs 对齐 _relation_has_remaining_paragraphs
func (s *DeleteAdminService) relationHasRemainingParagraphs(relationHash string, removing map[string]struct{}) bool {
	rows, err := s.Meta().Query(
		`SELECT p.hash AS hash, p.metadata AS metadata FROM paragraph_relations pr
		 JOIN paragraphs p ON p.hash = pr.paragraph_hash
		 WHERE pr.relation_hash = ? AND COALESCE(p.is_deleted, 0) = 0`, relationHash)
	if err != nil {
		return false
	}
	now := float64(time.Now().UnixNano()) / 1e9
	for _, row := range rows {
		if _, removingHash := removing[rowString(row, "hash")]; removingHash {
			continue
		}
		metadata := jsonObject(rowString(row, "metadata"))
		change := asMap(metadata["memory_change"])
		validTo := optionalFloat(change["valid_to"])
		if validTo == nil || *validTo > now {
			return true
		}
	}
	return false
}

// buildDeletePreviewItem 对齐 _build_delete_preview_item
func (s *DeleteAdminService) buildDeletePreviewItem(itemType string, itemHash string, itemKey string, item map[string]any) map[string]any {
	switch itemType {
	case "entity":
		name := itemKey
		if item != nil {
			if text := rowString(item, "name"); text != "" {
				name = text
			}
		}
		return map[string]any{"item_type": "entity", "item_hash": itemHash, "item_key": itemKey, "label": name, "preview": name}
	case "relation":
		subject, predicate, object := "", "", ""
		if item != nil {
			subject, predicate, object = rowString(item, "subject"), rowString(item, "predicate"), rowString(item, "object")
		}
		text := formatRelationText(subject, predicate, object)
		if text == "" {
			text = itemKey
		}
		return map[string]any{"item_type": "relation", "item_hash": itemHash, "item_key": itemKey, "label": text, "preview": text}
	default:
		content := ""
		source := ""
		if item != nil {
			content = rowString(item, "content")
			source = rowString(item, "source")
		}
		label := source
		if label == "" {
			label = itemKey
		}
		return map[string]any{
			"item_type": "paragraph", "item_hash": itemHash, "item_key": itemKey,
			"label": label, "preview": trimText(content, 220), "source": source,
		}
	}
}

// buildStandardDeleteResult 对齐 _build_standard_delete_result
func buildStandardDeleteResult(mode string, operationID string, counts map[string]any, sources []string,
	deletedEntityCount int, deletedRelationCount int, deletedParagraphCount int, deletedSourceCount int,
	deletedVectorCount int, requestedSourceCount int, matchedSourceCount int, errorMessage string) map[string]any {
	if counts == nil {
		counts = map[string]any{}
	}
	counts["entities"] = deletedEntityCount
	counts["relations"] = deletedRelationCount
	counts["paragraphs"] = deletedParagraphCount
	counts["sources"] = deletedSourceCount
	if requestedSourceCount > 0 || matchedSourceCount > 0 {
		counts["requested_sources"] = requestedSourceCount
		counts["matched_sources"] = matchedSourceCount
	}
	deletedCount := deletedEntityCount + deletedRelationCount + deletedParagraphCount + deletedSourceCount
	cleanSources := make([]string, 0, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source) != "" {
			cleanSources = append(cleanSources, source)
		}
	}
	return map[string]any{
		"success":                 errorMessage == "" && deletedCount > 0,
		"mode":                    mode,
		"operation_id":            operationID,
		"counts":                  counts,
		"sources":                 cleanSources,
		"deleted_count":           deletedCount,
		"deleted_entity_count":    deletedEntityCount,
		"deleted_relation_count":  deletedRelationCount,
		"deleted_paragraph_count": deletedParagraphCount,
		"deleted_source_count":    deletedSourceCount,
		"deleted_vector_count":    deletedVectorCount,
		"requested_source_count":  requestedSourceCount,
		"matched_source_count":    matchedSourceCount,
		"error":                   errorMessage,
	}
}

// asStringSlice 把任意列表转换为字符串切片
func asStringSlice(value any) []string {
	return tokens(value)
}

// sortRowsByHash 按 hash 排序行
func sortRowsByHash(rows []map[string]any) []map[string]any {
	out := append([]map[string]any{}, rows...)
	sort.SliceStable(out, func(i int, j int) bool { return rowString(out[i], "hash") < rowString(out[j], "hash") })
	return out
}
