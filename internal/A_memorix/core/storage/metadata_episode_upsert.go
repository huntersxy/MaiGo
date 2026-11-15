package storage

// A_memorix Episode 写入：upsert / 绑定段落 / 读取单条与列表
// 对齐 Python core/storage/metadata_episode.py 的写侧与详情读取方法

import (
	"errors"
	"strings"

	"maibot-go/internal/A_memorix/core/utils"
)

// 与 Python 逐字一致的错误文案
var (
	// ErrEpisodePayloadInvalid 对应 Python ValueError("payload 必须是字典")
	ErrEpisodePayloadInvalid = errors.New("payload 必须是字典")
	// ErrEpisodeTitleEmpty 对应 Python ValueError("episode.title 不能为空")
	ErrEpisodeTitleEmpty = errors.New("episode.title 不能为空")
	// ErrEpisodeSummaryEmpty 对应 Python ValueError("episode.summary 不能为空")
	ErrEpisodeSummaryEmpty = errors.New("episode.summary 不能为空")
	// ErrEpisodeIDEmpty 对应 Python ValueError("episode_id 不能为空")
	ErrEpisodeIDEmpty = errors.New("episode_id 不能为空")
)

// UpsertEpisode 写入或更新 episode，对齐 Python upsert_episode
func (s *Store) UpsertEpisode(payload map[string]any, conn *Conn) (map[string]any, error) {
	if payload == nil {
		return nil, ErrEpisodePayloadInvalid
	}
	title := strings.TrimSpace(episodeStrOrEmpty(payload["title"]))
	if title == "" {
		return nil, ErrEpisodeTitleEmpty
	}
	summary := strings.TrimSpace(episodeStrOrEmpty(payload["summary"]))
	if summary == "" {
		return nil, ErrEpisodeSummaryEmpty
	}

	sourceToken := strings.TrimSpace(episodeStrOrEmpty(payload["source"]))
	var source any
	if sourceToken != "" {
		source = sourceToken
	}
	participants := episodePyStrList(payload["participants"])
	keywords := episodePyStrList(payload["keywords"])
	evidenceIDs := episodePyStrList(payload["evidence_ids"])

	now := nowTimestamp()
	createdAt := episodeOptionalFloat(payload["created_at"])
	updatedAt := episodeOptionalFloat(payload["updated_at"])
	createdTs := now
	if createdAt != nil {
		createdTs = *createdAt
	}
	updatedTs := now
	if updatedAt != nil {
		updatedTs = *updatedAt
	}

	episodeID := strings.TrimSpace(episodeStrOrEmpty(payload["episode_id"]))
	if episodeID == "" {
		seed := episodeHashSeed(source, title, summary,
			payload["event_time_start"], payload["event_time_end"], evidenceIDs)
		hashed, err := utils.ComputeHash(seed)
		if err != nil {
			return nil, err
		}
		episodeID = hashed
	}

	paragraphCount := len(evidenceIDs)
	if raw, ok := payload["paragraph_count"]; ok && raw != nil {
		if value, ok := episodeToInt(raw); ok {
			paragraphCount = value
		}
	}

	timeConfidence := episodeFloatOr(payload["time_confidence"], 1.0)
	llmConfidence := episodeFloatOr(payload["llm_confidence"], 0.0)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	if column, err := c.Query("SELECT created_at FROM episodes WHERE episode_id = ? LIMIT 1", episodeID); err == nil {
		items, scanErr := scanRowsToMaps(column)
		if scanErr != nil {
			return nil, scanErr
		}
		if len(items) > 0 {
			if existing := episodeOptionalFloat(items[0]["created_at"]); existing != nil {
				createdTs = *existing
			}
		}
	} else {
		return nil, err
	}

	if _, err := c.Exec(`INSERT INTO episodes (
			episode_id, source, title, summary,
			event_time_start, event_time_end, time_granularity, time_confidence,
			participants_json, keywords_json, evidence_ids_json,
			paragraph_count, llm_confidence, segmentation_model, segmentation_version,
			input_fingerprint, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(episode_id) DO UPDATE SET
			source = excluded.source,
			title = excluded.title,
			summary = excluded.summary,
			event_time_start = excluded.event_time_start,
			event_time_end = excluded.event_time_end,
			time_granularity = excluded.time_granularity,
			time_confidence = excluded.time_confidence,
			participants_json = excluded.participants_json,
			keywords_json = excluded.keywords_json,
			evidence_ids_json = excluded.evidence_ids_json,
			paragraph_count = excluded.paragraph_count,
			llm_confidence = excluded.llm_confidence,
			segmentation_model = excluded.segmentation_model,
			segmentation_version = excluded.segmentation_version,
			input_fingerprint = excluded.input_fingerprint,
			updated_at = excluded.updated_at`,
		episodeID,
		source,
		title,
		summary,
		episodeOptionalFloat(payload["event_time_start"]),
		episodeOptionalFloat(payload["event_time_end"]),
		optionalString(strings.TrimSpace(episodeStrOrEmpty(payload["time_granularity"]))),
		timeConfidence,
		jsonDumps(participants),
		jsonDumps(keywords),
		jsonDumps(evidenceIDs),
		max(0, paragraphCount),
		llmConfidence,
		optionalString(strings.TrimSpace(episodeStrOrEmpty(payload["segmentation_model"]))),
		optionalString(strings.TrimSpace(episodeStrOrEmpty(payload["segmentation_version"]))),
		optionalString(strings.TrimSpace(episodeStrOrEmpty(payload["input_fingerprint"]))),
		createdTs,
		updatedTs,
	); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}
	stored, err := s.GetEpisodeByID(episodeID, c)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return map[string]any{"episode_id": episodeID}, nil
	}
	return stored, nil
}

