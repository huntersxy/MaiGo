package services

// 图节点与边详情，对齐 Python graph_admin_service.py 的详情段

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// buildGraphNodeDetail 对齐 _build_graph_node_detail
func (s *GraphAdminService) buildGraphNodeDetail(ctx context.Context, nodeID string, relationLimit int, paragraphLimit int, evidenceNodeLimit int) map[string]any {
	if strings.TrimSpace(nodeID) == "" {
		return map[string]any{"success": false, "error": "node_id 不能为空"}
	}
	resolvedName := s.resolveGraphNodeName(nodeID)

	var entityRow map[string]any
	if s.Meta() != nil {
		rows, err := s.Meta().Query(
			`SELECT e.*, (SELECT COUNT(*) FROM paragraph_entities pe JOIN paragraphs p ON p.hash = pe.paragraph_hash
			 WHERE pe.entity_hash = e.hash AND COALESCE(p.is_deleted, 0) = 0) AS active_evidence_count
			 FROM entities e WHERE COALESCE(e.is_deleted, 0) = 0
			 AND (LOWER(TRIM(e.name)) = LOWER(TRIM(?)) OR e.hash = ?)
			 ORDER BY e.appearance_count DESC, e.created_at ASC LIMIT 1`, resolvedName, resolvedName)
		if err == nil && len(rows) > 0 {
			entityRow = rows[0]
		}
	}
	relationRows := s.getRelatedRelationRowsForEntity(resolvedName, relationLimit)
	if len(relationRows) == 0 && entityRow == nil {
		return map[string]any{"success": false, "error": fmt.Sprintf("未找到节点: %s", resolvedName)}
	}

	var directParagraphs []map[string]any
	if s.Meta() != nil {
		if rows, err := s.Meta().GetParagraphsByEntity(resolvedName); err == nil {
			directParagraphs = rows
		}
	}
	relationHashes := make([]string, 0, len(relationRows))
	for _, row := range relationRows {
		relationHashes = append(relationHashes, rowString(row, "hash"))
	}
	relationParagraphHashes := s.queryDistinctParagraphHashesForRelations(relationHashes, 0)
	relationParagraphs := s.loadParagraphRows(relationParagraphHashes)

	paragraphByHash := map[string]map[string]any{}
	for _, row := range directParagraphs {
		hashValue := rowString(row, "hash")
		if hashValue == "" || rowBool(row, "is_deleted") {
			continue
		}
		paragraphByHash[hashValue] = row
	}
	for _, row := range relationParagraphs {
		hashValue := rowString(row, "hash")
		if hashValue == "" {
			continue
		}
		if _, ok := paragraphByHash[hashValue]; ok {
			continue
		}
		paragraphByHash[hashValue] = row
	}
	paragraphRows := make([]map[string]any, 0, len(paragraphByHash))
	for _, row := range paragraphByHash {
		paragraphRows = append(paragraphRows, row)
	}
	sort.SliceStable(paragraphRows, func(i int, j int) bool {
		leftUpdated, rightUpdated := rowFloat(paragraphRows[i], "updated_at"), rowFloat(paragraphRows[j], "updated_at")
		if leftUpdated != rightUpdated {
			return leftUpdated > rightUpdated
		}
		leftCreated, rightCreated := rowFloat(paragraphRows[i], "created_at"), rowFloat(paragraphRows[j], "created_at")
		if leftCreated != rightCreated {
			return leftCreated > rightCreated
		}
		return rowString(paragraphRows[i], "hash") < rowString(paragraphRows[j], "hash")
	})
	if paragraphLimit > 0 && len(paragraphRows) > paragraphLimit {
		paragraphRows = paragraphRows[:paragraphLimit]
	}

	relationSummaries := make([]any, 0, len(relationRows))
	for _, row := range relationRows {
		hashValue := rowString(row, "hash")
		relationSummaries = append(relationSummaries, s.buildRelationSummary(row, s.paragraphHashesForRelation(hashValue)))
	}
	paragraphSummaries := make([]any, 0, len(paragraphRows))
	for _, row := range paragraphRows {
		paragraphSummaries = append(paragraphSummaries, s.buildParagraphSummary(row))
	}
	evidenceGraph := s.buildEvidenceGraph([]string{resolvedName}, relationRows, paragraphRows, evidenceNodeLimit)

	node := map[string]any{"id": resolvedName, "type": "entity", "content": resolvedName}
	if entityRow != nil {
		node["hash"] = rowString(entityRow, "hash")
		node["appearance_count"] = rowInt(entityRow, "appearance_count")
		node["active_evidence_count"] = rowInt(entityRow, "active_evidence_count")
	}
	return map[string]any{
		"success":        true,
		"node":           node,
		"relations":      relationSummaries,
		"paragraphs":     paragraphSummaries,
		"evidence_graph": evidenceGraph,
	}
}

