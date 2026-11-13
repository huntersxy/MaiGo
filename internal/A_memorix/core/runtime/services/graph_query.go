package services

// 图管理查询与序列化辅助，对齐 Python graph_admin_service.py 的查询段

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// serializeGraph 对齐 _serialize_graph
func (s *GraphAdminService) serializeGraph(limit int) map[string]any {
	graphStore := s.Graph()
	if graphStore == nil {
		return map[string]any{"nodes": []any{}, "edges": []any{}, "total_nodes": 0, "total_edges": 0}
	}
	nodes := graphStore.GetNodes()
	if limit > 0 && len(nodes) > limit {
		nodes = nodes[:limit]
	}
	nodePayload := make([]any, 0, len(nodes))
	nodeSet := make(map[string]struct{}, len(nodes))
	for _, name := range nodes {
		nodeSet[name] = struct{}{}
		attributes := graphStore.GetNodeAttributes(name)
		if attributes == nil {
			attributes = map[string]any{}
		}
		nodePayload = append(nodePayload, map[string]any{"id": name, "name": name, "attributes": attributes})
	}

	entries := graphStore.IterEdgeHashEntries()
	edgePayload := make([]any, 0, len(entries))
	for _, entry := range entries {
		if _, ok := nodeSet[entry.Source]; !ok {
			continue
		}
		if _, ok := nodeSet[entry.Target]; !ok {
			continue
		}
		relationHashes := make([]string, 0, len(entry.Hashes))
		for hashValue := range entry.Hashes {
			if strings.TrimSpace(hashValue) != "" {
				relationHashes = append(relationHashes, hashValue)
			}
		}
		sort.Strings(relationHashes)
		rows := s.queryRelationRowsByHashes(relationHashes, false)
		predicates := make([]string, 0, len(rows))
		for _, row := range rows {
			predicates = append(predicates, rowString(row, "predicate"))
		}
		predicates = dedupeStrings(predicates)
		evidenceHashes := s.queryDistinctParagraphHashesForRelations(relationHashes, 0)
		edgePayload = append(edgePayload, map[string]any{
			"source":          entry.Source,
			"target":          entry.Target,
			"weight":          graphStore.GetEdgeWeight(entry.Source, entry.Target),
			"relation_hashes": relationHashes,
			"predicates":      predicates,
			"relation_count":  len(relationHashes),
			"evidence_count":  len(evidenceHashes),
			"label":           buildGraphEdgeLabel(predicates),
		})
	}

	return map[string]any{
		"nodes":       nodePayload,
		"edges":       edgePayload,
		"total_nodes": graphStore.NumNodes(),
		"total_edges": graphStore.NumEdges(),
	}
}

// graphSearchMatchRank 对齐 _graph_search_match_rank
func graphSearchMatchRank(value string, keyword string) (int, bool) {
	token := strings.ToLower(strings.TrimSpace(value))
	needle := strings.ToLower(strings.TrimSpace(keyword))
	if token == "" || needle == "" {
		return 0, false
	}
	switch {
	case token == needle:
		return 0, true
	case strings.HasPrefix(token, needle):
		return 1, true
	case strings.Contains(token, needle):
		return 2, true
	default:
		return 0, false
	}
}

// pickGraphSearchMatch 对齐 _pick_graph_search_match
func pickGraphSearchMatch(fields [][2]string, keyword string) (string, string, int, bool) {
	bestRank := -1
	bestField := ""
	bestValue := ""
	for _, field := range fields {
		rank, ok := graphSearchMatchRank(field[1], keyword)
		if !ok {
			continue
		}
		if bestRank < 0 || rank < bestRank {
			bestRank = rank
			bestField = field[0]
			bestValue = field[1]
		}
	}
	if bestRank < 0 {
		return "", "", 0, false
	}
	return bestField, bestValue, bestRank, true
}

