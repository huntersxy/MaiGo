package storage

// A_memorix 图片向量状态与嵌入任务，对齐 Python core/storage/metadata_image.py 的向量/任务查询部分

import "fmt"

// ImageJobList 对齐 Python list_image_jobs 返回的字典
type ImageJobList struct {
	// Success 固定 true，与 Python 返回一致
	Success bool `json:"success"`
	// Items 嵌入与描述补偿任务条目
	Items []map[string]any `json:"items"`
	// Total 过滤条件下的总数
	Total int `json:"total"`
}

// ImageRecordsExport 对齐 Python export_image_records 返回的字典
type ImageRecordsExport struct {
	// Assets 涉及的图片资产
	Assets []map[string]any `json:"assets"`
	// Occurrences 有效的出现记录
	Occurrences []map[string]any `json:"occurrences"`
	// Observations 未被取代的认知
	Observations []map[string]any `json:"observations"`
	// Links 有效记忆关联
	Links []map[string]any `json:"links"`
}

// GetImageRuntimeState 读取图片向量运行时状态，缺失返回空字典，对齐 Python get_image_runtime_state
func (s *Store) GetImageRuntimeState() (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	row, err := imageFirstDict(c, "SELECT * FROM image_runtime_state WHERE singleton_id = 1")
	if err != nil {
		return nil, err
	}
	if row == nil {
		return map[string]any{}, nil
	}
	return row, nil
}

// SetImageRuntimeState 写入图片向量运行时状态，对齐 Python set_image_runtime_state
func (s *Store) SetImageRuntimeState(generation int64, fingerprintHash string, dimension int64, status string, conn *Conn) error {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return err
	}
	_, err = c.Exec(`
		INSERT INTO image_runtime_state(singleton_id, generation, fingerprint_hash, dimension, status, updated_at)
		VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT(singleton_id) DO UPDATE SET
			generation=excluded.generation,
			fingerprint_hash=excluded.fingerprint_hash,
			dimension=excluded.dimension,
			status=excluded.status,
			updated_at=excluded.updated_at
	`, generation, fingerprintHash, dimension, status, nowTimestamp())
	return err
}

// GetImageEmbedding 读取图片向量登记，对齐 Python get_image_embedding
func (s *Store) GetImageEmbedding(assetID, fingerprintHash string) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	return imageFirstDict(c, "SELECT * FROM image_embeddings WHERE asset_id=? AND fingerprint_hash=?",
		assetID, fingerprintHash)
}

// EnqueueImageEmbeddingJob 入队图片嵌入任务，对齐 Python enqueue_image_embedding_job
// 仅当历史任务为 done/aborted 且尚无 ready 向量时才重置，其他状态保持原样
func (s *Store) EnqueueImageEmbeddingJob(assetID, fingerprintHash string, generation int64, conn *Conn) (map[string]any, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	if _, err := c.Exec(`
		INSERT INTO image_embedding_jobs(
			job_id, asset_id, fingerprint_hash, generation, status, lease_token,
			lease_until, attempt_count, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'pending', '', 0, 0, '', ?, ?)
		ON CONFLICT(asset_id, fingerprint_hash, generation) DO UPDATE SET
			status='pending', lease_token='', lease_until=0, attempt_count=0,
			last_error='', updated_at=excluded.updated_at
		WHERE image_embedding_jobs.status IN ('done', 'aborted')
		  AND NOT EXISTS (
			  SELECT 1 FROM image_embeddings e
			  WHERE e.asset_id=excluded.asset_id AND e.fingerprint_hash=excluded.fingerprint_hash
				AND e.status='ready'
		  )
	`, imageUUIDHex(), assetID, fingerprintHash, generation, now, now); err != nil {
		return nil, err
	}
	row, err := imageFirstDict(c,
		"SELECT * FROM image_embedding_jobs WHERE asset_id=? AND fingerprint_hash=? AND generation=?",
		assetID, fingerprintHash, generation)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("图片嵌入任务写入后无法读取")
	}
	return row, nil
}

