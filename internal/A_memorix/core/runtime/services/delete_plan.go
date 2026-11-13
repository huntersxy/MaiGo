package services

// 删除计划构建与预览，对齐 Python delete_admin_service.py 的计划段

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// CleanupRunner 是存储清理任务执行端口，负责向量与图 outbox 的落盘
type CleanupRunner interface {
	ProcessPendingStorageCleanupJobs(ctx context.Context, operationID string, limit int) (map[string]any, error)
}

// GraphRebuilder 是关系图重建端口
type GraphRebuilder interface {
	RebuildGraphFromMetadata(ctx context.Context) (map[string]any, error)
}

// ImportManifestInvalidator 是导入清单失效端口
type ImportManifestInvalidator interface {
	InvalidateImportManifestForSources(ctx context.Context, sources []string)
}

// deletePlan 对齐 _build_delete_plan 的返回结构
type deletePlan struct {
	Success              bool
	Mode                 string
	Selector             map[string]any
	Items                []map[string]any
	Counts               map[string]any
	VectorIDs            map[string][]string
	Sources              []string
	TargetHashes         map[string][]string
	RequestedSourceCount int
	MatchedSourceCount   int
	ErrorMessage         string
}

// buildDeletePlan 对齐 _build_delete_plan
func (s *DeleteAdminService) buildDeletePlan(mode string, selector map[string]any) deletePlan {
	plan := deletePlan{
		Mode:         mode,
		Selector:     selector,
		Items:        []map[string]any{},
		Counts:       map[string]any{},
		VectorIDs:    map[string][]string{"paragraphs": {}, "entities": {}, "relations": {}},
		Sources:      []string{},
		TargetHashes: map[string][]string{"relations": {}, "paragraphs": {}, "entities": {}, "sources": {}, "matched_sources": {}},
	}
	paragraphHashes := map[string]struct{}{}
	entityHashes := map[string]struct{}{}
	relationHashes := map[string]struct{}{}
	sourceSet := map[string]struct{}{}
	matchedSources := map[string]struct{}{}

	appendParagraph := func(row map[string]any) {
		hashValue := rowString(row, "hash")
		if hashValue == "" {
			return
		}
		if _, ok := paragraphHashes[hashValue]; ok {
			return
		}
		paragraphHashes[hashValue] = struct{}{}
		snapshot := s.snapshotParagraphItem(row)
		if snapshot != nil {
			plan.Items = append(plan.Items, snapshot)
		}
		if source := strings.TrimSpace(rowString(row, "source")); source != "" {
			sourceSet[source] = struct{}{}
		}
	}
	appendEntity := func(row map[string]any) {
		hashValue := rowString(row, "hash")
		if hashValue == "" {
			return
		}
		if _, ok := entityHashes[hashValue]; ok {
			return
		}
		entityHashes[hashValue] = struct{}{}
		if snapshot := s.snapshotEntityItem(row); snapshot != nil {
			plan.Items = append(plan.Items, snapshot)
		}
	}
	appendRelation := func(hashValue string) {
		token := strings.TrimSpace(hashValue)
		if token == "" {
			return
		}
		if _, ok := relationHashes[token]; ok {
			return
		}
		relationHashes[token] = struct{}{}
		if snapshot := s.snapshotRelationItem(token); snapshot != nil {
			plan.Items = append(plan.Items, snapshot)
		}
	}

	switch mode {
	case "relation":
		hashes := []string{}
		for _, token := range mergeTokens(selector["hashes"], selector["items"], selector["hash"]) {
			if hashPattern.MatchString(strings.ToLower(token)) {
				hashes = append(hashes, token)
			}
		}
		if len(hashes) == 0 {
			hashes = s.resolveRelationHashes(selector)
		}
		for _, hashValue := range hashes {
			appendRelation(hashValue)
		}
	case "paragraph":
		for _, row := range s.resolveParagraphTargets(selector, false) {
			appendParagraph(row)
		}
	case "entity":
		for _, row := range s.resolveEntityTargets(selector) {
			appendEntity(row)
			name := rowString(row, "name")
			for _, relationRow := range s.relatedRelationsForEntity(name) {
				appendRelation(rowString(relationRow, "hash"))
			}
		}
	case "source":
		sources := s.resolveSourceTargets(selector)
		plan.TargetHashes["sources"] = sources
		plan.RequestedSourceCount = len(sources)
		for _, source := range sources {
			rows, err := s.Meta().Query(
				"SELECT * FROM paragraphs WHERE source = ? AND COALESCE(is_deleted, 0) = 0 ORDER BY created_at ASC", source)
			if err != nil {
				continue
			}
			if len(rows) > 0 {
				matchedSources[source] = struct{}{}
			}
			for _, row := range rows {
				appendParagraph(row)
			}
		}
		plan.MatchedSourceCount = len(matchedSources)
		plan.TargetHashes["matched_sources"] = mapKeys(matchedSources)
	case "mixed":
		for _, source := range s.resolveSourceTargets(selector) {
			plan.TargetHashes["sources"] = append(plan.TargetHashes["sources"], source)
			rows, err := s.Meta().Query(
				"SELECT * FROM paragraphs WHERE source = ? AND COALESCE(is_deleted, 0) = 0 ORDER BY created_at ASC", source)
			if err != nil {
				continue
			}
			for _, row := range rows {
				appendParagraph(row)
			}
		}
		for _, row := range s.resolveEntityTargets(selector) {
			appendEntity(row)
			name := rowString(row, "name")
			for _, relationRow := range s.relatedRelationsForEntity(name) {
				appendRelation(rowString(relationRow, "hash"))
			}
		}
		for _, row := range s.resolveParagraphTargets(selector, false) {
			appendParagraph(row)
		}
		for _, hashValue := range tokens(selector["relation_hashes"]) {
			appendRelation(hashValue)
		}
	default:
		plan.ErrorMessage = fmt.Sprintf("不支持的 delete mode: %s", mode)
		return plan
	}

	// 段落级联：删除段落时若无剩余段落支撑则一并删除关系
	for hashValue := range paragraphHashes {
		rows, err := s.Meta().GetParagraphRelations(hashValue)
		if err != nil {
			continue
		}
		for _, row := range rows {
			relationHash := rowString(row, "relation_hash")
			if relationHash == "" {
				continue
			}
			if _, already := relationHashes[relationHash]; already {
				continue
			}
			if !s.relationHasRemainingParagraphs(relationHash, paragraphHashes) {
				appendRelation(relationHash)
			}
		}
	}

	plan.TargetHashes["paragraphs"] = mapKeys(paragraphHashes)
	plan.TargetHashes["entities"] = mapKeys(entityHashes)
	plan.TargetHashes["relations"] = mapKeys(relationHashes)
	for source := range sourceSet {
		plan.Sources = append(plan.Sources, source)
	}
	sort.Strings(plan.Sources)
	plan.VectorIDs["paragraphs"] = plan.TargetHashes["paragraphs"]
	plan.VectorIDs["entities"] = plan.TargetHashes["entities"]
	plan.VectorIDs["relations"] = plan.TargetHashes["relations"]
	plan.Counts["paragraphs"] = len(plan.TargetHashes["paragraphs"])
	plan.Counts["entities"] = len(plan.TargetHashes["entities"])
	plan.Counts["relations"] = len(plan.TargetHashes["relations"])
	plan.Counts["sources"] = len(plan.Sources)
	if len(plan.TargetHashes["sources"]) > 0 {
		plan.Counts["requested_sources"] = plan.RequestedSourceCount
		plan.Counts["matched_sources"] = plan.MatchedSourceCount
	}
	plan.Success = len(plan.Items) > 0
	if !plan.Success {
		plan.ErrorMessage = "未命中可删除内容"
	}
	return plan
}