// searchGraph 对齐 _search_graph
func (s *GraphAdminService) searchGraph(ctx context.Context, query string, limit int) map[string]any {
	token := strings.TrimSpace(query)
	safeLimit := limit
	if safeLimit < 1 {
		safeLimit = 1
	}
	if safeLimit > 200 {
		safeLimit = 200
	}
	if token == "" {
		return map[string]any{"success": false, "query": token, "limit": safeLimit, "count": 0, "items": []any{}, "error": "query 不能为空"}
	}
	if s.Meta() == nil {
		return map[string]any{"success": false, "query": token, "limit": safeLimit, "count": 0, "items": []any{}, "error": "metadata store 未初始化"}
	}

	like := "%" + strings.ToLower(token) + "%"
	entityRows, err := s.Meta().Query(
		`SELECT e.*, (SELECT COUNT(*) FROM paragraph_entities pe JOIN paragraphs p ON p.hash = pe.paragraph_hash
		 WHERE pe.entity_hash = e.hash AND COALESCE(p.is_deleted, 0) = 0) AS active_evidence_count
		 FROM entities e WHERE COALESCE(e.is_deleted, 0) = 0 AND (LOWER(e.name) LIKE ? OR LOWER(e.hash) LIKE ?)
		 ORDER BY e.appearance_count DESC, e.created_at ASC LIMIT 200`, like, like)
	if err != nil {
		return map[string]any{"success": false, "query": token, "limit": safeLimit, "count": 0, "items": []any{}, "error": err.Error()}
	}
	relationRows, err := s.Meta().Query(
		`SELECT * FROM relations WHERE (is_inactive IS NULL OR is_inactive = 0)
		 AND (LOWER(subject) LIKE ? OR LOWER(object) LIKE ? OR LOWER(predicate) LIKE ? OR LOWER(hash) LIKE ?)
		 ORDER BY confidence DESC, created_at DESC LIMIT 200`, like, like, like, like)
	if err != nil {
		return map[string]any{"success": false, "query": token, "limit": safeLimit, "count": 0, "items": []any{}, "error": err.Error()}
	}

	type ranked struct {
		item    map[string]any
		rank    int
		kind    int
		sortA   float64
		sortB   float64
		nameKey string
		predKey string
		objKey  string
		hashKey string
	}
	rankedItems := make([]ranked, 0, len(entityRows)+len(relationRows))
	seen := map[string]struct{}{}

	for _, row := range entityRows {
		fields := [][2]string{{"name", rowString(row, "name")}, {"hash", rowString(row, "hash")}}
		matchedField, matchedValue, rank, ok := pickGraphSearchMatch(fields, token)
		if !ok {
			continue
		}
		hashValue := rowString(row, "hash")
		dedupeKey := hashValue
		if dedupeKey == "" {
			dedupeKey = "name:" + strings.ToLower(rowString(row, "name"))
		}
		if _, exists := seen[dedupeKey]; exists {
			continue
		}
		seen[dedupeKey] = struct{}{}
		name := rowString(row, "name")
		title := name
		if title == "" {
			title = hashValue
		}
		item := map[string]any{
			"type":                  "entity",
			"title":                 title,
			"matched_field":         matchedField,
			"matched_value":         matchedValue,
			"entity_name":           name,
			"entity_hash":           hashValue,
			"appearance_count":      rowInt(row, "appearance_count"),
			"active_evidence_count": rowInt(row, "active_evidence_count"),
		}
		rankedItems = append(rankedItems, ranked{item: item, rank: rank, kind: 0,
			sortA: -float64(rowInt(row, "appearance_count")), sortB: 0,
			nameKey: strings.ToLower(name), predKey: "", objKey: "", hashKey: strings.ToLower(hashValue)})
	}

	for _, row := range relationRows {
		fields := [][2]string{
			{"subject", rowString(row, "subject")},
			{"object", rowString(row, "object")},
			{"predicate", rowString(row, "predicate")},
			{"hash", rowString(row, "hash")},
		}
		matchedField, matchedValue, rank, ok := pickGraphSearchMatch(fields, token)
		if !ok {
			continue
		}
		hashValue := rowString(row, "hash")
		subject := rowString(row, "subject")
		predicate := rowString(row, "predicate")
		object := rowString(row, "object")
		dedupeKey := hashValue
		if dedupeKey == "" {
			dedupeKey = strings.ToLower(subject + "|" + predicate + "|" + object)
		}
		if _, exists := seen[dedupeKey]; exists {
			continue
		}
		seen[dedupeKey] = struct{}{}
		item := map[string]any{
			"type":          "relation",
			"title":         formatRelationText(subject, predicate, object),
			"matched_field": matchedField,
			"matched_value": matchedValue,
			"subject":       subject,
			"predicate":     predicate,
			"object":        object,
			"relation_hash": hashValue,
			"confidence":    rowFloat(row, "confidence"),
			"created_at":    rowFloat(row, "created_at"),
		}
		rankedItems = append(rankedItems, ranked{item: item, rank: rank, kind: 1,
			sortA: -rowFloat(row, "confidence"), sortB: -rowFloat(row, "created_at"),
			nameKey: "", predKey: strings.ToLower(predicate), objKey: strings.ToLower(object), hashKey: strings.ToLower(hashValue)})
		// 关系项缺少实体名排序键，用 subject 兜底
		last := &rankedItems[len(rankedItems)-1]
		last.nameKey = strings.ToLower(subject)
	}

	sort.SliceStable(rankedItems, func(i int, j int) bool {
		left, right := rankedItems[i], rankedItems[j]
		if left.rank != right.rank {
			return left.rank < right.rank
		}
		if left.kind != right.kind {
			return left.kind < right.kind
		}
		if left.sortA != right.sortA {
			return left.sortA < right.sortA
		}
		if left.sortB != right.sortB {
			return left.sortB < right.sortB
		}
		if left.nameKey != right.nameKey {
			return left.nameKey < right.nameKey
		}
		if left.predKey != right.predKey {
			return left.predKey < right.predKey
		}
		if left.objKey != right.objKey {
			return left.objKey < right.objKey
		}
		return left.hashKey < right.hashKey
	})

	items := make([]any, 0, safeLimit)
	for index, entry := range rankedItems {
		if index >= safeLimit {
			break
		}
		items = append(items, entry.item)
	}
	return map[string]any{"success": true, "query": token, "limit": safeLimit, "count": len(items), "items": items}
}

