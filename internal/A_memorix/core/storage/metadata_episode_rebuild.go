package storage

// A_memorix Episode 来源重建队列状态机：租约、重试、revision、generation hash
// 对齐 Python core/storage/metadata_episode.py 的 claim / renew / publish / fail

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// 与 Python 逐字一致的错误文案
var (
	// ErrEpisodeGenerationHashEmpty 对应 Python ValueError("generation_hash 不能为空")
	ErrEpisodeGenerationHashEmpty = errors.New("generation_hash 不能为空")
	// ErrEpisodeMaxRetryInvalid 对应 Python ValueError("max_retry 必须至少为1")
	ErrEpisodeMaxRetryInvalid = errors.New("max_retry 必须至少为1")
	// ErrEpisodePublishArgsEmpty 对应 Python ValueError("source、lease_token 和 generation_hash 不能为空")
	ErrEpisodePublishArgsEmpty = errors.New("source、lease_token 和 generation_hash 不能为空")
	// ErrEpisodePublishLeaseChanged 对应 Python RuntimeError("Episode 来源发布期间租约发生变化")
	ErrEpisodePublishLeaseChanged = errors.New("Episode 来源发布期间租约发生变化")
)

// episodeRebuildColumns 对齐 Python 显式列出的 episode_rebuild_sources 列
const episodeRebuildColumns = `source, status, retry_count, last_error, reason, requested_at, updated_at,
	desired_revision, built_revision, claimed_revision,
	dirty_start, dirty_end, first_requested_at, ready_at,
	lease_token, lease_until, next_attempt_at,
	built_generation_hash, claimed_generation_hash,
	retry_revision, retry_generation_hash`

// EpisodeClaimOptions 对齐 Python claim_episode_source_rebuild_batch 的关键字参数
// 零值取 Python 默认值：Limit=20、MaxRetry=3、LeaseSeconds=1800、MaxWaitSeconds=60
// MaxRetry 显式传负数才触发 "max_retry 必须至少为1"
type EpisodeClaimOptions struct {
	// GenerationHash 本轮构建的代际哈希，用于判定 built 版本是否过期
	GenerationHash string
	// Sources 限定来源范围，nil 表示不限来源
	Sources []string
	// Limit 单次领取上限
	Limit int
	// MaxRetry 同一 revision + generation 的最大重试次数
	MaxRetry int
	// LeaseSeconds 租约时长
	LeaseSeconds float64
	// MaxWaitSeconds 超过该等待时长后忽略 ready_at 去抖
	MaxWaitSeconds float64
	// Now 指定当前时间，nil 表示取系统时间
	Now *float64
}

