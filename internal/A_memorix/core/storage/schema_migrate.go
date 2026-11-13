package storage

// A_memorix 历史库 schema 迁移，逐条对齐 Python metadata_schema.py 的 _migrate_schema
// 历史库缺少的表/列逐项补齐；破坏性语句失败只跳过，结构性补齐失败要暴露

// migrateSchema 执行数据库 schema 迁移，对齐 Python _migrate_schema
func (s *Store) migrateSchema(conn *Conn) error {
	if err := ensurePersonProfileSnapshotColumns(conn); err != nil {
		return err
	}
	if err := ensurePersonProfileAliasOverrideTable(conn); err != nil {
		return err
	}
	if err := ensurePersonIdentityTables(conn); err != nil {
		return err
	}

	// vNext 关键表兜底：历史库可能缺失，需在迁移阶段主动补齐
	if err := execAll(conn, ddlRelationHashAliases, ddlSchemaMigrations); err != nil {
		return err
	}

	// Episode MVP 表结构补齐
	if err := ensureEpisodeEmptyGroupTable(conn); err != nil {
		return err
	}
	if err := execAll(conn, ddlEpisodes); err != nil {
		return err
	}
	if err := ensureEpisodeColumns(conn); err != nil {
		return err
	}
	if err := execAll(conn, ddlEpisodeParagraphs, ddlEpisodeRebuildSources); err != nil {
		return err
	}
	if err := execAll(conn,
		ddlIdxEpisodesSourceTimeEnd,
		ddlIdxEpisodesUpdatedAt,
		ddlIdxEpisodeParagraphsParagraph,
		ddlIdxEpisodeRebuildStatusUpdated,
		ddlIdxEpisodeRebuildUpdatedAt,
	); err != nil {
		return err
	}
	if err := ensureEpisodeSourceRevisionColumns(conn); err != nil {
		return err
	}
	if err := execAll(conn, ddlParagraphVectorBackfill, ddlIdxParagraphVectorBackfillStatusUpdated); err != nil {
		return err
	}

	// 反馈任务、陈旧关系标记与外部引用
	if err := execAll(conn,
		ddlMemoryFeedbackTasks,
		ddlIdxMemoryFeedbackTasksStatusDue,
		ddlIdxMemoryFeedbackTasksSessionQuery,
		ddlMemoryFeedbackActionLogs,
		ddlIdxMemoryFeedbackActionLogsTask,
		ddlIdxMemoryFeedbackActionLogsQuery,
		ddlIdxMemoryFeedbackActionLogsTarget,
		ddlParagraphStaleRelationMarks,
		ddlIdxParagraphStaleRelationMarksParagraph,
		ddlIdxParagraphStaleRelationMarksRelation,
		ddlIdxParagraphStaleRelationMarksUpdated,
	); err != nil {
		return err
	}
	if err := ensureParagraphStaleRelationMarkColumns(conn); err != nil {
		return err
	}
	if err := execAll(conn,
		ddlIdxParagraphStaleRelationMarksSource,
		ddlPersonProfileRefreshQueue,
		ddlIdxPersonProfileRefreshQueueStatusUpdated,
		ddlIdxPersonProfileRefreshQueueRequested,
	); err != nil {
		return err
	}
	if err := ensureMemoryFeedbackTaskColumns(conn); err != nil {
		return err
	}
	if err := execAll(conn,
		ddlExternalMemoryRefs,
		ddlIdxExternalMemoryRefsParagraph,
		ddlMemoryV5Operations,
		ddlIdxMemoryV5OperationsCreated,
		ddlDeleteOperations,
		ddlIdxDeleteOperationsCreated,
		ddlIdxDeleteOperationsMode,
		ddlDeleteOperationItems,
		ddlIdxDeleteOperationItemsOperation,
		ddlIdxDeleteOperationItemsHash,
	); err != nil {
		return err
	}

	// 版本化扩展表
	if err := ensureStorageCleanupTables(conn); err != nil {
		return err
	}
	if err := ensureFactTables(conn); err != nil {
		return err
	}
	if err := ensureLifecycleColumns(conn); err != nil {
		return err
	}
	if err := ensureRelationGraphProjectionTables(conn); err != nil {
		return err
	}
	if err := ensureExternalMemoryRefsForeignKey(conn); err != nil {
		return err
	}
	if err := ensureFuzzyModifyPlanTables(conn); err != nil {
		return err
	}
	if err := ensureKnowledgePackageTables(conn); err != nil {
		return err
	}
	if err := ensureImageMemoryTables(conn); err != nil {
		return err
	}
	if err := ensureSummaryCheckpointTables(conn); err != nil {
		return err
	}

	return s.migrateLegacyColumns(conn)
}