// queryRelationRowsByHashes 对齐 _query_relation_rows_by_hashes
func (s *GraphAdminService) queryRelationRowsByHashes(hashes []string, includeInactive bool) []map[string]any {
	tokensList := dedupeStrings(hashes)
	if len(tokensList) == 0 || s.Meta() == nil {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(tokensList)), ",")
	statement := "SELECT hash, subject, predicate, object, confidence, created_at, source_paragraph FROM relations WHERE hash IN (" + placeholders + ")"
	if !includeInactive {
		statement += " AND (is_inactive IS NULL OR is_inactive = 0)"
	}
	params := make([]any, 0, len(tokensList))
	for _, token := range tokensList {
		params = append(params, token)
	}
	rows, err := s.Meta().Query(statement, params...)
	if err != nil {
		return nil
	}
	byHash := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		byHash[rowString(row, "hash")] = row
	}
	ordered := make([]map[string]any, 0, len(rows))
	for _, token := range tokensList {
		if row, ok := byHash[token]; ok {
			ordered = append(ordered, row)
		}
	}
	return ordered
}

// queryDistinctParagraphHashesForRelations 对齐 _query_distinct_paragraph_hashes_for_relations
func (s *GraphAdminService) queryDistinctParagraphHashesForRelations(hashes []string, limit int) []string {
	tokensList := dedupeStrings(hashes)
	if len(tokensList) == 0 || s.Meta() == nil {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(tokensList)), ",")
	statement := `SELECT DISTINCT p.hash AS hash, p.updated_at AS updated_at, p.created_at AS created_at
		FROM paragraphs p JOIN paragraph_relations pr ON pr.paragraph_hash = p.hash
		WHERE pr.relation_hash IN (` + placeholders + `) AND COALESCE(p.is_deleted, 0) = 0
		ORDER BY p.updated_at DESC, p.created_at DESC, p.hash ASC`
	params := make([]any, 0, len(tokensList)+1)
	for _, token := range tokensList {
		params = append(params, token)
	}
	if limit > 0 {
		statement += " LIMIT ?"
		params = append(params, limit)
	}
	rows, err := s.Meta().Query(statement, params...)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowString(row, "hash"))
	}
	return out
}

// loadParagraphRows 对齐 _load_paragraph_rows
func (s *GraphAdminService) loadParagraphRows(hashes []string) []map[string]any {
	if s.Meta() == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(hashes))
	for _, hashValue := range dedupeStrings(hashes) {
		row, err := s.Meta().GetParagraph(hashValue)
		if err != nil || row == nil {
			continue
		}
		if rowBool(row, "is_deleted") {
			continue
		}
		out = append(out, row)
	}
	return out
}

