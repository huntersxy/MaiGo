package storage

// A_memorix Episode 重建队列基础操作，对齐 Python core/storage/metadata_episode.py 的入队与来源查询
// 其余 Episode 构建逻辑在同一文件继续扩展

import (
	"database/sql"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// normalizeEpisodeSource 对齐 Python _normalize_episode_source
func normalizeEpisodeSource(source string) string {
	return strings.TrimSpace(source)
}

// dedupeEpisodeSources 保序去重，对齐 Python _dedupe_episode_sources
func dedupeEpisodeSources(sources []string) []string {
	normalized := make([]string, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, item := range sources {
		token := normalizeEpisodeSource(item)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		normalized = append(normalized, token)
	}
	return normalized
}

// asOptionalFloat 对齐 Python _as_optional_float
func asOptionalFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// GetSourcesForParagraphHashes 查询段落对应的来源集合，对齐 Python _get_sources_for_paragraph_hashes
func (s *Store) GetSourcesForParagraphHashes(hashes []string, includeDeleted bool, conn *Conn) []string {
	normalized := make([]string, 0, len(hashes))
	for _, item := range hashes {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return []string{}
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		zap.L().Warn("查询段落来源失败", zap.Error(err))
		return []string{}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(normalized)), ",")
	conditions := []string{
		fmt.Sprintf("hash IN (%s)", placeholders),
		"TRIM(COALESCE(source, '')) != ''",
	}
	if !includeDeleted {
		conditions = append(conditions, "(is_deleted IS NULL OR is_deleted = 0)")
	}
	query := fmt.Sprintf(`SELECT DISTINCT TRIM(source) AS source
		FROM paragraphs
		WHERE %s`, strings.Join(conditions, " AND "))
	args := make([]any, len(normalized))
	for i, item := range normalized {
		args[i] = item
	}
	rows, err := c.Query(query, args...)
	if err != nil {
		zap.L().Warn("查询段落来源失败", zap.Error(err))
		return []string{}
	}
	defer rows.Close()
	values := make([]string, 0, len(normalized))
	for rows.Next() {
		var source sql.NullString
		if err := rows.Scan(&source); err != nil {
			zap.L().Warn("查询段落来源失败", zap.Error(err))
			return []string{}
		}
		values = append(values, source.String)
	}
	if err := rows.Err(); err != nil {
		zap.L().Warn("查询段落来源失败", zap.Error(err))
		return []string{}
	}
	return dedupeEpisodeSources(values)
}

// EpisodeEnqueueOptions 对齐 Python _enqueue_episode_source_rebuilds 的关键字参数
type EpisodeEnqueueOptions struct {
	// DirtyStart 本轮脏区间起点
	DirtyStart *float64
	// DirtyEnd 本轮脏区间终点
	DirtyEnd *float64
	// DebounceSeconds 去抖延迟，默认 5 秒
	DebounceSeconds float64
	// Now 指定的当前时间，nil 表示取系统时间
	Now *float64
	// Conn 指定连接，nil 表示使用存储主连接
	Conn *Conn
}

// EnqueueEpisodeSourceRebuilds 原子提升来源期望版本并合并脏区间，对齐 Python _enqueue_episode_source_rebuilds
func (s *Store) EnqueueEpisodeSourceRebuilds(sources []string, reason string, opts EpisodeEnqueueOptions) int {
	normalized := dedupeEpisodeSources(sources)
	if len(normalized) == 0 {
		return 0
	}
	c, err := s.ResolveConn(opts.Conn)
	if err != nil {
		zap.L().Warn("Episode 重建入队失败", zap.Error(err))
		return 0
	}
	nowTs := nowTimestamp()
	if opts.Now != nil {
		nowTs = *opts.Now
	}
	debounce := opts.DebounceSeconds
	if debounce < 0 {
		debounce = 0
	}
	readyAt := nowTs + debounce
	reasonText := strings.TrimSpace(reason)
	if len([]rune(reasonText)) > 200 {
		reasonText = string([]rune(reasonText)[:200])
	}
	var reasonValue any
	if reasonText != "" {
		reasonValue = reasonText
	}
	statement := `INSERT INTO episode_rebuild_sources (
			source, status, retry_count, last_error, reason, requested_at, updated_at,
			desired_revision, built_revision, claimed_revision,
			dirty_start, dirty_end, first_requested_at, ready_at,
			lease_token, lease_until, next_attempt_at,
			built_generation_hash, claimed_generation_hash,
			retry_revision, retry_generation_hash
		) VALUES (
			?, 'pending', 0, NULL, ?, ?, ?, 1, 0, NULL, ?, ?, ?, ?,
			NULL, NULL, NULL, NULL, NULL, NULL, NULL
		)
		ON CONFLICT(source) DO UPDATE SET
			status = CASE
				WHEN episode_rebuild_sources.lease_token IS NOT NULL
				 AND COALESCE(episode_rebuild_sources.lease_until, 0) > excluded.requested_at
				THEN 'running'
				ELSE 'pending'
			END,
			retry_count = 0,
			last_error = NULL,
			reason = CASE
				WHEN episode_rebuild_sources.reason = 'fact_evidence_retracted'
				 AND episode_rebuild_sources.desired_revision > episode_rebuild_sources.built_revision
				THEN episode_rebuild_sources.reason ELSE excluded.reason END,
			requested_at = excluded.requested_at,
			updated_at = excluded.updated_at,
			desired_revision = COALESCE(episode_rebuild_sources.desired_revision, 0) + 1,
			dirty_start = CASE
				WHEN excluded.dirty_start IS NULL THEN episode_rebuild_sources.dirty_start
				WHEN episode_rebuild_sources.dirty_start IS NULL THEN excluded.dirty_start
				ELSE MIN(episode_rebuild_sources.dirty_start, excluded.dirty_start)
			END,
			dirty_end = CASE
				WHEN excluded.dirty_end IS NULL THEN episode_rebuild_sources.dirty_end
				WHEN episode_rebuild_sources.dirty_end IS NULL THEN excluded.dirty_end
				ELSE MAX(episode_rebuild_sources.dirty_end, excluded.dirty_end)
			END,
			first_requested_at = CASE
				WHEN COALESCE(episode_rebuild_sources.desired_revision, 0)
					 <= COALESCE(episode_rebuild_sources.built_revision, 0)
				THEN excluded.first_requested_at
				ELSE COALESCE(episode_rebuild_sources.first_requested_at, excluded.first_requested_at)
			END,
			ready_at = excluded.ready_at,
			next_attempt_at = NULL,
			retry_revision = NULL,
			retry_generation_hash = NULL`
	for _, source := range normalized {
		if _, err := c.Exec(statement, source, reasonValue, nowTs, nowTs,
			asOptionalFloat(opts.DirtyStart), asOptionalFloat(opts.DirtyEnd), nowTs, readyAt); err != nil {
			zap.L().Warn("Episode 重建入队失败", zap.Error(err), zap.String("source", source))
			return 0
		}
	}
	if err := c.Commit(); err != nil {
		zap.L().Warn("Episode 重建入队提交失败", zap.Error(err))
		return 0
	}
	return len(normalized)
}

// EnqueueEpisodeSourceRebuild 将单个 source 入队，对齐 Python enqueue_episode_source_rebuild
func (s *Store) EnqueueEpisodeSourceRebuild(source string, reason string, opts EpisodeEnqueueOptions) bool {
	return s.EnqueueEpisodeSourceRebuilds([]string{source}, reason, opts) > 0
}