// buildGraphEdgeDetail 对齐 _build_graph_edge_detail
func (s *GraphAdminService) buildGraphEdgeDetail(ctx context.Context, source string, target string, paragraphLimit int, evidenceNodeLimit int) map[string]any {
	sourceName := s.resolveGraphNodeName(source)
	targetName := s.resolveGraphNodeName(target)
	if strings.TrimSpace(sourceName) == "" || strings.TrimSpace(targetName) == "" {
		return map[string]any{"success": false, "error": "source/target 不能为空"}
	}
	relationRows := []map[string]any{}
	if s.Meta() != nil {
		rows, err := s.Meta().Query(
			`SELECT * FROM relations WHERE (is_inactive IS NULL OR is_inactive = 0)
			 AND LOWER(TRIM(subject)) = LOWER(TRIM(?)) AND LOWER(TRIM(object)) = LOWER(TRIM(?))
			 ORDER BY confidence DESC, created_at DESC`, sourceName, targetName)
		if err == nil {
			relationRows = rows
		}
	}
	if len(relationRows) == 0 {
		return map[string]any{"success": false, "error": fmt.Sprintf("未找到边: %s -> %s", sourceName, targetName)}
	}

	relationHashes := make([]string, 0, len(relationRows))
	predicates := make([]string, 0, len(relationRows))
	for _, row := range relationRows {
		relationHashes = append(relationHashes, rowString(row, "hash"))
		predicates = append(predicates, rowString(row, "predicate"))
	}
	predicates = dedupeStrings(predicates)
	paragraphHashes := s.queryDistinctParagraphHashesForRelations(relationHashes, paragraphLimit)
	paragraphRows := s.loadParagraphRows(paragraphHashes)

	relationSummaries := make([]any, 0, len(relationRows))
	for _, row := range relationRows {
		hashValue := rowString(row, "hash")
		relationSummaries = append(relationSummaries, s.buildRelationSummary(row, s.paragraphHashesForRelation(hashValue)))
	}
	paragraphSummaries := make([]any, 0, len(paragraphRows))
	for _, row := range paragraphRows {
		paragraphSummaries = append(paragraphSummaries, s.buildParagraphSummary(row))
	}
	evidenceGraph := s.buildEvidenceGraph([]string{sourceName, targetName}, relationRows, paragraphRows, evidenceNodeLimit)
	weight := 0.0
	if graphStore := s.Graph(); graphStore != nil {
		weight = graphStore.GetEdgeWeight(sourceName, targetName)
	}
	return map[string]any{
		"success": true,
		"edge": map[string]any{
			"source":          sourceName,
			"target":          targetName,
			"weight":          weight,
			"relation_hashes": relationHashes,
			"predicates":      predicates,
			"relation_count":  len(relationHashes),
			"evidence_count":  len(paragraphHashes),
			"label":           buildGraphEdgeLabel(predicates),
		},
		"relations":      relationSummaries,
		"paragraphs":     paragraphSummaries,
		"evidence_graph": evidenceGraph,
	}
}
