package storage

// A_memorix 实体/关系写入与软删除复活，对齐 Python core/storage/metadata_store.py

import (
	"database/sql"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"maibot-go/internal/A_memorix/core/utils"
)

// MergeExistingParagraphMetadata 合并已存在段落的 metadata，对齐 Python _merge_existing_paragraph_metadata
func (s *Store) MergeExistingParagraphMetadata(paragraphHash string, metadataPatch map[string]any, conn *Conn) (map[string]any, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	var rawMetadata sql.NullString
	err = c.QueryRow(`SELECT metadata
		FROM paragraphs
		WHERE hash = ?
		LIMIT 1`, paragraphHash).Scan(&rawMetadata)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	metadata := decodeMetadata(rawMetadata.String)
	updated := mergeParagraphMetadata(metadata, metadataPatch)
	if metadataEqual(updated, metadata) {
		return metadata, nil
	}
	encoded := encodeMetadata(updated)
	err = s.Transaction(true, func(tx *Tx) error {
		if _, err := tx.Exec("UPDATE paragraphs SET metadata = ?, updated_at = ? WHERE hash = ?",
			encoded, nowTimestamp(), paragraphHash); err != nil {
			return err
		}
		sources := s.GetSourcesForParagraphHashes([]string{paragraphHash}, true, tx.Conn())
		s.EnqueueEpisodeSourceRebuilds(sources, "paragraph_metadata_merged", EpisodeEnqueueOptions{Conn: tx.Conn()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// metadataEqual 比较两份 metadata 是否等价（JSON 序列化后比较）
func metadataEqual(left map[string]any, right map[string]any) bool {
	return jsonDumps(left) == jsonDumps(right)
}

// ReviveIfDeleted 复活被软删除的实体与段落，对齐 Python revive_if_deleted
func (s *Store) ReviveIfDeleted(entityHashes []string, paragraphHashes []string) (int, error) {
	count := 0
	err := s.Transaction(true, func(tx *Tx) error {
		if len(entityHashes) > 0 {
			for _, batch := range iterSQLBatches(entityHashes, 900) {
				query := fmt.Sprintf(`UPDATE entities
					SET is_deleted = 0, deleted_at = NULL
					WHERE is_deleted = 1 AND hash IN (%s)`, sqlPlaceholders(len(batch)))
				result, err := tx.Exec(query, stringArgs(batch)...)
				if err != nil {
					return err
				}
				affected, err := result.RowsAffected()
				if err != nil {
					return err
				}
				count += int(affected)
			}
		}
		if len(paragraphHashes) > 0 {
			touchedSources := s.GetSourcesForParagraphHashes(paragraphHashes, true, tx.Conn())
			for _, batch := range iterSQLBatches(paragraphHashes, 900) {
				query := fmt.Sprintf(`SELECT hash, content
					FROM paragraphs
					WHERE is_deleted = 1 AND hash IN (%s)`, sqlPlaceholders(len(batch)))
				rows, err := tx.Query(query, stringArgs(batch)...)
				if err != nil {
					return err
				}
				reviveRows, err := scanRowsToMaps(rows)
				if err != nil {
					return err
				}
				update := fmt.Sprintf(`UPDATE paragraphs
					SET is_deleted = 0, deleted_at = NULL,
						expires_at = NULL, deletion_reason = NULL
					WHERE is_deleted = 1 AND hash IN (%s)`, sqlPlaceholders(len(batch)))
				result, err := tx.Exec(update, stringArgs(batch)...)
				if err != nil {
					return err
				}
				affected, err := result.RowsAffected()
				if err != nil {
					return err
				}
				count += int(affected)
				if affected > 0 {
					for _, row := range reviveRows {
						hash := stringFromRow(row, "hash")
						s.UpsertParagraphNgramIfReady(hash, stringFromRow(row, "content"), 1, tx.Conn())
						s.FtsUpsertTokenizedParagraph(hash, tx.Conn())
					}
				}
			}
			if len(touchedSources) > 0 {
				s.EnqueueEpisodeSourceRebuilds(touchedSources, "paragraph_revived", EpisodeEnqueueOptions{Conn: tx.Conn()})
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if count > 0 {
		zap.L().Info("自动复活", zap.Int("count", count))
	}
	return count, nil
}

// ReviveEntitiesByNames 按名称复活实体，对齐 Python revive_entities_by_names
func (s *Store) ReviveEntitiesByNames(names []string) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}
	hashes := make([]string, 0, len(names))
	for _, name := range names {
		hashValue, err := utils.ComputeHash(canonicalizeName(name))
		if err != nil {
			return 0, err
		}
		hashes = append(hashes, hashValue)
	}
	return s.ReviveIfDeleted(hashes, nil)
}

// AddEntity 添加实体，对齐 Python add_entity
func (s *Store) AddEntity(name string, vectorIndex *int, sourceParagraph string, metadata map[string]any) (string, error) {
	nameNormalized := canonicalizeName(name)
	if nameNormalized == "" {
		return "", fmt.Errorf("Entity name cannot be empty")
	}
	hashValue, err := utils.ComputeHash(nameNormalized)
	if err != nil {
		return "", err
	}
	now := nowTimestamp()
	existed := false
	err = s.Transaction(true, func(tx *Tx) error {
		var marker int
		queryErr := tx.QueryRow("SELECT 1 FROM entities WHERE hash = ?", hashValue).Scan(&marker)
		if queryErr == nil {
			existed = true
		} else if queryErr != sql.ErrNoRows {
			return queryErr
		}
		if _, err := tx.Exec(`INSERT INTO entities
			(hash, name, vector_index, appearance_count, created_at, metadata, is_deleted, deleted_at)
			VALUES (?, ?, ?, 1, ?, ?, 0, NULL)
			ON CONFLICT(hash) DO UPDATE SET
				appearance_count = entities.appearance_count + 1,
				is_deleted = 0,
				deleted_at = NULL`,
			hashValue, name, optionalInt(vectorIndex), now, encodeMetadata(metadata),
		); err != nil {
			return err
		}
		if strings.TrimSpace(sourceParagraph) != "" {
			if _, err := s.LinkParagraphEntity(sourceParagraph, hashValue, 1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if existed {
		zap.L().Debug("实体已存在(复活/计数+1)", zap.String("name", name))
	} else {
		zap.L().Debug("添加实体", zap.String("name", name), zap.String("hash", truncateRunes(hashValue, 8)))
	}
	return hashValue, nil
}

// AddEntitiesBatch 在单个受管事务中批量添加实体，对齐 Python add_entities_batch
func (s *Store) AddEntitiesBatch(names []string, vectorIndex *int, sourceParagraph string, metadata map[string]any) ([]string, error) {
	var hashes []string
	err := s.Transaction(true, func(tx *Tx) error {
		inner, err := s.addEntitiesBatch(tx, names, vectorIndex, sourceParagraph, metadata)
		hashes = inner
		return err
	})
	if err != nil {
		return nil, err
	}
	return hashes, nil
}

// addEntitiesBatch 批量写入实体并维护出现次数与段落提及次数
func (s *Store) addEntitiesBatch(tx *Tx, names []string, vectorIndex *int, sourceParagraph string, metadata map[string]any) ([]string, error) {
	if len(names) == 0 {
		return []string{}, nil
	}
	c := tx.Conn()
	orderedHashes := make([]string, 0, len(names))
	type groupedEntity struct {
		name  string
		count int
	}
	grouped := make(map[string]groupedEntity, len(names))
	order := make([]string, 0, len(names))
	for _, name := range names {
		nameNormalized := canonicalizeName(name)
		if nameNormalized == "" {
			return nil, fmt.Errorf("Entity name cannot be empty")
		}
		hashValue, err := utils.ComputeHash(nameNormalized)
		if err != nil {
			return nil, err
		}
		orderedHashes = append(orderedHashes, hashValue)
		if existing, ok := grouped[hashValue]; ok {
			existing.count++
			grouped[hashValue] = existing
			continue
		}
		grouped[hashValue] = groupedEntity{name: name, count: 1}
		order = append(order, hashValue)
	}

	now := nowTimestamp()
	encodedMetadata := encodeMetadata(metadata)
	insert := `INSERT INTO entities
		(hash, name, vector_index, appearance_count, created_at, metadata, is_deleted, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, NULL)
		ON CONFLICT(hash) DO UPDATE SET
			appearance_count = entities.appearance_count + excluded.appearance_count,
			is_deleted = 0,
			deleted_at = NULL`
	for _, hashValue := range order {
		entry := grouped[hashValue]
		if _, err := c.Exec(insert, hashValue, entry.name, optionalInt(vectorIndex), entry.count, now, encodedMetadata); err != nil {
			return nil, err
		}
	}

	if strings.TrimSpace(sourceParagraph) != "" {
		link := `INSERT INTO paragraph_entities (paragraph_hash, entity_hash, mention_count)
			VALUES (?, ?, ?)
			ON CONFLICT(paragraph_hash, entity_hash) DO UPDATE SET
				mention_count = paragraph_entities.mention_count + excluded.mention_count`
		for _, hashValue := range order {
			if _, err := c.Exec(link, sourceParagraph, hashValue, grouped[hashValue].count); err != nil {
				return nil, err
			}
		}
		sources := s.GetSourcesForParagraphHashes([]string{sourceParagraph}, true, c)
		s.EnqueueEpisodeSourceRebuilds(sources, "paragraph_entity_linked", EpisodeEnqueueOptions{Conn: c})
	} else if err := c.Commit(); err != nil {
		return nil, err
	}
	zap.L().Debug("批量添加实体", zap.Int("items", len(names)), zap.Int("unique", len(grouped)))
	return orderedHashes, nil
}

// ComputeRelationHash 计算关系稳定 hash，对齐 Python compute_relation_hash
func (s *Store) ComputeRelationHash(subject string, predicate string, object string) (string, error) {
	subjectCanon := canonicalizeName(subject)
	predicateCanon := canonicalizeName(predicate)
	objectCanon := canonicalizeName(object)
	if subjectCanon == "" || predicateCanon == "" || objectCanon == "" {
		return "", fmt.Errorf("Relation components cannot be empty")
	}
	return utils.ComputeHash(fmt.Sprintf("%s|%s|%s", subjectCanon, predicateCanon, objectCanon))
}

// AddRelation 添加关系，对齐 Python add_relation
func (s *Store) AddRelation(subject string, predicate string, object string, vectorIndex *int, confidence float64, sourceParagraph string, metadata map[string]any) (string, error) {
	hashValue, err := s.ComputeRelationHash(subject, predicate, object)
	if err != nil {
		return "", err
	}
	now := nowTimestamp()
	err = s.Transaction(true, func(tx *Tx) error {
		result, err := tx.Exec(`INSERT OR IGNORE INTO relations
			(hash, subject, predicate, object, vector_index, confidence, created_at, source_paragraph, metadata,
			 retention_strength, retention_anchor_at, next_lifecycle_at, reinforcement_count, inactive_reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1.0, ?, ?, 0, NULL)`,
			hashValue, subject, predicate, object, optionalInt(vectorIndex), confidence, now,
			optionalString(sourceParagraph), encodeMetadata(metadata), now, now,
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			zap.L().Debug("添加关系", zap.String("subject", subject), zap.String("predicate", predicate), zap.String("object", object))
		} else {
			zap.L().Debug("关系已存在", zap.String("subject", subject), zap.String("predicate", predicate), zap.String("object", object))
		}
		if strings.TrimSpace(sourceParagraph) != "" {
			if _, err := s.LinkParagraphRelation(sourceParagraph, hashValue); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hashValue, nil
}

// AddRelationsBatch 在单个受管事务中批量添加关系，对齐 Python add_relations_batch
func (s *Store) AddRelationsBatch(relations [][3]string, vectorIndex *int, confidence float64, sourceParagraph string, metadata map[string]any) ([]string, error) {
	var hashes []string
	err := s.Transaction(true, func(tx *Tx) error {
		inner, err := s.addRelationsBatch(tx, relations, vectorIndex, confidence, sourceParagraph, metadata)
		hashes = inner
		return err
	})
	if err != nil {
		return nil, err
	}
	return hashes, nil
}

// addRelationsBatch 批量写入关系与段落关联，保留首见关系元数据
func (s *Store) addRelationsBatch(tx *Tx, relations [][3]string, vectorIndex *int, confidence float64, sourceParagraph string, metadata map[string]any) ([]string, error) {
	if len(relations) == 0 {
		return []string{}, nil
	}
	c := tx.Conn()
	hashes := make([]string, 0, len(relations))
	for _, relation := range relations {
		hashValue, err := s.ComputeRelationHash(relation[0], relation[1], relation[2])
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, hashValue)
	}
	now := nowTimestamp()
	encodedMetadata := encodeMetadata(metadata)
	insert := `INSERT OR IGNORE INTO relations
		(hash, subject, predicate, object, vector_index, confidence, created_at, source_paragraph, metadata,
		 retention_strength, retention_anchor_at, next_lifecycle_at, reinforcement_count, inactive_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1.0, ?, ?, 0, NULL)`
	for i, relation := range relations {
		if _, err := c.Exec(insert, hashes[i], relation[0], relation[1], relation[2],
			optionalInt(vectorIndex), confidence, now, optionalString(sourceParagraph),
			encodedMetadata, now, now); err != nil {
			return nil, err
		}
	}

	if strings.TrimSpace(sourceParagraph) != "" {
		uniqueHashes := dedupeStrings(hashes)
		query := fmt.Sprintf(`SELECT relation_hash
			FROM paragraph_relations
			WHERE paragraph_hash = ?
			  AND relation_hash IN (%s)`, sqlPlaceholders(len(uniqueHashes)))
		args := append([]any{sourceParagraph}, stringArgs(uniqueHashes)...)
		rows, err := c.Query(query, args...)
		if err != nil {
			return nil, err
		}
		existingRows, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		existing := make(map[string]struct{}, len(existingRows))
		for _, row := range existingRows {
			existing[stringFromRow(row, "relation_hash")] = struct{}{}
		}
		for _, hashValue := range hashes {
			if _, err := c.Exec("INSERT OR IGNORE INTO paragraph_relations (paragraph_hash, relation_hash) VALUES (?, ?)",
				sourceParagraph, hashValue); err != nil {
				return nil, err
			}
		}
		newEvidence := make([]string, 0, len(uniqueHashes))
		for _, hashValue := range uniqueHashes {
			if _, ok := existing[hashValue]; !ok {
				newEvidence = append(newEvidence, hashValue)
			}
		}
		s.ObserveRelationEvidence(newEvidence, now, c)
		sources := s.GetSourcesForParagraphHashes([]string{sourceParagraph}, true, c)
		s.EnqueueEpisodeSourceRebuilds(sources, "paragraph_relation_linked", EpisodeEnqueueOptions{Conn: c})
	} else if err := c.Commit(); err != nil {
		return nil, err
	}
	zap.L().Debug("批量添加关系", zap.Int("items", len(relations)))
	return hashes, nil
}

// LinkParagraphRelation 关联段落和关系（幂等），对齐 Python link_paragraph_relation
func (s *Store) LinkParagraphRelation(paragraphHash string, relationHash string) (bool, error) {
	linked := false
	err := s.Transaction(true, func(tx *Tx) error {
		result, err := tx.Exec("INSERT OR IGNORE INTO paragraph_relations (paragraph_hash, relation_hash) VALUES (?, ?)",
			paragraphHash, relationHash)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		linked = affected > 0
		if linked {
			s.ObserveRelationEvidence([]string{relationHash}, nowTimestamp(), tx.Conn())
			sources := s.GetSourcesForParagraphHashes([]string{paragraphHash}, true, tx.Conn())
			s.EnqueueEpisodeSourceRebuilds(sources, "paragraph_relation_linked", EpisodeEnqueueOptions{Conn: tx.Conn()})
		}
		return nil
	})
	if err != nil {
		if isIntegrityError(err) {
			return false, nil
		}
		return false, err
	}
	return linked, nil
}

// LinkParagraphEntity 关联段落和实体（幂等），对齐 Python link_paragraph_entity
func (s *Store) LinkParagraphEntity(paragraphHash string, entityHash string, mentionCount int) (bool, error) {
	err := s.Transaction(true, func(tx *Tx) error {
		result, err := tx.Exec("INSERT OR IGNORE INTO paragraph_entities (paragraph_hash, entity_hash, mention_count) VALUES (?, ?, ?)",
			paragraphHash, entityHash, mentionCount)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			if _, err := tx.Exec(`UPDATE paragraph_entities
				SET mention_count = mention_count + ?
				WHERE paragraph_hash = ? AND entity_hash = ?`, mentionCount, paragraphHash, entityHash); err != nil {
				return err
			}
		}
		sources := s.GetSourcesForParagraphHashes([]string{paragraphHash}, true, tx.Conn())
		s.EnqueueEpisodeSourceRebuilds(sources, "paragraph_entity_linked", EpisodeEnqueueOptions{Conn: tx.Conn()})
		return nil
	})
	if err != nil {
		if isIntegrityError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
