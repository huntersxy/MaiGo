package storage

// A_memorix 图片描述补偿与任务生命周期，对齐 Python core/storage/metadata_image.py 的描述补偿/失效部分

// EnqueueImageDescriptionCompensation 入队描述补偿，对齐 Python enqueue_image_description_compensation
// 已完成的任务保持 done，其余重置为 pending 并清空租约
func (s *Store) EnqueueImageDescriptionCompensation(occurrenceID, descriptionHash string, conn *Conn) error {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return err
	}
	now := nowTimestamp()
	_, err = c.Exec(`
		INSERT INTO image_description_compensations(
			occurrence_id, description_hash, status, created_at, updated_at
		) VALUES (?, ?, 'pending', ?, ?)
		ON CONFLICT(occurrence_id, description_hash) DO UPDATE SET
			status=CASE
				WHEN image_description_compensations.status='done' THEN 'done'
				ELSE 'pending'
			END,
			lease_token='', lease_until=0, updated_at=excluded.updated_at
	`, occurrenceID, descriptionHash, now, now)
	return err
}

// UpsertPendingImageDescription 持久保存先于图片资产到达的模型描述，对齐 Python upsert_pending_image_description
func (s *Store) UpsertPendingImageDescription(contentHash, descriptionHash, text string, conn *Conn) error {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return err
	}
	now := nowTimestamp()
	_, err = c.Exec(`
		INSERT INTO image_pending_descriptions(
			content_hash, description_hash, text, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(content_hash, description_hash) DO UPDATE SET
			text=excluded.text,
			updated_at=excluded.updated_at
	`, contentHash, descriptionHash, text, now, now)
	return err
}

// ListPendingImageDescriptions 列出待绑定描述，对齐 Python list_pending_image_descriptions
func (s *Store) ListPendingImageDescriptions(contentHash string) ([]map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return imageQueryDicts(c, `
		SELECT * FROM image_pending_descriptions
		WHERE content_hash=? ORDER BY created_at ASC
	`, contentHash)
}

// DeletePendingImageDescription 删除待绑定描述，对齐 Python delete_pending_image_description
func (s *Store) DeletePendingImageDescription(contentHash, descriptionHash string, conn *Conn) (int, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	result, err := c.Exec(
		"DELETE FROM image_pending_descriptions WHERE content_hash=? AND description_hash=?",
		contentHash, descriptionHash)
	if err != nil {
		return 0, err
	}
	return imageAffected(result)
}