// resolveGraphNodeName 对齐 _resolve_graph_node_name
func (s *GraphAdminService) resolveGraphNodeName(nodeID string) string {
	token := strings.TrimSpace(nodeID)
	if token == "" {
		return ""
	}
	if graphStore := s.Graph(); graphStore != nil {
		for _, name := range graphStore.GetNodes() {
			if strings.EqualFold(name, token) {
				return name
			}
		}
	}
	if s.Meta() != nil {
		rows, err := s.Meta().Query(
			`SELECT name FROM entities WHERE COALESCE(is_deleted, 0) = 0
			 AND (LOWER(TRIM(name)) = LOWER(TRIM(?)) OR hash = ?)
			 ORDER BY appearance_count DESC, created_at ASC LIMIT 1`, token, token)
		if err == nil && len(rows) > 0 {
			return rowString(rows[0], "name")
		}
		rows, err = s.Meta().Query(
			`SELECT subject AS name FROM relations WHERE (is_inactive IS NULL OR is_inactive = 0)
			 AND (LOWER(TRIM(subject)) = LOWER(TRIM(?)) OR LOWER(TRIM(object)) = LOWER(TRIM(?))) LIMIT 1`, token, token)
		if err == nil && len(rows) > 0 {
			return rowString(rows[0], "name")
		}
	}
	return token
}

// getRelatedRelationRowsForEntity 对齐 _get_related_relation_rows_for_entity
func (s *GraphAdminService) getRelatedRelationRowsForEntity(name string, limit int) []map[string]any {
	if s.Meta() == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	statement := `SELECT * FROM relations WHERE (is_inactive IS NULL OR is_inactive = 0)
		AND (LOWER(TRIM(subject)) = LOWER(TRIM(?)) OR LOWER(TRIM(object)) = LOWER(TRIM(?)))
		ORDER BY confidence DESC, created_at DESC`
	params := []any{name, name}
	if limit > 0 {
		statement += " LIMIT ?"
		params = append(params, limit)
	}
	rows, err := s.Meta().Query(statement, params...)
	if err != nil {
		return nil
	}
	return rows
}

// buildRelationSummary 对齐 _build_relation_summary
func (s *GraphAdminService) buildRelationSummary(row map[string]any, paragraphHashes []string) map[string]any {
	hashValue := rowString(row, "hash")
	if paragraphHashes == nil {
		paragraphHashes = s.paragraphHashesForRelation(hashValue)
	}
	subject := rowString(row, "subject")
	predicate := rowString(row, "predicate")
	object := rowString(row, "object")
	return map[string]any{
		"hash":             hashValue,
		"subject":          subject,
		"predicate":        predicate,
		"object":           object,
		"text":             formatRelationText(subject, predicate, object),
		"confidence":       rowFloat(row, "confidence"),
		"paragraph_count":  len(paragraphHashes),
		"paragraph_hashes": paragraphHashes,
		"source_paragraph": rowString(row, "source_paragraph"),
	}
}

// paragraphHashesForRelation 查询关系关联的段落哈希
func (s *GraphAdminService) paragraphHashesForRelation(hashValue string) []string {
	if s.Meta() == nil || strings.TrimSpace(hashValue) == "" {
		return []string{}
	}
	rows, err := s.Meta().Query(
		"SELECT paragraph_hash FROM paragraph_relations WHERE relation_hash = ? ORDER BY paragraph_hash ASC", hashValue)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowString(row, "paragraph_hash"))
	}
	return out
}