// EnqueueUnindexedImageAssets 为尚未建立向量的活跃资产入队，对齐 Python enqueue_unindexed_image_assets
func (s *Store) EnqueueUnindexedImageAssets(fingerprintHash string, generation int64, limit int) (int, error) {
	count := 0
	err := s.Transaction(true, func(tx *Tx) error {
		rows, err := imageQueryDicts(tx, `
			SELECT asset.asset_id
			FROM image_assets AS asset
			WHERE asset.status='active'
			  AND EXISTS(
				  SELECT 1 FROM image_occurrences AS occurrence
				  WHERE occurrence.asset_id=asset.asset_id AND occurrence.status='active'
			  )
			  AND NOT EXISTS(
				  SELECT 1 FROM image_embeddings AS embedding
				  WHERE embedding.asset_id=asset.asset_id
					AND embedding.fingerprint_hash=? AND embedding.status='ready'
			  )
			LIMIT ?
		`, fingerprintHash, imageAtLeastOne(limit))
		if err != nil {
			return err
		}
		for _, row := range rows {
			if _, err := s.EnqueueImageEmbeddingJob(
				stringFromRow(row, "asset_id"), fingerprintHash, generation, tx.Conn()); err != nil {
				return err
			}
		}
		count = len(rows)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// ClaimImageEmbeddingJobs 领取嵌入任务，对齐 Python claim_image_embedding_jobs
func (s *Store) ClaimImageEmbeddingJobs(leaseToken string, leaseSeconds float64, maxAttempts, limit int) ([]map[string]any, error) {
	now := nowTimestamp()
	leaseUntil := imageLeaseDeadline(now, leaseSeconds)
	claimed := []map[string]any{}
	err := s.Transaction(true, func(tx *Tx) error {
		rows, err := imageQueryDicts(tx, `
			SELECT * FROM image_embedding_jobs
			WHERE attempt_count < ?
			  AND (
				status IN ('pending', 'failed')
				OR (status='running' AND lease_until <= ?)
			  )
			ORDER BY created_at ASC LIMIT ?
		`, imageAtLeastOne(maxAttempts), now, imageAtLeastOne(limit))
		if err != nil {
			return err
		}
		for _, row := range rows {
			jobID := stringFromRow(row, "job_id")
			result, err := tx.Exec(`
				UPDATE image_embedding_jobs
				SET status='running', lease_token=?, lease_until=?,
					attempt_count=attempt_count+1, updated_at=?
				WHERE job_id=? AND (
					status IN ('pending', 'failed')
					OR (status='running' AND lease_until <= ?)
				)
			`, leaseToken, leaseUntil, now, jobID, now)
			if err != nil {
				return err
			}
			affected, err := imageAffected(result)
			if err != nil {
				return err
			}
			if affected != 1 {
				continue
			}
			loaded, err := imageFirstDict(tx, "SELECT * FROM image_embedding_jobs WHERE job_id=?", jobID)
			if err != nil {
				return err
			}
			if loaded != nil {
				claimed = append(claimed, loaded)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// ImageEmbeddingJobIsPublishable 判断任务是否可发布，对齐 Python image_embedding_job_is_publishable
func (s *Store) ImageEmbeddingJobIsPublishable(jobID, leaseToken string, generation int64, assetID string) (bool, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return false, err
	}
	return imageExists(c, `
		SELECT 1
		FROM image_embedding_jobs AS job
		JOIN image_assets AS asset ON asset.asset_id=job.asset_id
		WHERE job.job_id=? AND job.lease_token=? AND job.status='running'
		  AND job.generation=? AND job.asset_id=? AND asset.status='active'
		  AND EXISTS(
			  SELECT 1 FROM image_occurrences AS occurrence
			  WHERE occurrence.asset_id=asset.asset_id AND occurrence.status='active'
		  )
	`, jobID, leaseToken, generation, assetID)
}

// PublishImageEmbedding 发布图片向量并结束任务，对齐 Python publish_image_embedding
func (s *Store) PublishImageEmbedding(jobID, leaseToken, assetID, fingerprintHash, vectorID string, generation, dimension int64) (bool, error) {
	published := false
	err := s.Transaction(true, func(tx *Tx) error {
		ready, err := imageExists(tx, `
			SELECT 1 FROM image_embedding_jobs AS job
			JOIN image_assets AS asset ON asset.asset_id=job.asset_id
			WHERE job.job_id=? AND job.lease_token=? AND job.status='running'
			  AND job.asset_id=? AND job.fingerprint_hash=? AND job.generation=?
			  AND asset.status='active'
			  AND EXISTS(
				  SELECT 1 FROM image_occurrences AS occurrence
				  WHERE occurrence.asset_id=asset.asset_id AND occurrence.status='active'
			  )
		`, jobID, leaseToken, assetID, fingerprintHash, generation)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
		now := nowTimestamp()
		if _, err := tx.Exec(`
			INSERT INTO image_embeddings(
				asset_id, fingerprint_hash, vector_id, generation, dimension, status, updated_at
			) VALUES (?, ?, ?, ?, ?, 'ready', ?)
			ON CONFLICT(asset_id, fingerprint_hash) DO UPDATE SET
				vector_id=excluded.vector_id,
				generation=excluded.generation,
				dimension=excluded.dimension,
				status='ready',
				updated_at=excluded.updated_at
		`, assetID, fingerprintHash, vectorID, generation, dimension, now); err != nil {
			return err
		}
		result, err := tx.Exec(`
			UPDATE image_embedding_jobs
			SET status='done', lease_token='', lease_until=0, last_error='', updated_at=?
			WHERE job_id=? AND lease_token=? AND status='running'
		`, now, jobID, leaseToken)
		if err != nil {
			return err
		}
		affected, err := imageAffected(result)
		if err != nil {
			return err
		}
		published = affected == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return published, nil
}

// RecordImportedImageEmbedding 登记包内导入的可信向量，对齐 Python record_imported_image_embedding
// 向量已写入内存索引后调用；资产未激活或无出现记录时报 Python 同文案错误
func (s *Store) RecordImportedImageEmbedding(assetID, fingerprintHash, vectorID string, generation, dimension int64, conn *Conn) error {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return err
	}
	active, err := imageExists(c, `
		SELECT 1 FROM image_assets AS asset
		WHERE asset.asset_id=? AND asset.status='active'
		  AND EXISTS(
			  SELECT 1 FROM image_occurrences AS occurrence
			  WHERE occurrence.asset_id=asset.asset_id AND occurrence.status='active'
		  )
	`, assetID)
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("图片向量引用了未激活或无出现记录的资产")
	}
	now := nowTimestamp()
	if _, err := c.Exec(`
		INSERT INTO image_embeddings(
			asset_id, fingerprint_hash, vector_id, generation, dimension, status, updated_at
		) VALUES (?, ?, ?, ?, ?, 'ready', ?)
		ON CONFLICT(asset_id, fingerprint_hash) DO UPDATE SET
			vector_id=excluded.vector_id,
			generation=excluded.generation,
			dimension=excluded.dimension,
			status='ready',
			updated_at=excluded.updated_at
	`, assetID, fingerprintHash, vectorID, generation, dimension, now); err != nil {
		return err
	}
	_, err = c.Exec(`
		UPDATE image_embedding_jobs
		SET status='done', lease_token='', lease_until=0, last_error='', updated_at=?
		WHERE asset_id=? AND fingerprint_hash=? AND generation=?
		  AND status IN ('pending', 'failed', 'running')
	`, now, assetID, fingerprintHash, generation)
	return err
}

// FailImageEmbeddingJob 标记任务失败，对齐 Python fail_image_embedding_job
func (s *Store) FailImageEmbeddingJob(jobID, leaseToken, errText string) (bool, error) {
	failed := false
	err := s.Transaction(true, func(tx *Tx) error {
		result, err := tx.Exec(`
			UPDATE image_embedding_jobs
			SET status='failed', lease_token='', lease_until=0, last_error=?, updated_at=?
			WHERE job_id=? AND lease_token=? AND status='running'
		`, truncateRunes(errText, 1000), nowTimestamp(), jobID, leaseToken)
		if err != nil {
			return err
		}
		affected, err := imageAffected(result)
		if err != nil {
			return err
		}
		failed = affected == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return failed, nil
}

// ListImageJobs 统一展示嵌入与描述补偿任务，对齐 Python list_image_jobs
func (s *Store) ListImageJobs(limit, offset int, status string) (ImageJobList, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return ImageJobList{}, err
	}
	query := "SELECT 'embedding' AS kind, job_id AS id, asset_id, status, attempt_count, last_error, " +
		"updated_at, lease_until FROM image_embedding_jobs UNION ALL " +
		"SELECT 'description' AS kind, occurrence_id AS id, '' AS asset_id, status, attempt_count, " +
		"last_error, updated_at, lease_until FROM image_description_compensations"
	condition := ""
	params := make([]any, 0, 1)
	if status != "" {
		condition = " WHERE status=?"
		params = append(params, status)
	}
	total, err := countRows(c, fmt.Sprintf("SELECT COUNT(*) FROM (%s)%s", query, condition), params...)
	if err != nil {
		return ImageJobList{}, err
	}
	listArgs := make([]any, 0, len(params)+2)
	listArgs = append(listArgs, params...)
	listArgs = append(listArgs, limit, offset)
	items, err := imageQueryDicts(c,
		fmt.Sprintf("SELECT * FROM (%s)%s ORDER BY updated_at DESC LIMIT ? OFFSET ?", query, condition),
		listArgs...)
	if err != nil {
		return ImageJobList{}, err
	}
	return ImageJobList{Success: true, Items: items, Total: total}, nil
}

// ExportImageRecords 导出出现记录及其资产/认知/关联，对齐 Python export_image_records
func (s *Store) ExportImageRecords(occurrenceIDs []string) (ImageRecordsExport, error) {
	exported := ImageRecordsExport{
		Assets:       []map[string]any{},
		Occurrences:  []map[string]any{},
		Observations: []map[string]any{},
		Links:        []map[string]any{},
	}
	tokens := dedupeStrings(imageTokenList(occurrenceIDs))
	if len(tokens) == 0 {
		return exported, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return ImageRecordsExport{}, err
	}
	occurrences, err := imageQueryDicts(c, fmt.Sprintf(
		"SELECT * FROM image_occurrences WHERE status='active' AND occurrence_id IN (%s)",
		sqlPlaceholders(len(tokens))), stringArgs(tokens)...)
	if err != nil {
		return ImageRecordsExport{}, err
	}
	activeIDs := make([]string, 0, len(occurrences))
	assetIDs := make([]string, 0, len(occurrences))
	for _, row := range occurrences {
		activeIDs = append(activeIDs, stringFromRow(row, "occurrence_id"))
		assetIDs = append(assetIDs, stringFromRow(row, "asset_id"))
	}
	assets := []map[string]any{}
	if uniqueAssets := dedupeStrings(assetIDs); len(uniqueAssets) > 0 {
		assets, err = imageQueryDicts(c, fmt.Sprintf(
			"SELECT * FROM image_assets WHERE status='active' AND asset_id IN (%s)",
			sqlPlaceholders(len(uniqueAssets))), stringArgs(uniqueAssets)...)
		if err != nil {
			return ImageRecordsExport{}, err
		}
	}
	observations, err := s.ListImageObservations(activeIDs)
	if err != nil {
		return ImageRecordsExport{}, err
	}
	links, err := s.ListImageMemoryLinks(activeIDs)
	if err != nil {
		return ImageRecordsExport{}, err
	}
	exported.Assets = assets
	exported.Occurrences = occurrences
	exported.Observations = observations
	exported.Links = links
	return exported, nil
}