// migrateLegacyColumns 迁移历史库的段落/关系/实体列与列错位数据
// 对齐 Python _migrate_schema 中逐段 try/except 的兼容迁移，失败只跳过不中断
func (s *Store) migrateLegacyColumns(conn *Conn) error {
	// knowledge_type 列
	paragraphColumns, err := tableColumnSet(conn, "paragraphs")
	if err != nil {
		return err
	}
	if _, ok := paragraphColumns["knowledge_type"]; !ok {
		if _, execErr := conn.Exec("ALTER TABLE paragraphs ADD COLUMN knowledge_type TEXT DEFAULT 'mixed'"); execErr == nil {
			if err := conn.Commit(); err != nil {
				return err
			}
		}
	}

	// 时序字段迁移
	paragraphColumns, err = tableColumnSet(conn, "paragraphs")
	if err != nil {
		return err
	}
	for _, migration := range [][2]string{
		{"event_time", "ALTER TABLE paragraphs ADD COLUMN event_time REAL"},
		{"event_time_start", "ALTER TABLE paragraphs ADD COLUMN event_time_start REAL"},
		{"event_time_end", "ALTER TABLE paragraphs ADD COLUMN event_time_end REAL"},
		{"time_granularity", "ALTER TABLE paragraphs ADD COLUMN time_granularity TEXT"},
		{"time_confidence", "ALTER TABLE paragraphs ADD COLUMN time_confidence REAL DEFAULT 1.0"},
	} {
		if _, ok := paragraphColumns[migration[0]]; ok {
			continue
		}
		_, _ = conn.Exec(migration[1])
	}
	// 时序索引仅在列存在时创建，兼容旧库迁移
	if err := s.createTemporalIndexesIfReady(conn); err != nil {
		return err
	}
	if err := conn.Commit(); err != nil {
		return err
	}

	// 记忆动态字段
	paragraphColumns, err = tableColumnSet(conn, "paragraphs")
	if err != nil {
		return err
	}
	if _, ok := paragraphColumns["is_permanent"]; !ok {
		if execErr := execAll(conn,
			"ALTER TABLE paragraphs ADD COLUMN is_permanent BOOLEAN DEFAULT 0",
			"ALTER TABLE paragraphs ADD COLUMN last_accessed REAL",
			"ALTER TABLE paragraphs ADD COLUMN access_count INTEGER DEFAULT 0",
			"ALTER TABLE relations ADD COLUMN is_permanent BOOLEAN DEFAULT 0",
			"ALTER TABLE relations ADD COLUMN last_accessed REAL",
			"ALTER TABLE relations ADD COLUMN access_count INTEGER DEFAULT 0",
		); execErr == nil {
			if err := conn.Commit(); err != nil {
				return err
			}
		}
	}

	// V5 记忆动态字段与回收站表
	relationColumns, err := tableColumnSet(conn, "relations")
	if err != nil {
		return err
	}
	if _, ok := relationColumns["is_inactive"]; !ok {
		if execErr := execAll(conn,
			"ALTER TABLE relations ADD COLUMN is_inactive BOOLEAN DEFAULT 0",
			"ALTER TABLE relations ADD COLUMN inactive_since REAL",
			"ALTER TABLE relations ADD COLUMN is_pinned BOOLEAN DEFAULT 0",
			"ALTER TABLE relations ADD COLUMN protected_until REAL",
			"ALTER TABLE relations ADD COLUMN last_reinforced REAL",
			ddlDeletedRelationsLegacyV5,
		); execErr == nil {
			if err := conn.Commit(); err != nil {
				return err
			}
		}
	}

	// 关系向量状态字段迁移
	relationColumns, err = tableColumnSet(conn, "relations")
	if err != nil {
		return err
	}
	for _, migration := range [][2]string{
		{"vector_state", "ALTER TABLE relations ADD COLUMN vector_state TEXT DEFAULT 'none'"},
		{"vector_updated_at", "ALTER TABLE relations ADD COLUMN vector_updated_at REAL"},
		{"vector_error", "ALTER TABLE relations ADD COLUMN vector_error TEXT"},
		{"vector_retry_count", "ALTER TABLE relations ADD COLUMN vector_retry_count INTEGER DEFAULT 0"},
	} {
		if _, ok := relationColumns[migration[0]]; ok {
			continue
		}
		_, _ = conn.Exec(migration[1])
	}

	// 回收站同步字段迁移（用于 restore 保留向量状态）
	deletedRelationColumns, err := tableColumnSet(conn, "deleted_relations")
	if err != nil {
		return err
	}
	for _, migration := range [][2]string{
		{"vector_state", "ALTER TABLE deleted_relations ADD COLUMN vector_state TEXT DEFAULT 'none'"},
		{"vector_updated_at", "ALTER TABLE deleted_relations ADD COLUMN vector_updated_at REAL"},
		{"vector_error", "ALTER TABLE deleted_relations ADD COLUMN vector_error TEXT"},
		{"vector_retry_count", "ALTER TABLE deleted_relations ADD COLUMN vector_retry_count INTEGER DEFAULT 0"},
	} {
		if _, ok := deletedRelationColumns[migration[0]]; ok {
			continue
		}
		_, _ = conn.Exec(migration[1])
	}

	// 软删除字段
	entityColumns, err := tableColumnSet(conn, "entities")
	if err != nil {
		return err
	}
	if _, ok := entityColumns["is_deleted"]; !ok {
		if execErr := execAll(conn,
			"ALTER TABLE entities ADD COLUMN is_deleted INTEGER DEFAULT 0",
			"ALTER TABLE entities ADD COLUMN deleted_at REAL",
			"ALTER TABLE paragraphs ADD COLUMN is_deleted INTEGER DEFAULT 0",
			"ALTER TABLE paragraphs ADD COLUMN deleted_at REAL",
		); execErr == nil {
			if err := conn.Commit(); err != nil {
				return err
			}
		}
	}

	// 数据修复：source/vector_index 列错位（文件名误存入 vector_index）
	var misplaced int
	if err := conn.QueryRow(migrationColumnSwapCountSQL).Scan(&misplaced); err == nil && misplaced > 0 {
		if _, execErr := conn.Exec(migrationColumnSwapFixSQL); execErr == nil {
			if err := conn.Commit(); err != nil {
				return err
			}
		}
	}

	// 升级前的图快照不能作为活跃态事实源，迁移完成时为全部关系建立最终状态任务
	if err := seedRelationGraphProjectionJobs(conn); err != nil {
		return err
	}
	if err := s.createPerformanceIndexes(conn); err != nil {
		return err
	}
	return conn.Commit()
}