// RetryFailedImageJobs 显式重置失败任务，对齐 Python retry_failed_image_jobs
// 保留正在执行的租约和已完成任务，返回 {"embedding": n, "description": n}
func (s *Store) RetryFailedImageJobs() (map[string]int, error) {
	counts := map[string]int{}
	err := s.Transaction(true, func(tx *Tx) error {
		embedding, err := tx.Exec(
			"UPDATE image_embedding_jobs SET status='pending', attempt_count=0, "+
				"lease_token='', lease_until=0, last_error='', updated_at=? "+
				"WHERE status='failed' AND asset_id IN (SELECT asset_id FROM image_assets WHERE status='active')",
			nowTimestamp())
		if err != nil {
			return err
		}
		affected, err := imageAffected(embedding)
		if err != nil {
			return err
		}
		counts["embedding"] = affected

		description, err := tx.Exec(
			"UPDATE image_description_compensations SET status='pending', attempt_count=0, "+
				"lease_token='', lease_until=0, last_error='', updated_at=? "+
				"WHERE status='failed' AND occurrence_id IN "+
				"(SELECT occurrence_id FROM image_occurrences WHERE status='active')",
			nowTimestamp())
		if err != nil {
			return err
		}
		affected, err = imageAffected(description)
		if err != nil {
			return err
		}
		counts["description"] = affected
		return nil
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

// ClaimImageDescriptionCompensations 领取描述补偿任务，对齐 Python claim_image_description_compensations
// 只领取作用域为 chat 且带消息时间与标识的出现记录；回收已过期的 running 租约
// 返回的是更新前的 SELECT 快照（Python dict(row) 语义），租约与状态以传入 token 为准
func (s *Store) ClaimImageDescriptionCompensations(leaseToken string, leaseSeconds float64, maxAttempts, limit int) ([]map[string]any, error) {
	now := nowTimestamp()
	claimed := []map[string]any{}
	err := s.Transaction(true, func(tx *Tx) error {
		if _, err := tx.Exec(`
			UPDATE image_description_compensations
			SET status='pending', lease_token='', lease_until=0, updated_at=?
			WHERE status='running' AND lease_until < ?
		`, now, now); err != nil {
			return err
		}
		rows, err := imageQueryDicts(tx, `
			SELECT compensation.*, occurrence.chat_id, occurrence.message_id, occurrence.occurred_at
			FROM image_description_compensations AS compensation
			JOIN image_occurrences AS occurrence
			  ON occurrence.occurrence_id=compensation.occurrence_id
			WHERE compensation.status IN ('pending', 'failed')
			  AND compensation.attempt_count < ?
			  AND occurrence.status='active'
			  AND occurrence.scope_type='chat'
			  AND occurrence.chat_id != '' AND occurrence.message_id != ''
			  AND occurrence.occurred_at IS NOT NULL
			ORDER BY compensation.created_at ASC
			LIMIT ?
		`, imageAtLeastOne(maxAttempts), imageAtLeastOne(limit))
		if err != nil {
			return err
		}
		leaseUntil := imageLeaseDeadline(now, leaseSeconds)
		for _, row := range rows {
			result, err := tx.Exec(`
				UPDATE image_description_compensations
				SET status='running', lease_token=?, lease_until=?,
					attempt_count=attempt_count+1, updated_at=?
				WHERE occurrence_id=? AND description_hash=?
				  AND status IN ('pending', 'failed')
			`, leaseToken, leaseUntil, now,
				stringFromRow(row, "occurrence_id"), stringFromRow(row, "description_hash"))
			if err != nil {
				return err
			}
			affected, err := imageAffected(result)
			if err != nil {
				return err
			}
			if affected == 1 {
				claimed = append(claimed, row)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// CompleteImageDescriptionCompensation 结束描述补偿任务，对齐 Python complete_image_description_compensation
func (s *Store) CompleteImageDescriptionCompensation(occurrenceID, descriptionHash, leaseToken string, success bool, errText string) (bool, error) {
	status := "failed"
	if success {
		status = "done"
	}
	completed := false
	err := s.Transaction(true, func(tx *Tx) error {
		result, err := tx.Exec(`
			UPDATE image_description_compensations
			SET status=?, lease_token='', lease_until=0, last_error=?, updated_at=?
			WHERE occurrence_id=? AND description_hash=? AND lease_token=? AND status='running'
		`, status, truncateRunes(errText, 1000), nowTimestamp(), occurrenceID, descriptionHash, leaseToken)
		if err != nil {
			return err
		}
		affected, err := imageAffected(result)
		if err != nil {
			return err
		}
		completed = affected == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return completed, nil
}

// DeactivateImageOccurrence 失效出现记录并中止其描述补偿，对齐 Python deactivate_image_occurrence
func (s *Store) DeactivateImageOccurrence(occurrenceID string, conn *Conn) (int, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return 0, err
	}
	result, err := c.Exec(
		"UPDATE image_occurrences SET status='deleted', updated_at=? WHERE occurrence_id=? AND status='active'",
		nowTimestamp(), occurrenceID)
	if err != nil {
		return 0, err
	}
	affected, err := imageAffected(result)
	if err != nil {
		return 0, err
	}
	if _, err := c.Exec(`
		UPDATE image_description_compensations
		SET status='aborted', lease_token='', lease_until=0, updated_at=?
		WHERE occurrence_id=? AND status IN ('pending', 'failed', 'running')
	`, nowTimestamp(), occurrenceID); err != nil {
		return 0, err
	}
	return affected, nil
}

// DeactivateInstallationImageOccurrences 失效某安装的全部出现记录，对齐 Python deactivate_installation_image_occurrences
// 返回涉及资产去重后的列表（保持首次出现顺序）；conn 为 nil 表示主连接
func (s *Store) DeactivateInstallationImageOccurrences(installationID string, conn *Conn) ([]string, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := imageQueryDicts(c,
		"SELECT occurrence_id, asset_id FROM image_occurrences WHERE installation_id=? AND status='active'",
		installationID)
	if err != nil {
		return nil, err
	}
	assetIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, err := s.DeactivateImageOccurrence(stringFromRow(row, "occurrence_id"), c); err != nil {
			return nil, err
		}
		assetIDs = append(assetIDs, stringFromRow(row, "asset_id"))
	}
	return dedupeStrings(assetIDs), nil
}

// AbortImageJobsIfUnreferenced 资产无引用时中止任务并失效资产与向量
// 对齐 Python abort_image_jobs_if_unreferenced
func (s *Store) AbortImageJobsIfUnreferenced(assetID string, conn *Conn) (bool, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return false, err
	}
	referenced, err := imageExists(c,
		"SELECT 1 FROM image_occurrences WHERE asset_id=? AND status='active' LIMIT 1", assetID)
	if err != nil {
		return false, err
	}
	if referenced {
		return false, nil
	}
	if _, err := c.Exec(`
		UPDATE image_embedding_jobs
		SET status='aborted', lease_token='', lease_until=0,
			last_error='asset unreferenced', updated_at=?
		WHERE asset_id=? AND status IN ('pending', 'failed', 'running')
	`, nowTimestamp(), assetID); err != nil {
		return false, err
	}
	if _, err := c.Exec("UPDATE image_assets SET status='deleted', updated_at=? WHERE asset_id=?",
		nowTimestamp(), assetID); err != nil {
		return false, err
	}
	// 文件与向量即将释放，必须同时失效索引凭证，避免同图重新出现时误判为已索引
	if _, err := c.Exec("UPDATE image_embeddings SET status='deleted', updated_at=? WHERE asset_id=?",
		nowTimestamp(), assetID); err != nil {
		return false, err
	}
	return true, nil
}
