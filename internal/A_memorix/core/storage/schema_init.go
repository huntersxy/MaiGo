package storage

// A_memorix 元数据建表初始化，逐条对齐 Python metadata_schema.py 的 _initialize_tables
// 语句顺序与 Python 完全一致，保证同一份库两种实现可互换

// initializeTables 初始化数据库表结构，对齐 Python _initialize_tables
func (s *Store) initializeTables(conn *Conn) error {
	// 段落表、实体表、关系表与回收站关系表
	if err := execAll(conn,
		ddlParagraphs,
		ddlEntities,
		ddlRelations,
		ddlDeletedRelations,
	); err != nil {
		return err
	}
	// 32 位哈希别名映射（用于 vNext 唯一解析）与 schema 版本表
	if err := execAll(conn,
		ddlRelationHashAliases,
		ddlSchemaMigrations,
	); err != nil {
		return err
	}
	// 三元组与段落、实体与段落的关联表
	if err := execAll(conn,
		ddlParagraphRelations,
		ddlParagraphEntities,
	); err != nil {
		return err
	}
	// 基础索引
	if err := execAll(conn,
		ddlIdxParagraphsVector,
		ddlIdxEntitiesVector,
		ddlIdxRelationsVector,
		ddlIdxRelationsSubject,
		ddlIdxRelationsObject,
		ddlIdxEntitiesName,
		ddlIdxParagraphsSource,
		ddlIdxParagraphsDeleted,
		ddlIdxEntitiesDeleted,
		ddlIdxRelationsInactive,
		ddlIdxRelationsProtected,
	); err != nil {
		return err
	}

	// 人物画像开关表、快照表、活跃人物集合与覆盖表
	if err := execAll(conn,
		ddlPersonProfileSwitches,
		ddlPersonProfileSnapshots,
	); err != nil {
		return err
	}
	if err := ensurePersonProfileSnapshotColumns(conn); err != nil {
		return err
	}
	if err := execAll(conn, ddlPersonProfileActivePersons, ddlPersonProfileOverrides); err != nil {
		return err
	}
	if err := ensurePersonProfileAliasOverrideTable(conn); err != nil {
		return err
	}
	if err := ensurePersonIdentityTables(conn); err != nil {
		return err
	}
	if err := execAll(conn,
		ddlIdxPersonProfileSwitchesEnabled,
		ddlIdxPersonProfileSnapshotsPerson,
		ddlIdxPersonProfileActiveSeen,
		ddlIdxPersonProfileOverridesUpdated,
	); err != nil {
		return err
	}

	// Episode 情景记忆表
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

	// 反馈任务、操作日志与陈旧关系标记
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
	if err := s.createTemporalIndexesIfReady(conn); err != nil {
		return err
	}
	if err := s.createPerformanceIndexes(conn); err != nil {
		return err
	}

	// 新版 schema 包含完整字段，直接写入版本信息
	if _, err := conn.Exec(
		"INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)",
		SchemaVersion,
		nowTimestamp(),
	); err != nil {
		return err
	}
	return conn.Commit()
}
