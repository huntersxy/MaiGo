package storage

// A_memorix schema 数据规范化，对齐 Python metadata_schema.py 尾部与 metadata_store.py
// 包括 32 位关系别名重建、段落 knowledge_type 归一与人物事实回填入口

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// relationAliasResult 对齐 Python rebuild_relation_hash_aliases 的返回值
type relationAliasResult struct {
	Inserted      int
	ConflictCount int
	Conflicts     []string
}

// RebuildRelationHashAliases 重建 32 位 relation hash 别名映射
// conn 为空时使用 Store 当前连接，对齐 Python conn 可选参数
func (s *Store) RebuildRelationHashAliases(conn *Conn) (relationAliasResult, error) {
	connection, err := s.ResolveConn(conn)
	if err != nil {
		return relationAliasResult{}, err
	}
	// 历史库兜底：缺表时先创建，避免迁移过程直接中断
	if _, err := connection.Exec(ddlRelationHashAliases); err != nil {
		return relationAliasResult{}, err
	}
	if _, err := connection.Exec("DELETE FROM relation_hash_aliases"); err != nil {
		return relationAliasResult{}, err
	}

	hashes, err := queryHashes(connection, "SELECT hash FROM relations")
	if err != nil {
		return relationAliasResult{}, err
	}
	hasDeleted, err := s.tableExists(connection, "deleted_relations")
	if err != nil {
		return relationAliasResult{}, err
	}
	if hasDeleted {
		deletedHashes, err := queryHashes(connection, "SELECT hash FROM deleted_relations")
		if err != nil {
			return relationAliasResult{}, err
		}
		hashes = append(hashes, deletedHashes...)
	}

	aliasMap := make(map[string]string)
	conflicts := make(map[string]struct{})
	for _, hash := range hashes {
		if len(hash) != 64 {
			continue
		}
		alias := hash[:32]
		old, ok := aliasMap[alias]
		if !ok {
			aliasMap[alias] = hash
			continue
		}
		if old != hash {
			conflicts[alias] = struct{}{}
		}
	}

	aliases := make([]string, 0, len(aliasMap))
	for alias := range aliasMap {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	inserted := 0
	for _, alias := range aliases {
		if _, conflicted := conflicts[alias]; conflicted {
			continue
		}
		if _, err := connection.Exec(
			"INSERT INTO relation_hash_aliases(alias32, hash) VALUES (?, ?)",
			alias,
			aliasMap[alias],
		); err != nil {
			return relationAliasResult{}, err
		}
		inserted++
	}
	if conn == nil {
		if err := connection.Commit(); err != nil {
			return relationAliasResult{}, err
		}
	}

	conflictList := make([]string, 0, len(conflicts))
	for alias := range conflicts {
		conflictList = append(conflictList, alias)
	}
	sort.Strings(conflictList)
	return relationAliasResult{
		Inserted:      inserted,
		ConflictCount: len(conflicts),
		Conflicts:     conflictList,
	}, nil
}

// queryHashes 读取单列 hash 列表
func queryHashes(conn *Conn, query string) ([]string, error) {
	rows, err := conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		out = append(out, hash)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// knowledgeTypeNormalizeResult 对齐 Python normalize_paragraph_knowledge_types 的返回值
type knowledgeTypeNormalizeResult struct {
	Normalized    int
	InvalidBefore []string
	NormalizedTo  map[string]int
}

// ListInvalidParagraphKnowledgeTypes 列出当前库中不合法的段落 knowledge_type
func (s *Store) ListInvalidParagraphKnowledgeTypes(conn *Conn) ([]string, error) {
	connection, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	allowed := AllowedKnowledgeTypeValues()
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(allowed)), ", ")
	args := make([]any, 0, len(allowed))
	for _, item := range allowed {
		args = append(args, item)
	}
	rows, err := connection.Query(`
		SELECT DISTINCT knowledge_type
		FROM paragraphs
		WHERE knowledge_type IS NULL
		   OR TRIM(COALESCE(knowledge_type, '')) = ''
		   OR LOWER(TRIM(knowledge_type)) NOT IN (`+placeholders+`)
		ORDER BY knowledge_type
	`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	invalid := make([]string, 0)
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if raw.Valid {
			invalid = append(invalid, raw.String)
		} else {
			invalid = append(invalid, "")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return invalid, nil
}

// NormalizeParagraphKnowledgeTypes 将历史非法 knowledge_type 归一化为合法值
func (s *Store) NormalizeParagraphKnowledgeTypes(conn *Conn) (knowledgeTypeNormalizeResult, error) {
	connection, err := s.ResolveConn(conn)
	if err != nil {
		return knowledgeTypeNormalizeResult{}, err
	}
	rows, err := connection.Query("SELECT hash, content, knowledge_type FROM paragraphs")
	if err != nil {
		return knowledgeTypeNormalizeResult{}, err
	}
	type paragraphRow struct {
		hash          string
		content       string
		knowledgeType sql.NullString
	}
	var scanned []paragraphRow
	for rows.Next() {
		var row paragraphRow
		if err := rows.Scan(&row.hash, &row.content, &row.knowledgeType); err != nil {
			_ = rows.Close()
			return knowledgeTypeNormalizeResult{}, err
		}
		scanned = append(scanned, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return knowledgeTypeNormalizeResult{}, err
	}
	_ = rows.Close()

	normalizedCount := 0
	normalizedMap := make(map[string]int)
	invalidBefore := make([]string, 0)
	invalidSeen := make(map[string]struct{})
	fallback := KnowledgeMixed

	for _, row := range scanned {
		var rawValue any
		if row.knowledgeType.Valid {
			rawValue = row.knowledgeType.String
		}
		if _, err := ValidateStoredKnowledgeType(rawValue); err == nil {
			continue
		}
		rawText := ""
		if row.knowledgeType.Valid {
			rawText = row.knowledgeType.String
		}
		if _, seen := invalidSeen[rawText]; !seen {
			invalidSeen[rawText] = struct{}{}
			invalidBefore = append(invalidBefore, rawText)
		}

		normalizedType, err := ResolveStoredKnowledgeType(rawValue, row.content, true, &fallback)
		if err != nil {
			return knowledgeTypeNormalizeResult{}, err
		}
		if _, err := connection.Exec(
			"UPDATE paragraphs SET knowledge_type = ? WHERE hash = ?",
			string(normalizedType),
			row.hash,
		); err != nil {
			return knowledgeTypeNormalizeResult{}, err
		}
		normalizedCount++
		normalizedMap[string(normalizedType)]++
	}

	if err := connection.Commit(); err != nil {
		return knowledgeTypeNormalizeResult{}, err
	}
	sort.Strings(invalidBefore)
	return knowledgeTypeNormalizeResult{
		Normalized:    normalizedCount,
		InvalidBefore: invalidBefore,
		NormalizedTo:  normalizedMap,
	}, nil
}

// BackfillPersonFactClaims 把存量原子人物事实段落确定性迁移到事实账本
// 对齐 Python backfill_person_fact_claims：不调用模型，每条旧段落获得独立 statement 槽位
func (s *Store) BackfillPersonFactClaims(conn *Conn) (map[string]int, error) {
	connection, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := connection.Query(`
		SELECT hash, content, source, metadata, event_time, created_at, is_deleted
		FROM paragraphs
		WHERE source LIKE 'person_fact:%'
		  AND (is_deleted IS NULL OR is_deleted = 0)
		ORDER BY created_at ASC, hash ASC`)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	migrated := 0
	retracted := 0
	for _, row := range items {
		paragraphHash := strings.TrimSpace(stringFromRow(row, "hash"))
		content := strings.TrimSpace(stringFromRow(row, "content"))
		source := strings.TrimSpace(stringFromRow(row, "source"))
		if paragraphHash == "" || content == "" || !strings.HasPrefix(source, "person_fact:") {
			continue
		}
		metadata := decodeMetadata(stringFromRow(row, "metadata"))
		personID := strings.TrimSpace(stringFromAny(metadata["person_id"]))
		if personID == "" {
			personID = strings.TrimSpace(strings.TrimPrefix(source, "person_fact:"))
		}
		if personID == "" {
			continue
		}
		evidenceSource := strings.TrimSpace(stringFromAny(metadata["evidence_source"]))
		claimSpec, _ := metadata["fact_claim"].(map[string]any)
		userSupported := evidenceSource == "user_supported"
		factKey := strings.TrimSpace(stringFromAny(claimSpec["fact_key"]))
		if factKey == "" {
			factKey = "statement:" + paragraphHash
		}
		stability := strings.TrimSpace(stringFromAny(claimSpec["stability"]))
		if stability == "" {
			stability = "uncertain"
			if userSupported {
				stability = "stable"
			}
		}
		profileSection := strings.TrimSpace(stringFromAny(claimSpec["profile_section"]))
		if profileSection == "" {
			profileSection = "uncertain_notes"
			if userSupported {
				profileSection = "stable_facts"
			}
		}
		authority := strings.TrimSpace(stringFromAny(claimSpec["authority"]))
		if authority == "" {
			authority = "summary_derived"
			if userSupported {
				authority = "direct_user"
			}
		}
		confidence := 0.5
		if userSupported {
			confidence = 1.0
		}
		if parsed := OptionalFloat(claimSpec["confidence"]); parsed != nil {
			confidence = *parsed
		}
		var observedAt *float64
		if value := OptionalFloat(row["event_time"]); value != nil {
			observedAt = value
		} else if value := OptionalFloat(row["created_at"]); value != nil {
			observedAt = value
		}
		result, err := s.UpsertFactClaim(FactClaimUpsert{
			ScopeType:      "person",
			ScopeID:        personID,
			FactKey:        factKey,
			ValueText:      content,
			Polarity:       defaultFactClaimText(claimSpec, "polarity", "positive"),
			Cardinality:    defaultFactClaimText(claimSpec, "cardinality", "set"),
			Stability:      stability,
			ProfileSection: profileSection,
			Authority:      authority,
			Confidence:     &confidence,
			EvidenceType:   "paragraph",
			EvidenceID:     paragraphHash,
			EvidenceMetadata: map[string]any{
				"source_type":     "person_fact",
				"migration":       true,
				"evidence_source": evidenceSource,
				"trust":           factNormalizedToken(claimSpec["trust"]),
			},
			Reason:     "person_fact_schema_backfill",
			ObservedAt: observedAt,
		})
		if err != nil {
			return nil, err
		}
		claimID := strings.TrimSpace(stringFromAny(result["claim_id"]))
		if claimID == "" {
			continue
		}
		if _, err := s.UpdateParagraphMetadata(paragraphHash, map[string]any{"fact_claim_ids": []any{claimID}}, true); err != nil {
			return nil, err
		}
		migrated++
		if memoryChange, ok := metadata["memory_change"].(map[string]any); ok {
			if validTo := OptionalFloat(memoryChange["valid_to"]); validTo != nil {
				if _, err := s.RetractFactClaim(claimID, FactClaimAudit{
					Reason:       "person_fact_schema_backfill_superseded",
					EvidenceType: "paragraph",
					EvidenceID:   paragraphHash,
					At:           validTo,
				}); err != nil {
					return nil, err
				}
				retracted++
			}
		}
	}
	return map[string]int{"scanned": len(items), "migrated": migrated, "retracted": retracted}, nil
}

// defaultFactClaimText 读取 claim spec 的字符串字段，对齐 Python str(spec.get(key) or fallback)
func defaultFactClaimText(spec map[string]any, key string, fallback string) string {
	if value := strings.TrimSpace(stringFromAny(spec[key])); value != "" {
		return value
	}
	return fallback
}

// stringFromAny 把任意元数据值转成字符串，对齐 Python str(value)
func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