// relatedRelationsForEntity 查询实体名关联的未失效关系
func (s *DeleteAdminService) relatedRelationsForEntity(name string) []map[string]any {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	out := []map[string]any{}
	seen := map[string]struct{}{}
	for _, pair := range [][2]string{{name, ""}, {"", name}} {
		rows, err := s.Meta().GetRelations(pair[0], "", pair[1], false)
		if err != nil {
			continue
		}
		for _, row := range rows {
			hashValue := rowString(row, "hash")
			if hashValue == "" {
				continue
			}
			if _, ok := seen[hashValue]; ok {
				continue
			}
			seen[hashValue] = struct{}{}
			out = append(out, row)
		}
	}
	sort.SliceStable(out, func(i int, j int) bool { return rowString(out[i], "hash") < rowString(out[j], "hash") })
	return out
}

// previewDeleteAction 对齐 _preview_delete_action
func (s *DeleteAdminService) previewDeleteAction(mode string, selector map[string]any) map[string]any {
	plan := s.buildDeletePlan(mode, selector)
	if !plan.Success {
		return map[string]any{"success": false, "error": plan.ErrorMessage}
	}
	previews := make([]any, 0, 100)
	for index, item := range plan.Items {
		if index >= 100 {
			break
		}
		itemType := asString(item["item_type"])
		payload := asMap(item["payload"])
		source := map[string]any(nil)
		switch itemType {
		case "relation":
			source = asMap(payload["relation"])
		case "paragraph":
			source = asMap(payload["paragraph"])
		case "entity":
			source = asMap(payload["entity"])
		}
		previews = append(previews, s.buildDeletePreviewItem(itemType, asString(item["item_hash"]), asString(item["item_key"]), source))
	}
	return map[string]any{
		"success":                true,
		"mode":                   mode,
		"selector":               selector,
		"counts":                 plan.Counts,
		"requested_source_count": plan.RequestedSourceCount,
		"matched_source_count":   plan.MatchedSourceCount,
		"sources":                plan.Sources,
		"vector_ids":             plan.VectorIDs,
		"items":                  previews,
		"item_count":             len(plan.Items),
		"dry_run":                true,
	}
}

// mapKeys 返回映射键的有序切片
func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
