package storage

// A_memorix Episode 来源重建队列的查询与维护
// 对齐 Python core/storage/metadata_episode.py 的 discard / list / summary / blocked flags

import (
	"fmt"
	"sort"
	"strings"
)

// episodeMigrationRebuildReasons 对齐 Python _MIGRATION_REBUILD_REASONS
var episodeMigrationRebuildReasons = [2]string{"schema_19_pending_migration", "schema_19_source_discovery"}

// episodeRebuildStatuses 对齐 Python list_episode_source_rebuilds 允许的状态过滤值
var episodeRebuildStatuses = map[string]struct{}{
	"pending": {},
	"running": {},
	"done":    {},
	"failed":  {},
}

// DiscardMigrationEpisodeRebuilds 仅丢弃升级迁移留下的来源任务，保留已有 Episode 和后续增量任务
// 对齐 Python discard_migration_episode_rebuilds
func (s *Store) DiscardMigrationEpisodeRebuilds(dryRun bool, conn *Conn) (map[string]any, error) {
	nowTs := nowTimestamp()
	reasons := episodeMigrationRebuildReasons
	eligibleSQL := "reason IN (?, ?) AND " +
		"(lease_token IS NULL OR lease_token = '' OR COALESCE(lease_until, 0) <= ?)"
	byStatus := make(map[string]int)
	candidates := 0
	sample := make([]string, 0, 10)
	active := 0
	discarded := 0

	err := s.episodeRunTx(!dryRun, conn, func(tx *Tx) error {
		rows, err := tx.Query(
			"SELECT status, COUNT(*) AS count FROM episode_rebuild_sources WHERE "+eligibleSQL+" GROUP BY status",
			reasons[0], reasons[1], nowTs,
		)
		if err != nil {
			return err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		for _, item := range items {
			status := "None"
			if raw, ok := item["status"]; ok && raw != nil {
				status = stringFromRow(item, "status")
			}
			count := episodeRowInt(item, "count")
			byStatus[status] = count
			candidates += count
		}

		sampleRows, err := tx.Query(
			"SELECT source FROM episode_rebuild_sources WHERE "+eligibleSQL+
				" ORDER BY requested_at ASC, source ASC LIMIT 10",
			reasons[0], reasons[1], nowTs,
		)
		if err != nil {
			return err
		}
		sampleItems, err := scanRowsToMaps(sampleRows)
		if err != nil {
			return err
		}
		for _, item := range sampleItems {
			sample = append(sample, episodePyStr(item["source"]))
		}

		var activeCount int64
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM episode_rebuild_sources "+
				"WHERE reason IN (?, ?) AND lease_token IS NOT NULL "+
				"AND lease_token != '' AND COALESCE(lease_until, 0) > ?",
			reasons[0], reasons[1], nowTs,
		).Scan(&activeCount); err != nil {
			return err
		}
		active = int(activeCount)

		if !dryRun && candidates > 0 {
			result, err := tx.Exec("DELETE FROM episode_rebuild_sources WHERE "+eligibleSQL,
				reasons[0], reasons[1], nowTs)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			discarded = int(affected)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"dry_run":        dryRun,
		"candidates":     candidates,
		"discarded":      discarded,
		"active_skipped": active,
		"by_status":      byStatus,
		"sample_sources": sample,
	}, nil
}

// ListEpisodeSourceRebuilds 列出 source 重建状态，对齐 Python list_episode_source_rebuilds
// limit 为 0 时取 Python 默认值 100
func (s *Store) ListEpisodeSourceRebuilds(statuses []string, limit int, conn *Conn) ([]map[string]any, error) {
	safeLimit := limit
	if safeLimit == 0 {
		safeLimit = 100
	}
	if safeLimit < 1 {
		safeLimit = 1
	}
	conditions := make([]string, 0, 1)
	args := make([]any, 0, len(statuses)+1)
	normalized := make([]string, 0, len(statuses))
	for _, item := range statuses {
		token := strings.ToLower(strings.TrimSpace(item))
		if _, ok := episodeRebuildStatuses[token]; !ok {
			continue
		}
		normalized = append(normalized, token)
	}
	if len(normalized) > 0 {
		conditions = append(conditions, fmt.Sprintf("status IN (%s)", sqlPlaceholders(len(normalized))))
		for _, token := range normalized {
			args = append(args, token)
		}
	}
	whereSQL := ""
	if len(conditions) > 0 {
		whereSQL = "WHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, safeLimit)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT "+episodeRebuildColumns+" FROM episode_rebuild_sources "+
		whereSQL+" ORDER BY updated_at DESC, source ASC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	return scanRowsToMaps(rows)
}

// GetEpisodeSourceRebuildSummary 汇总 source 重建队列状态
// 对齐 Python get_episode_source_rebuild_summary，failedLimit 为 0 时取 20
func (s *Store) GetEpisodeSourceRebuildSummary(failedLimit int, conn *Conn) (map[string]any, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT status, COUNT(*) AS cnt
		FROM episode_rebuild_sources
		GROUP BY status`)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"pending": 0, "running": 0, "done": 0, "failed": 0, "total": 0}
	for _, item := range items {
		status := strings.ToLower(strings.TrimSpace(stringFromRow(item, "status")))
		count := episodeRowInt(item, "cnt")
		counts[status] += count
		counts["total"] += count
	}

	running, err := s.ListEpisodeSourceRebuilds([]string{"running"}, 20, c)
	if err != nil {
		return nil, err
	}
	limit := failedLimit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 {
		limit = 1
	}
	failed, err := s.ListEpisodeSourceRebuilds([]string{"failed"}, limit, c)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"counts":  counts,
		"running": running,
		"failed":  failed,
	}, nil
}

// ListEpisodeSourcesForRebuild 列出全量重建涉及的 source（live paragraphs + stale episodes）
// 对齐 Python list_episode_sources_for_rebuild
func (s *Store) ListEpisodeSourcesForRebuild(conn *Conn) ([]string, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT DISTINCT source
		FROM (
			SELECT TRIM(source) AS source
			FROM paragraphs
			WHERE TRIM(COALESCE(source, '')) != ''
			  AND (is_deleted IS NULL OR is_deleted = 0)
			UNION
			SELECT TRIM(source) AS source
			FROM episodes
			WHERE TRIM(COALESCE(source, '')) != ''
		)
		WHERE TRIM(COALESCE(source, '')) != ''
		ORDER BY source ASC`)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, stringFromRow(item, "source"))
	}
	return dedupeEpisodeSources(values), nil
}