// buildParagraphSummary 对齐 _build_paragraph_summary
func (s *GraphAdminService) buildParagraphSummary(row map[string]any) map[string]any {
	hashValue := rowString(row, "hash")
	entities := []string{}
	relations := []string{}
	if s.Meta() != nil {
		if entityRows, err := s.Meta().GetParagraphEntities(hashValue); err == nil {
			for _, entityRow := range entityRows {
				entities = append(entities, rowString(entityRow, "entity_name"))
			}
		}
		if relationRows, err := s.Meta().GetParagraphRelations(hashValue); err == nil {
			for _, relationRow := range relationRows {
				relations = append(relations, formatRelationText(
					rowString(relationRow, "subject"), rowString(relationRow, "predicate"), rowString(relationRow, "object")))
			}
		}
	}
	entities = dedupeStrings(entities)
	relations = dedupeStrings(relations)
	staleMarks := []any{}
	isStale := false
	if s.Meta() != nil {
		if marks, err := s.Meta().GetParagraphStaleRelationMarksBatch([]string{hashValue}, nil); err == nil {
			for _, mark := range marks[hashValue] {
				isStale = true
				staleMarks = append(staleMarks, map[string]any{
					"mark":              mark,
					"relation_inactive": rowBool(mark, "relation_inactive"),
				})
			}
		}
	}
	return map[string]any{
		"hash":                 hashValue,
		"content":              rowString(row, "content"),
		"preview":              trimText(rowString(row, "content"), 220),
		"source":               rowString(row, "source"),
		"created_at":           rowFloat(row, "created_at"),
		"updated_at":           rowFloat(row, "updated_at"),
		"entity_count":         len(entities),
		"relation_count":       len(relations),
		"entities":             entities,
		"relations":            relations,
		"is_stale":             isStale,
		"stale_relation_marks": staleMarks,
	}
}

// evidenceNodeID 生成证据图节点 ID
func evidenceEntityNodeID(name string) string         { return "entity:" + name }
func evidenceRelationNodeID(hashValue string) string  { return "relation:" + hashValue }
func evidenceParagraphNodeID(hashValue string) string { return "paragraph:" + hashValue }

// buildEvidenceGraph 对齐 _build_evidence_graph
func (s *GraphAdminService) buildEvidenceGraph(focusEntities []string, relationRows []map[string]any, paragraphRows []map[string]any, nodeLimit int) map[string]any {
	nodes := map[string]map[string]any{}
	edges := make([]any, 0)
	edgeKeys := map[string]struct{}{}
	addEdge := func(source string, target string, kind string, label string, weight float64, extra map[string]any) {
		key := source + "->" + target + "#" + kind
		if _, ok := edgeKeys[key]; ok {
			return
		}
		edgeKeys[key] = struct{}{}
		edge := map[string]any{"source": source, "target": target, "kind": kind, "label": label, "weight": weight}
		for name, value := range extra {
			edge[name] = value
		}
		edges = append(edges, edge)
	}
	addEntityNode := func(name string) {
		if strings.TrimSpace(name) == "" {
			return
		}
		id := evidenceEntityNodeID(name)
		if _, ok := nodes[id]; ok {
			return
		}
		nodes[id] = map[string]any{"id": id, "type": "entity", "label": name, "name": name}
	}
	focus := dedupeStrings(focusEntities)
	for _, name := range focus {
		addEntityNode(name)
	}
	relationHashSet := map[string]struct{}{}
	for _, row := range relationRows {
		hashValue := rowString(row, "hash")
		if hashValue != "" {
			relationHashSet[hashValue] = struct{}{}
		}
		id := evidenceRelationNodeID(hashValue)
		subject := rowString(row, "subject")
		predicate := rowString(row, "predicate")
		object := rowString(row, "object")
		paragraphHashes := s.paragraphHashesForRelation(hashValue)
		nodes[id] = map[string]any{
			"id":    id,
			"type":  "relation",
			"label": formatRelationText(subject, predicate, object),
			"metadata": map[string]any{
				"hash":             hashValue,
				"subject":          subject,
				"predicate":        predicate,
				"object":           object,
				"confidence":       rowFloat(row, "confidence"),
				"paragraph_count":  len(paragraphHashes),
				"paragraph_hashes": paragraphHashes,
				"text":             formatRelationText(subject, predicate, object),
			},
		}
		addEntityNode(subject)
		addEntityNode(object)
		addEdge(evidenceEntityNodeID(subject), id, "subject", "主语", 1, nil)
		addEdge(evidenceEntityNodeID(object), id, "object", "宾语", 1, nil)
	}
	for _, row := range paragraphRows {
		hashValue := rowString(row, "hash")
		id := evidenceParagraphNodeID(hashValue)
		entityRows := []map[string]any{}
		if s.Meta() != nil {
			if loaded, err := s.Meta().GetParagraphEntities(hashValue); err == nil {
				entityRows = loaded
			}
		}
		nodes[id] = map[string]any{
			"id":    id,
			"type":  "paragraph",
			"label": trimText(rowString(row, "content"), 60),
			"metadata": map[string]any{
				"hash":           hashValue,
				"source":         rowString(row, "source"),
				"updated_at":     rowFloat(row, "updated_at"),
				"entity_count":   len(entityRows),
				"relation_count": 0,
				"preview":        trimText(rowString(row, "content"), 220),
			},
		}
		for _, entityRow := range entityRows {
			name := rowString(entityRow, "entity_name")
			addEntityNode(name)
			mentionCount := rowInt(entityRow, "mention_count")
			if mentionCount < 1 {
				mentionCount = 1
			}
			label := "提及"
			if mentionCount > 1 {
				label = fmt.Sprintf("提及 ×%d", mentionCount)
			}
			addEdge(id, evidenceEntityNodeID(name), "mentions", label, float64(mentionCount), nil)
		}
		if s.Meta() != nil {
			if relationRowsForParagraph, err := s.Meta().GetParagraphRelations(hashValue); err == nil {
				for _, relationRow := range relationRowsForParagraph {
					relationHash := rowString(relationRow, "relation_hash")
					if _, ok := relationHashSet[relationHash]; !ok {
						continue
					}
					addEdge(id, evidenceRelationNodeID(relationHash), "supports", "支撑", 1, nil)
				}
			}
		}
	}

	nodeList := make([]any, 0, len(nodes))
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i int, j int) bool {
		left, right := nodes[ids[i]], nodes[ids[j]]
		leftType, rightType := evidenceTypePriority(asString(left["type"])), evidenceTypePriority(asString(right["type"]))
		if leftType != rightType {
			return leftType < rightType
		}
		return ids[i] < ids[j]
	})
	kept := map[string]struct{}{}
	for index, id := range ids {
		if nodeLimit > 0 && index >= nodeLimit {
			break
		}
		kept[id] = struct{}{}
		nodeList = append(nodeList, nodes[id])
	}
	filteredEdges := make([]any, 0, len(edges))
	for _, edgeValue := range edges {
		edge := edgeValue.(map[string]any)
		if _, ok := kept[asString(edge["source"])]; !ok {
			continue
		}
		if _, ok := kept[asString(edge["target"])]; !ok {
			continue
		}
		filteredEdges = append(filteredEdges, edge)
	}
	return map[string]any{"nodes": nodeList, "edges": filteredEdges, "focus_entities": focus}
}