// ClaimEpisodeSourceRebuildBatch 领取到期的来源任务，对齐 Python claim_episode_source_rebuild_batch
func (s *Store) ClaimEpisodeSourceRebuildBatch(opts EpisodeClaimOptions, conn *Conn) ([]map[string]any, error) {
	safeLimit := opts.Limit
	if safeLimit == 0 {
		safeLimit = 20
	}
	if safeLimit < 1 {
		safeLimit = 1
	}
	maxRetry := opts.MaxRetry
	if maxRetry == 0 {
		maxRetry = 3
	}
	if maxRetry < 1 {
		return nil, ErrEpisodeMaxRetryInvalid
	}
	generation := strings.TrimSpace(opts.GenerationHash)
	if generation == "" {
		return nil, ErrEpisodeGenerationHashEmpty
	}
	scope := dedupeEpisodeSources(opts.Sources)
	if opts.Sources != nil && len(scope) == 0 {
		return []map[string]any{}, nil
	}
	leaseSeconds := opts.LeaseSeconds
	if leaseSeconds == 0 {
		leaseSeconds = 1800
	}
	maxWaitSeconds := opts.MaxWaitSeconds
	if maxWaitSeconds == 0 {
		maxWaitSeconds = 60
	}
	nowTs := nowTimestamp()
	if opts.Now != nil {
		nowTs = *opts.Now
	}
	leaseUntil := nowTs + math.Max(1, leaseSeconds)
	maxWaitCutoff := nowTs - math.Max(0, maxWaitSeconds)

	sourceFilterSQL := ""
	queryArgs := []any{generation, generation, maxRetry, nowTs, generation, nowTs, nowTs, maxWaitCutoff}
	if len(scope) > 0 {
		payload, err := json.Marshal(scope)
		if err != nil {
			return nil, err
		}
		sourceFilterSQL = "AND source IN (SELECT value FROM json_each(?))"
		queryArgs = append(queryArgs, string(payload))
	}
	queryArgs = append(queryArgs, safeLimit)

	selectSQL := `SELECT source
		FROM episode_rebuild_sources
		WHERE (
				COALESCE(desired_revision, 0) > COALESCE(built_revision, 0)
			 OR COALESCE(built_generation_hash, '') != ?
			)
		  AND (
				retry_revision IS NULL
			 OR retry_revision != desired_revision
			 OR COALESCE(retry_generation_hash, '') != ?
			 OR COALESCE(retry_count, 0) < ?
			)
		  AND (lease_token IS NULL OR COALESCE(lease_until, 0) <= ?)
		  AND (
				retry_revision IS NULL
			 OR retry_revision != desired_revision
			 OR COALESCE(retry_generation_hash, '') != ?
			 OR next_attempt_at IS NULL
			 OR next_attempt_at <= ?
			)
		  AND (
				COALESCE(ready_at, requested_at) <= ?
			 OR COALESCE(first_requested_at, requested_at) <= ?
			)
		` + sourceFilterSQL + `
		ORDER BY COALESCE(first_requested_at, requested_at) ASC, updated_at ASC, source ASC
		LIMIT ?`

	updateSQL := `UPDATE episode_rebuild_sources
		SET status = 'running',
			claimed_revision = desired_revision,
			claimed_generation_hash = ?,
			retry_count = CASE
				WHEN retry_revision IS NULL
				  OR retry_revision != desired_revision
				  OR COALESCE(retry_generation_hash, '') != ?
				THEN 0
				ELSE COALESCE(retry_count, 0)
			END,
			last_error = CASE
				WHEN retry_revision IS NULL
				  OR retry_revision != desired_revision
				  OR COALESCE(retry_generation_hash, '') != ?
				THEN NULL
				ELSE last_error
			END,
			next_attempt_at = CASE
				WHEN retry_revision IS NULL
				  OR retry_revision != desired_revision
				  OR COALESCE(retry_generation_hash, '') != ?
				THEN NULL
				ELSE next_attempt_at
			END,
			retry_revision = desired_revision,
			retry_generation_hash = ?,
			lease_token = ?,
			lease_until = ?,
			updated_at = ?
		WHERE source = ?
		  AND (lease_token IS NULL OR COALESCE(lease_until, 0) <= ?)`

	claims := make([]map[string]any, 0, safeLimit)
	err := s.episodeRunTx(true, conn, func(tx *Tx) error {
		rows, err := tx.Query(selectSQL, queryArgs...)
		if err != nil {
			return err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		sources := make([]string, 0, len(items))
		for _, item := range items {
			sources = append(sources, strings.TrimSpace(stringFromRow(item, "source")))
		}
		for _, source := range sources {
			leaseToken := episodeLeaseToken()
			result, err := tx.Exec(updateSQL,
				generation, generation, generation, generation, generation,
				leaseToken, leaseUntil, nowTs, source, nowTs,
			)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected <= 0 {
				continue
			}
			claimRows, err := tx.Query("SELECT "+episodeRebuildColumns+" FROM episode_rebuild_sources WHERE source = ?", source)
			if err != nil {
				return err
			}
			claimItems, err := scanRowsToMaps(claimRows)
			if err != nil {
				return err
			}
			if len(claimItems) > 0 {
				claims = append(claims, claimItems[0])
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// EpisodeLeaseRenewOptions 对齐 Python renew_episode_source_rebuild_lease 的关键字参数
type EpisodeLeaseRenewOptions struct {
	// Source 来源
	Source string
	// LeaseToken 当前租约令牌
	LeaseToken string
	// ClaimedRevision 领取到的期望版本
	ClaimedRevision int
	// GenerationHash 本轮代际哈希
	GenerationHash string
	// LeaseSeconds 续期时长，Python 侧必填
	LeaseSeconds float64
	// Now 指定当前时间，nil 表示取系统时间
	Now *float64
}

// RenewEpisodeSourceRebuildLease 仅当仍持有未过期且未被新 revision 取代的租约时续期
// 对齐 Python renew_episode_source_rebuild_lease
func (s *Store) RenewEpisodeSourceRebuildLease(opts EpisodeLeaseRenewOptions, conn *Conn) (bool, error) {
	token := normalizeEpisodeSource(opts.Source)
	lease := strings.TrimSpace(opts.LeaseToken)
	generation := strings.TrimSpace(opts.GenerationHash)
	if token == "" || lease == "" || generation == "" {
		return false, nil
	}
	revision := opts.ClaimedRevision
	if revision < 0 {
		revision = 0
	}
	nowTs := nowTimestamp()
	if opts.Now != nil {
		nowTs = *opts.Now
	}
	renewedUntil := nowTs + math.Max(1, opts.LeaseSeconds)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return false, err
	}
	result, err := c.Exec(`UPDATE episode_rebuild_sources
		SET lease_until = MAX(COALESCE(lease_until, 0), ?),
			updated_at = ?
		WHERE source = ?
		  AND lease_token = ?
		  AND claimed_revision = ?
		  AND claimed_generation_hash = ?
		  AND desired_revision = ?
		  AND COALESCE(lease_until, 0) > ?`,
		renewedUntil, nowTs, token, lease, revision, generation, revision, nowTs,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	return affected == 1, nil
}

// EpisodePublishOptions 对齐 Python publish_episode_source_rebuild 的关键字参数
type EpisodePublishOptions struct {
	// Source 来源
	Source string
	// LeaseToken 当前租约令牌
	LeaseToken string
	// ClaimedRevision 领取到的期望版本
	ClaimedRevision int
	// GenerationHash 本轮代际哈希
	GenerationHash string
	// EpisodesPayloads 该来源的完整 episode 快照
	EpisodesPayloads []map[string]any
	// EmptyGroupFingerprints 本轮确认为空的分组指纹
	EmptyGroupFingerprints []string
	// Now 指定当前时间，nil 表示取系统时间
	Now *float64
}

// PublishEpisodeSourceRebuild 仅在来源版本未变化时替换完整快照并完成任务
// 对齐 Python publish_episode_source_rebuild
func (s *Store) PublishEpisodeSourceRebuild(opts EpisodePublishOptions, conn *Conn) (map[string]any, error) {
	token := normalizeEpisodeSource(opts.Source)
	lease := strings.TrimSpace(opts.LeaseToken)
	generation := strings.TrimSpace(opts.GenerationHash)
	if token == "" || lease == "" || generation == "" {
		return nil, ErrEpisodePublishArgsEmpty
	}
	revision := opts.ClaimedRevision
	if revision < 0 {
		revision = 0
	}
	nowTs := nowTimestamp()
	if opts.Now != nil {
		nowTs = *opts.Now
	}

	var published map[string]any
	err := s.episodeRunTx(true, conn, func(tx *Tx) error {
		rows, err := tx.Query(`SELECT desired_revision, claimed_revision, claimed_generation_hash, lease_token, lease_until
			FROM episode_rebuild_sources
			WHERE source = ?`, token)
		if err != nil {
			return err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		var row map[string]any
		if len(items) > 0 {
			row = items[0]
		}
		claimMatches := false
		desiredRevision := 0
		if row != nil {
			leaseValue := strings.TrimSpace(stringFromRow(row, "lease_token"))
			claimedRevision := episodeRowInt(row, "claimed_revision")
			claimedGeneration := strings.TrimSpace(stringFromRow(row, "claimed_generation_hash"))
			leaseUntil := episodeRowFloat(row, "lease_until")
			claimMatches = leaseValue == lease &&
				claimedRevision == revision &&
				claimedGeneration == generation &&
				leaseUntil > nowTs
			desiredRevision = episodeRowInt(row, "desired_revision")
		}
		sourceUnchanged := claimMatches && desiredRevision == revision
		if !sourceUnchanged {
			if claimMatches {
				if _, err := tx.Exec(`UPDATE episode_rebuild_sources
					SET status = 'pending',
						claimed_revision = NULL,
						claimed_generation_hash = NULL,
						lease_token = NULL,
						lease_until = NULL,
						updated_at = ?
					WHERE source = ? AND lease_token = ?`, nowTs, token, lease); err != nil {
					return err
				}
			}
			published = map[string]any{
				"source":        token,
				"published":     false,
				"superseded":    claimMatches,
				"episode_count": 0,
			}
			return nil
		}

		replaceResult, err := s.ReplaceEpisodesForSource(token, opts.EpisodesPayloads, opts.EmptyGroupFingerprints, tx.Conn())
		if err != nil {
			return err
		}
		result, err := tx.Exec(`UPDATE episode_rebuild_sources
			SET status = 'done',
				built_revision = ?,
				built_generation_hash = ?,
				claimed_revision = NULL,
				claimed_generation_hash = NULL,
				dirty_start = NULL,
				dirty_end = NULL,
				first_requested_at = NULL,
				ready_at = NULL,
				lease_token = NULL,
				lease_until = NULL,
				retry_count = 0,
				retry_revision = NULL,
				retry_generation_hash = NULL,
				next_attempt_at = NULL,
				last_error = NULL,
				updated_at = ?
			WHERE source = ? AND lease_token = ?`,
			revision, generation, nowTs, token, lease,
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrEpisodePublishLeaseChanged
		}
		episodeCount := 0
		if raw, ok := replaceResult["episode_count"]; ok {
			if n, ok := episodeToInt(raw); ok {
				episodeCount = n
			}
		}
		published = map[string]any{
			"source":         token,
			"published":      true,
			"superseded":     false,
			"episode_count":  episodeCount,
			"built_revision": revision,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return published, nil
}

// EpisodeFailOptions 对齐 Python fail_episode_source_rebuild 的关键字参数
type EpisodeFailOptions struct {
	// Source 来源
	Source string
	// LeaseToken 当前租约令牌
	LeaseToken string
	// ClaimedRevision 领取到的期望版本
	ClaimedRevision int
	// Error 失败原因，落库前截断到 500 字符
	Error string
	// RetryBackoffSeconds 重试退避，零值取 Python 默认 5 秒
	RetryBackoffSeconds float64
	// Now 指定当前时间，nil 表示取系统时间
	Now *float64
}

// FailEpisodeSourceRebuild 释放失败租约；新版本已到达时直接回到 pending
// 对齐 Python fail_episode_source_rebuild
func (s *Store) FailEpisodeSourceRebuild(opts EpisodeFailOptions, conn *Conn) (bool, error) {
	token := normalizeEpisodeSource(opts.Source)
	lease := strings.TrimSpace(opts.LeaseToken)
	if token == "" || lease == "" {
		return false, nil
	}
	revision := opts.ClaimedRevision
	if revision < 0 {
		revision = 0
	}
	nowTs := nowTimestamp()
	if opts.Now != nil {
		nowTs = *opts.Now
	}
	backoff := opts.RetryBackoffSeconds
	if backoff == 0 {
		backoff = 5
	}
	nextAttemptAt := nowTs + math.Max(0, backoff)
	errorText := truncateRunes(strings.TrimSpace(opts.Error), 500)

	c, err := s.ResolveConn(conn)
	if err != nil {
		return false, err
	}
	result, err := c.Exec(`UPDATE episode_rebuild_sources
		SET status = CASE
				WHEN desired_revision > ? THEN 'pending'
				ELSE 'failed'
			END,
			retry_count = CASE
				WHEN desired_revision > ? THEN 0
				ELSE COALESCE(retry_count, 0) + 1
			END,
			last_error = CASE
				WHEN desired_revision > ? THEN NULL
				ELSE ?
			END,
			next_attempt_at = CASE
				WHEN desired_revision > ? THEN NULL
				ELSE ?
			END,
			claimed_revision = NULL,
			claimed_generation_hash = NULL,
			retry_revision = CASE
				WHEN desired_revision > ? THEN NULL
				ELSE retry_revision
			END,
			retry_generation_hash = CASE
				WHEN desired_revision > ? THEN NULL
				ELSE retry_generation_hash
			END,
			lease_token = NULL,
			lease_until = NULL,
			updated_at = ?
		WHERE source = ?
		  AND lease_token = ?
		  AND claimed_revision = ?
		  AND COALESCE(lease_until, 0) > ?`,
		revision, revision, revision, errorText, revision, nextAttemptAt,
		revision, revision, nowTs, token, lease, revision, nowTs,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := c.Commit(); err != nil {
		return false, err
	}
	return affected == 1, nil
}