// BindEpisodeParagraphs 重建 episode 与段落映射，对齐 Python bind_episode_paragraphs
func (s *Store) BindEpisodeParagraphs(episodeID string, paragraphHashesOrdered []string, conn *Conn) (int, error) {
	token := strings.TrimSpace(episodeID)
	if token == "" {
		return 0, ErrEpisodeIDEmpty
	}
	normalized := dedupeStrings(func() []string {
		values := make([]string, 0, len(paragraphHashesOrdered))
		for _, item := range paragraphHashesOrdered {
			values = append(values, strings.TrimSpace(item))
		}
		return values
	}())

	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	if _, err := c.Exec("DELETE FROM episode_paragraphs WHERE episode_id = ?", token); err != nil {
		return 0, err
	}
	for index, hashValue := range normalized {
		if _, err := c.Exec(`INSERT OR IGNORE INTO episode_paragraphs (episode_id, paragraph_hash, position)
			VALUES (?, ?, ?)`, token, hashValue, index); err != nil {
			return 0, err
		}
	}
	if _, err := c.Exec(`UPDATE episodes
		SET paragraph_count = ?, updated_at = ?
		WHERE episode_id = ?`, len(normalized), nowTimestamp(), token); err != nil {
		return 0, err
	}
	if err := c.Commit(); err != nil {
		return 0, err
	}
	return len(normalized), nil
}

// GetEpisodeByID 获取单条 episode，对齐 Python get_episode_by_id（不存在返回 nil, nil）
func (s *Store) GetEpisodeByID(episodeID string, conn *Conn) (map[string]any, error) {
	token := strings.TrimSpace(episodeID)
	if token == "" {
		return nil, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT * FROM episodes WHERE episode_id = ? LIMIT 1", token)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return episodeRowToDict(items[0]), nil
}

// GetEpisodesBySource 读取来源下的全部 Episode，供确定性重建缓存复用
// 对齐 Python get_episodes_by_source
func (s *Store) GetEpisodesBySource(source string, conn *Conn) ([]map[string]any, error) {
	token := normalizeEpisodeSource(source)
	if token == "" {
		return []map[string]any{}, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT *
		FROM episodes
		WHERE TRIM(COALESCE(source, '')) = ?
		ORDER BY event_time_start ASC, created_at ASC, episode_id ASC`, token)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, episodeRowToDict(item))
	}
	return out, nil
}

// GetEpisodeParagraphs 获取 episode 关联段落（按 position 排序）
// 对齐 Python get_episode_paragraphs，limit 为 0 时取 100
func (s *Store) GetEpisodeParagraphs(episodeID string, limit int, conn *Conn) ([]map[string]any, error) {
	token := strings.TrimSpace(episodeID)
	if token == "" {
		return []map[string]any{}, nil
	}
	safeLimit := limit
	if safeLimit == 0 {
		safeLimit = 100
	}
	if safeLimit < 1 {
		safeLimit = 1
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT p.*, ep.position
		FROM episode_paragraphs ep
		JOIN paragraphs p ON p.hash = ep.paragraph_hash
		WHERE ep.episode_id = ?
		  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
		ORDER BY ep.position ASC
		LIMIT ?`, token, safeLimit)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		payload := s.rowToDict(item)
		payload["position"] = item["position"]
		out = append(out, payload)
	}
	return out, nil
}

// HasTable 检查数据库是否存在指定表，对齐 Python has_table
func (s *Store) HasTable(tableName string, conn *Conn) bool {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false
	}
	found, err := s.tableExists(c, tableName)
	if err != nil {
		return false
	}
	return found
}
