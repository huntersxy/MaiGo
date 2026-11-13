package storage

// A_memorix 关系图投影任务队列，对齐 Python core/storage/metadata_store.py L3005-L3340

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	// DefaultRelationGraphProjectionJobLimit 对齐 Python claim_relation_graph_projection_jobs 的 limit 默认值
	DefaultRelationGraphProjectionJobLimit = 1000
	// DefaultRelationGraphProjectionJobLeaseSeconds 对齐 Python lease_seconds 默认值
	DefaultRelationGraphProjectionJobLeaseSeconds = 60.0
	// relationGraphProjectionDefaultError 对齐 Python 失败记录的兜底文案
	relationGraphProjectionDefaultError = "关系图投影失败"
)

// ClaimRelationGraphProjectionJobs 领取一批关系图投影任务，过期租约可以被正式重试
func (s *Store) ClaimRelationGraphProjectionJobs(limit int, leaseSeconds float64, now *float64) ([]map[string]any, error) {
	if limit <= 0 {
		return nil, errors.New("关系图投影任务 limit 必须大于0")
	}
	if leaseSeconds <= 0.0 {
		return nil, errors.New("关系图投影任务 lease_seconds 必须大于0")
	}
	claimedAt := nowTimestamp()
	if now != nil {
		claimedAt = *now
	}
	leaseToken := strings.ReplaceAll(uuid.NewString(), "-", "")
	claimed := make([]map[string]any, 0)

	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		rows, err := c.Query(`
			SELECT relation_hash, subject, object, desired_active,
			       desired_lifecycle_revision, job_revision, status,
			       attempt_count, updated_at
			FROM relation_graph_projection_jobs
			WHERE status IN ('pending', 'failed')
			   OR (status = 'running' AND COALESCE(lease_expires_at, 0.0) <= ?)
			ORDER BY updated_at ASC, relation_hash ASC
			LIMIT ?
			`, claimedAt, limit)
		if err != nil {
			return err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		for _, row := range items {
			result, err := c.Exec(`
				UPDATE relation_graph_projection_jobs
				SET status = 'running',
				    attempt_count = attempt_count + 1,
				    lease_token = ?,
				    lease_expires_at = ?,
				    last_error = NULL,
				    updated_at = ?
				WHERE relation_hash = ?
				  AND job_revision = ?
				  AND (
				      status IN ('pending', 'failed')
				      OR (status = 'running' AND COALESCE(lease_expires_at, 0.0) <= ?)
				  )
				`, leaseToken, claimedAt+leaseSeconds, claimedAt,
				stringFromRow(row, "relation_hash"), intFromRow(row, "job_revision"), claimedAt)
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
			item := miscCopyMap(row)
			item["lease_token"] = leaseToken
			item["lease_expires_at"] = claimedAt + leaseSeconds
			item["attempt_count"] = intFromRow(row, "attempt_count") + 1
			item["status"] = "running"
			claimed = append(claimed, item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// CountClaimableRelationGraphProjectionJobs 返回调用时刻可领取任务数，为单轮聚合建立有界水位
func (s *Store) CountClaimableRelationGraphProjectionJobs(now *float64) (int, error) {
	timestamp := nowTimestamp()
	if now != nil {
		timestamp = *now
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return 0, err
	}
	var count int64
	err = c.QueryRow(`
		SELECT COUNT(*) AS count
		FROM relation_graph_projection_jobs
		WHERE status IN ('pending', 'failed')
		   OR (status = 'running' AND COALESCE(lease_expires_at, 0.0) <= ?)
		`, timestamp).Scan(&count)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return int(count), nil
}

// ResetRelationGraphProjectionLeases 独占图运行时启动时释放上次进程遗留的执行租约
func (s *Store) ResetRelationGraphProjectionLeases(now *float64) (int, error) {
	resetAt := nowTimestamp()
	if now != nil {
		resetAt = *now
	}
	reset := 0
	err := s.Transaction(true, func(tx *Tx) error {
		result, err := tx.Exec(`
			UPDATE relation_graph_projection_jobs
			SET status = 'pending', lease_token = NULL,
			    lease_expires_at = NULL, updated_at = ?
			WHERE status = 'running'
			`, resetAt)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			reset = int(affected)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return reset, nil
}

// AuthorizeRelationGraphProjectionJobs 按任务版本、租约和当前关系活跃态授权图投影副作用
func (s *Store) AuthorizeRelationGraphProjectionJobs(jobs []map[string]any) ([]map[string]any, error) {
	claims := make(map[string]map[string]any, len(jobs))
	for _, item := range jobs {
		token := strings.TrimSpace(stringFromRow(item, "relation_hash"))
		if token == "" {
			continue
		}
		claims[token] = item
	}
	if len(claims) == 0 {
		return []map[string]any{}, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	relationHashes := make([]string, 0, len(claims))
	for token := range claims {
		relationHashes = append(relationHashes, token)
	}
	sort.Strings(relationHashes)

	authorized := make([]map[string]any, 0, len(relationHashes))
	for _, chunk := range iterSQLBatches(relationHashes, 900) {
		query := fmt.Sprintf(`
			SELECT j.relation_hash, j.subject, j.object,
			       j.job_revision, j.status, j.lease_token,
			       r.hash AS current_hash,
			       r.subject AS current_subject, r.object AS current_object,
			       r.is_inactive AS current_is_inactive,
			       r.lifecycle_revision AS current_lifecycle_revision
			FROM relation_graph_projection_jobs j
			LEFT JOIN relations r ON r.hash = j.relation_hash
			WHERE j.relation_hash IN (%s)
			`, sqlPlaceholders(len(chunk)))
		rows, err := c.Query(query, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		items, err := scanRowsToMaps(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range items {
			relationHash := stringFromRow(row, "relation_hash")
			claim, ok := claims[relationHash]
			if !ok {
				continue
			}
			if stringFromRow(row, "status") != "running" ||
				stringFromRow(row, "lease_token") != stringFromRow(claim, "lease_token") ||
				intFromRow(row, "job_revision") != intFromRow(claim, "job_revision") {
				continue
			}
			currentExists := row["current_hash"] != nil
			item := miscCopyMap(claim)
			if currentExists {
				item["subject"] = stringFromRow(row, "current_subject")
				item["object"] = stringFromRow(row, "current_object")
			} else {
				item["subject"] = stringFromRow(row, "subject")
				item["object"] = stringFromRow(row, "object")
			}
			item["authoritative_active"] = currentExists && !miscBoolValue(row["current_is_inactive"])
			if currentExists {
				item["current_lifecycle_revision"] = intFromRow(row, "current_lifecycle_revision")
			} else {
				item["current_lifecycle_revision"] = nil
			}
			authorized = append(authorized, item)
		}
	}
	return authorized, nil
}

// CompleteRelationGraphProjectionJobs 图快照持久化后按租约、任务版本和当前权威态 CAS 完成
func (s *Store) CompleteRelationGraphProjectionJobs(jobs []map[string]any, completedAt *float64) (int, error) {
	finishedAt := nowTimestamp()
	if completedAt != nil {
		finishedAt = *completedAt
	}
	completed := 0
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		for _, item := range jobs {
			relationHash := strings.TrimSpace(stringFromRow(item, "relation_hash"))
			if relationHash == "" {
				continue
			}
			var inactive sql.NullInt64
			err := c.QueryRow("SELECT is_inactive FROM relations WHERE hash = ?", relationHash).Scan(&inactive)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			currentActive := err == nil && (!inactive.Valid || inactive.Int64 == 0)
			appliedActive := miscBoolValue(item["authoritative_active"])
			jobRevision := intFromRow(item, "job_revision")
			leaseToken := stringFromRow(item, "lease_token")
			if currentActive != appliedActive {
				_, err = c.Exec(`
					UPDATE relation_graph_projection_jobs
					SET status = 'pending', lease_token = NULL,
					    lease_expires_at = NULL, updated_at = ?
					WHERE relation_hash = ? AND job_revision = ?
					  AND status = 'running' AND lease_token = ?
					`, finishedAt, relationHash, jobRevision, leaseToken)
				if err != nil {
					return err
				}
				continue
			}
			result, err := c.Exec(`
				DELETE FROM relation_graph_projection_jobs
				WHERE relation_hash = ? AND job_revision = ?
				  AND status = 'running' AND lease_token = ?
				`, relationHash, jobRevision, leaseToken)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected > 0 {
				completed += int(affected)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return completed, nil
}

// FailRelationGraphProjectionJobs 记录当前租约执行失败；更高版本任务不会被旧失败覆盖
func (s *Store) FailRelationGraphProjectionJobs(jobs []map[string]any, lastError string, failedAt *float64) (int, error) {
	recordedAt := nowTimestamp()
	if failedAt != nil {
		recordedAt = *failedAt
	}
	message := lastError
	if message == "" {
		message = relationGraphProjectionDefaultError
	}
	message = truncateRunes(message, 500)
	failed := 0
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		for _, item := range jobs {
			result, err := c.Exec(`
				UPDATE relation_graph_projection_jobs
				SET status = 'failed', lease_token = NULL,
				    lease_expires_at = NULL, last_error = ?, updated_at = ?
				WHERE relation_hash = ? AND job_revision = ?
				  AND status = 'running' AND lease_token = ?
				`, message, recordedAt,
				stringFromRow(item, "relation_hash"), intFromRow(item, "job_revision"),
				stringFromRow(item, "lease_token"))
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected > 0 {
				failed += int(affected)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return failed, nil
}

// ReenqueueAuthoritativeRelationGraphProjectionJobs CAS 未完成时按当前 metadata 状态重新建立 durable intent
func (s *Store) ReenqueueAuthoritativeRelationGraphProjectionJobs(jobs []map[string]any, enqueuedAt *float64) (int, error) {
	fallbackByHash := make(map[string]map[string]any, len(jobs))
	order := make([]string, 0, len(jobs))
	for _, item := range jobs {
		token := strings.TrimSpace(stringFromRow(item, "relation_hash"))
		if token == "" {
			continue
		}
		if _, ok := fallbackByHash[token]; !ok {
			order = append(order, token)
		}
		fallbackByHash[token] = item
	}
	if len(fallbackByHash) == 0 {
		return 0, nil
	}
	timestamp := nowTimestamp()
	if enqueuedAt != nil {
		timestamp = *enqueuedAt
	}
	enqueued := 0
	err := s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		for _, relationHash := range order {
			fallback := fallbackByHash[relationHash]
			var subject, object sql.NullString
			var inactive, revision sql.NullInt64
			err := c.QueryRow(`
				SELECT subject, object, is_inactive, lifecycle_revision
				FROM relations
				WHERE hash = ?
				`, relationHash).Scan(&subject, &object, &inactive, &revision)
			var subjectValue, objectValue string
			var desiredActive int
			var lifecycleRevision int
			switch {
			case err == sql.ErrNoRows:
				subjectValue = strings.TrimSpace(stringFromRow(fallback, "subject"))
				objectValue = strings.TrimSpace(stringFromRow(fallback, "object"))
				desiredActive = 0
				lifecycleRevision = intFromRow(fallback, "current_lifecycle_revision")
				if lifecycleRevision == 0 {
					lifecycleRevision = intFromRow(fallback, "desired_lifecycle_revision")
				}
			case err != nil:
				return err
			default:
				subjectValue = strings.TrimSpace(subject.String)
				objectValue = strings.TrimSpace(object.String)
				if inactive.Valid && inactive.Int64 != 0 {
					desiredActive = 0
				} else {
					desiredActive = 1
				}
				lifecycleRevision = int(revision.Int64)
			}
			if subjectValue == "" || objectValue == "" {
				return fmt.Errorf("关系图投影任务缺少端点: %s", relationHash)
			}
			_, err = c.Exec(`
				INSERT INTO relation_graph_projection_jobs (
					relation_hash, subject, object, desired_active,
					desired_lifecycle_revision, job_revision, status,
					attempt_count, lease_token, lease_expires_at, last_error,
					created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, 1, 'pending', 0, NULL, NULL, NULL, ?, ?)
				ON CONFLICT(relation_hash) DO UPDATE SET
					subject = excluded.subject,
					object = excluded.object,
					desired_active = excluded.desired_active,
					desired_lifecycle_revision = excluded.desired_lifecycle_revision,
					job_revision = relation_graph_projection_jobs.job_revision + 1,
					status = 'pending',
					attempt_count = 0,
					lease_token = NULL,
					lease_expires_at = NULL,
					last_error = NULL,
					updated_at = excluded.updated_at
				`, relationHash, subjectValue, objectValue, desiredActive, lifecycleRevision,
				timestamp, timestamp)
			if err != nil {
				return err
			}
			enqueued++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return enqueued, nil
}