// IsEpisodeSourceQueryBlocked 仅在来源尚无任何完整物化版本时报告阻塞
// 对齐 Python is_episode_source_query_blocked
func (s *Store) IsEpisodeSourceQueryBlocked(source string, conn *Conn) (bool, error) {
	flags, err := s.GetEpisodeSourceQueryBlockedFlags([]string{source}, conn)
	if err != nil {
		return false, err
	}
	return flags[source], nil
}

// GetEpisodeSourceQueryBlockedFlags 批量判断来源是否处于「已登记重建但尚无任何完整物化版本」的阻塞态
// 对齐 Python get_episode_source_query_blocked_flags
func (s *Store) GetEpisodeSourceQueryBlockedFlags(sources []string, conn *Conn) (map[string]bool, error) {
	normalized := make(map[string]string, len(sources))
	for _, source := range sources {
		token := normalizeEpisodeSource(source)
		if token != "" {
			normalized[source] = token
		}
	}
	if len(normalized) == 0 {
		flags := make(map[string]bool, len(sources))
		for _, source := range sources {
			flags[source] = false
		}
		return flags, nil
	}

	tokenSet := make(map[string]struct{}, len(normalized))
	for _, token := range normalized {
		tokenSet[token] = struct{}{}
	}
	tokens := make([]string, 0, len(tokenSet))
	for token := range tokenSet {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(tokens))
	for _, token := range tokens {
		args = append(args, token)
	}
	rows, err := c.Query(fmt.Sprintf(`SELECT source, desired_revision, built_revision, reason
		FROM episode_rebuild_sources
		WHERE source IN (%s)`, sqlPlaceholders(len(tokens))), args...)
	if err != nil {
		return nil, err
	}
	sourceRows, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}

	invalidated := make(map[string]struct{}, len(sourceRows))
	candidates := make(map[string]struct{}, len(sourceRows))
	for _, row := range sourceRows {
		sourceToken := stringFromRow(row, "source")
		reason := stringFromRow(row, "reason")
		desired := episodeRowInt(row, "desired_revision")
		built := episodeRowInt(row, "built_revision")
		if reason == "fact_evidence_retracted" && desired > built {
			invalidated[sourceToken] = struct{}{}
		}
		if built <= 0 && desired > 0 {
			candidates[sourceToken] = struct{}{}
		}
	}

	materialized := make(map[string]struct{})
	if len(candidates) > 0 {
		rows, err := c.Query(`SELECT DISTINCT TRIM(COALESCE(source, '')) AS source
			FROM episodes`)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			source := stringFromRow(item, "source")
			if source != "" {
				materialized[source] = struct{}{}
			}
		}
	}

	flags := make(map[string]bool, len(normalized))
	for source, token := range normalized {
		blocked := false
		if _, ok := invalidated[token]; ok {
			blocked = true
		} else if _, ok := candidates[token]; ok {
			if _, ok := materialized[token]; !ok {
				blocked = true
			}
		}
		flags[source] = blocked
	}
	return flags, nil
}

// GetEpisodeEmptyGroupFingerprints 读取已成功处理且结果为空的分组
// 对齐 Python get_episode_empty_group_fingerprints
func (s *Store) GetEpisodeEmptyGroupFingerprints(source string, conn *Conn) ([]string, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT input_fingerprint FROM episode_empty_groups WHERE source = ?",
		normalizeEpisodeSource(source))
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, episodePyStr(item["input_fingerprint"]))
	}
	return out, nil
}