// evidenceTypePriority 证据图节点类型优先级
func evidenceTypePriority(typeName string) int {
	switch typeName {
	case "entity":
		return 0
	case "relation":
		return 1
	case "paragraph":
		return 2
	default:
		return 3
	}
}

// dedupeStrings 对齐 _dedupe_strings：按序去重去空
func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		token := strings.TrimSpace(value)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

// buildGraphEdgeLabel 对齐 _build_graph_edge_label
func buildGraphEdgeLabel(predicates []string) string {
	tokensList := dedupeStrings(predicates)
	if len(tokensList) == 0 {
		return ""
	}
	if len(tokensList) == 1 {
		return tokensList[0]
	}
	return fmt.Sprintf("%s +%d", tokensList[0], len(tokensList)-1)
}

// formatRelationText 对齐 _format_relation_text
func formatRelationText(subject string, predicate string, object string) string {
	return strings.TrimSpace(strings.Join([]string{subject, predicate, object}, " "))
}

// rowString 读取行内字符串列
func rowString(row map[string]any, key string) string {
	return asString(row[key])
}

// rowFloat 读取行内浮点列
func rowFloat(row map[string]any, key string) float64 {
	value, ok := asFloat(row[key])
	if !ok {
		return 0
	}
	return value
}

// rowInt 读取行内整数列
func rowInt(row map[string]any, key string) int {
	value, ok := asFloat(row[key])
	if !ok {
		return 0
	}
	return int(value)
}

// rowBool 读取行内布尔列，兼容 SQLite 的 0/1
func rowBool(row map[string]any, key string) bool {
	switch typed := row[key].(type) {
	case nil:
		return false
	case bool:
		return typed
	case int64:
		return typed != 0
	case int:
		return typed != 0
	case float64:
		return typed != 0
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	default:
		return false
	}
}

var _ = context.Background
