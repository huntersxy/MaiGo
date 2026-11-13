package storage

// A_memorix Episode 按来源全量替换，对齐 Python core/storage/metadata_episode.py 的 replace_episodes_for_source

import (
	"strings"

	"maibot-go/internal/A_memorix/core/utils"
)

// ReplaceEpisodesForSource 按 source 全量替换 episode 结果
// 对齐 Python replace_episodes_for_source；conn 非 nil 时在同一连接上开事务（发布路径复用）
func (s *Store) ReplaceEpisodesForSource(source string, payloads []map[string]any, emptyGroupFingerprints []string, conn *Conn) (map[string]any, error) {
	token := normalizeEpisodeSource(source)
	if token == "" {
		return map[string]any{"source": "", "episode_count": 0}, nil
	}
	normalizedPayloads := make([]map[string]any, 0, len(payloads))
	for _, item := range payloads {
		if item == nil {
			continue
		}
		normalizedPayloads = append(normalizedPayloads, item)
	}
	now := nowTimestamp()

	result := map[string]any{}
	err := s.episodeRunTx(true, conn, func(tx *Tx) error {
		// 空结果与情景快照在同一事务中发布；失效租约不能提前写入缓存
		if _, err := tx.Exec("DELETE FROM episode_empty_groups WHERE source = ?", token); err != nil {
			return err
		}
		for _, fingerprint := range dedupeStrings(emptyGroupFingerprints) {
			if _, err := tx.Exec("INSERT INTO episode_empty_groups (source, input_fingerprint) VALUES (?, ?)",
				token, fingerprint); err != nil {
				return err
			}
		}

		rows, err := tx.Query(`SELECT episode_id, created_at
			FROM episodes
			WHERE TRIM(COALESCE(source, '')) = ?`, token)
		if err != nil {
			return err
		}
		existingRows, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		existingCreatedAt := make(map[string]*float64, len(existingRows))
		for _, row := range existingRows {
			existingCreatedAt[episodePyStr(row["episode_id"])] = episodeOptionalFloat(row["created_at"])
		}

		if _, err := tx.Exec("DELETE FROM episodes WHERE TRIM(COALESCE(source, '')) = ?", token); err != nil {
			return err
		}

		inserted := 0
		for _, payload := range normalizedPayloads {
			title := strings.TrimSpace(episodeStrOrEmpty(payload["title"]))
			summary := strings.TrimSpace(episodeStrOrEmpty(payload["summary"]))
			evidenceIDs := dedupeStrings(episodePyStrList(payload["evidence_ids"]))
			if title == "" || summary == "" || len(evidenceIDs) == 0 {
				continue
			}

			episodeID := strings.TrimSpace(episodeStrOrEmpty(payload["episode_id"]))
			if episodeID == "" {
				seed := episodeHashSeed(token, title, summary,
					payload["event_time_start"], payload["event_time_end"], evidenceIDs)
				hashed, hashErr := utils.ComputeHash(seed)
				if hashErr != nil {
					return hashErr
				}
				episodeID = hashed
			}

			participants := episodeTruncateList(episodePyStrList(payload["participants"]), 16)
			keywords := episodeTruncateList(episodePyStrList(payload["keywords"]), 20)

			paragraphCount := len(evidenceIDs)
			if raw, ok := payload["paragraph_count"]; ok && raw != nil {
				if value, ok := episodeToInt(raw); ok {
					paragraphCount = value
				}
			}
			if paragraphCount <= 0 {
				paragraphCount = len(evidenceIDs)
			}
			if paragraphCount <= 0 {
				continue
			}

			timeConfidence := episodeFloatOr(payload["time_confidence"], 1.0)
			llmConfidence := episodeFloatOr(payload["llm_confidence"], 0.0)

			createdTs := now
			if existing, ok := existingCreatedAt[episodeID]; ok && existing != nil {
				createdTs = *existing
			}
			updatedTs := now
			if value := episodeOptionalFloat(payload["updated_at"]); value != nil && *value != 0 {
				updatedTs = *value
			}

			if _, err := tx.Exec(`INSERT INTO episodes (
					episode_id, source, title, summary,
					event_time_start, event_time_end, time_granularity, time_confidence,
					participants_json, keywords_json, evidence_ids_json,
					paragraph_count, llm_confidence, segmentation_model, segmentation_version,
					input_fingerprint, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				episodeID,
				token,
				truncateRunes(title, 120),
				truncateRunes(summary, 2000),
				episodeOptionalFloat(payload["event_time_start"]),
				episodeOptionalFloat(payload["event_time_end"]),
				optionalString(strings.TrimSpace(episodeStrOrEmpty(payload["time_granularity"]))),
				timeConfidence,
				jsonDumps(participants),
				jsonDumps(keywords),
				jsonDumps(evidenceIDs),
				paragraphCount,
				llmConfidence,
				optionalString(strings.TrimSpace(episodeStrOrEmpty(payload["segmentation_model"]))),
				optionalString(strings.TrimSpace(episodeStrOrEmpty(payload["segmentation_version"]))),
				optionalString(strings.TrimSpace(episodeStrOrEmpty(payload["input_fingerprint"]))),
				createdTs,
				updatedTs,
			); err != nil {
				return err
			}
			for index, hashValue := range evidenceIDs {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO episode_paragraphs (episode_id, paragraph_hash, position)
					VALUES (?, ?, ?)`, episodeID, hashValue, index); err != nil {
					return err
				}
			}
			inserted++
		}
		result["source"] = token
		result["episode_count"] = inserted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// episodeTruncateList 对齐 Python 列表切片 [:limit]
func episodeTruncateList(values []string, limit int) []string {
	if limit < 0 {
		limit = 0
	}
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}