// createTemporalIndexesIfReady 仅当时序列已存在时创建索引
func (s *Store) createTemporalIndexesIfReady(conn *Conn) error {
	columns, err := tableColumnSet(conn, "paragraphs")
	if err != nil {
		return err
	}
	statements := make([]string, 0, 3)
	if _, ok := columns["event_time"]; ok {
		statements = append(statements, ddlIdxParagraphsEventTime)
	}
	if _, ok := columns["event_time_start"]; ok {
		statements = append(statements, ddlIdxParagraphsEventStart)
	}
	if _, ok := columns["event_time_end"]; ok {
		statements = append(statements, ddlIdxParagraphsEventEnd)
	}
	return execAll(conn, statements...)
}

// createPerformanceIndexes 创建热点查询使用的补充索引
func (s *Store) createPerformanceIndexes(conn *Conn) error {
	paragraphColumns, err := tableColumnSet(conn, "paragraphs")
	if err != nil {
		return err
	}
	relationColumns, err := tableColumnSet(conn, "relations")
	if err != nil {
		return err
	}

	if err := execAll(conn, ddlIdxParagraphRelationsRelation, ddlIdxParagraphEntitiesEntity); err != nil {
		return err
	}
	if hasAllColumns(paragraphColumns, "source", "is_deleted", "created_at", "hash") {
		if err := execAll(conn, ddlIdxParagraphsSourceLiveCreated); err != nil {
			return err
		}
	}
	if hasAllColumns(relationColumns, "subject", "object", "is_inactive") {
		if err := execAll(conn, ddlIdxRelationsSubjectObjectActive); err != nil {
			return err
		}
	}
	return execAll(conn,
		ddlIdxRelationsObjectActive,
		ddlIdxParagraphVectorBackfillStatusRetryUpdated,
		ddlIdxEpisodeRebuildStatusRetryUpdated,
		ddlIdxPersonProfileRefreshStatusRetryUpdated,
	)
}

// hasAllColumns 判断列集合是否覆盖给定列，对齐 Python issubset 判断
func hasAllColumns(columns map[string]struct{}, names ...string) bool {
	for _, name := range names {
		if _, ok := columns[name]; !ok {
			return false
		}
	}
	return true
}
