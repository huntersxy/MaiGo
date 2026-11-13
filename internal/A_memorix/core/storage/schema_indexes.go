package storage

// 元数据库索引与触发器 DDL，逐字对齐 Python core/storage/metadata_schema.py

// _ensure_person_identity_tables · Python L50
const ddlIdxPersonIdentityAlias = `
CREATE INDEX IF NOT EXISTS idx_person_identity_alias ON person_identity_aliases(alias)
`

// _ensure_summary_checkpoint_tables · Python L74
const ddlIdxChatSummaryCheckpointsChat = `
CREATE INDEX IF NOT EXISTS idx_chat_summary_checkpoints_chat
ON chat_summary_checkpoints(chat_id, trigger_message_count)
`

// _ensure_summary_checkpoint_tables · Python L83
const ddlIdxFactClaimsScopeValue = `
CREATE INDEX IF NOT EXISTS idx_fact_claims_scope_value ON fact_claims(scope_type, scope_id, value_normalized, status)
`

// _ensure_summary_checkpoint_tables · Python L85
const ddlIdxExternalMemoryRefsChat = `
CREATE INDEX IF NOT EXISTS idx_external_memory_refs_chat ON external_memory_refs(source_type, json_extract(metadata_json, '$.chat_id'))
`

// _ensure_person_profile_alias_override_table · Python L463
const ddlIdxPersonProfileAliasOverridesUpdated = `
CREATE INDEX IF NOT EXISTS idx_person_profile_alias_overrides_updated
ON person_profile_alias_overrides(updated_at DESC)
`

// _ensure_lifecycle_columns · Python L560
const ddlIdxRelationsLifecycleDue = `
CREATE INDEX idx_relations_lifecycle_due
ON relations(is_inactive, next_lifecycle_at, hash)
`

// _ensure_lifecycle_columns · Python L566
const ddlIdxRelationsInactiveReason = `
CREATE INDEX IF NOT EXISTS idx_relations_inactive_reason
ON relations(is_inactive, inactive_reason, inactive_since)
`

// _ensure_lifecycle_columns · Python L587
const ddlIdxParagraphsExpiration = `
CREATE INDEX IF NOT EXISTS idx_paragraphs_expiration
ON paragraphs(is_deleted, expires_at)
`

// _ensure_relation_graph_projection_tables · Python L618
const ddlIdxRelationGraphProjectionReady = `
CREATE INDEX IF NOT EXISTS idx_relation_graph_projection_ready
ON relation_graph_projection_jobs(status, lease_expires_at, updated_at, relation_hash)
`

// _ensure_relation_graph_projection_tables · Python L625
const ddlTrgRelationsGraphProjectionLifecycle = `
CREATE TRIGGER trg_relations_graph_projection_lifecycle
AFTER UPDATE OF is_inactive ON relations
WHEN COALESCE(OLD.is_inactive, 0) != COALESCE(NEW.is_inactive, 0)
BEGIN
    INSERT INTO relation_graph_projection_jobs (
        relation_hash, subject, object, desired_active,
        desired_lifecycle_revision, job_revision, status,
        attempt_count, lease_token, lease_expires_at, last_error,
        created_at, updated_at
    ) VALUES (
        NEW.hash, NEW.subject, NEW.object,
        CASE WHEN COALESCE(NEW.is_inactive, 0) = 0 THEN 1 ELSE 0 END,
        COALESCE(NEW.lifecycle_revision, 0), 1, 'pending',
        0, NULL, NULL, NULL,
        CAST(strftime('%s', 'now') AS REAL),
        CAST(strftime('%s', 'now') AS REAL)
    )
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
        updated_at = excluded.updated_at;
END
`

// _ensure_external_memory_refs_foreign_key · Python L874
const ddlIdxExternalMemoryRefsParagraphRebuild = `
CREATE INDEX idx_external_memory_refs_paragraph
ON external_memory_refs(paragraph_hash)
`

// _ensure_storage_cleanup_tables · Python L909
const ddlIdxStorageCleanupReady = `
CREATE INDEX IF NOT EXISTS idx_storage_cleanup_ready
ON storage_cleanup_jobs(status, next_attempt_at, lease_until, created_at)
`

// _ensure_storage_cleanup_tables · Python L915
const ddlIdxStorageCleanupOperation = `
CREATE INDEX IF NOT EXISTS idx_storage_cleanup_operation
ON storage_cleanup_jobs(operation_id, status, job_id)
`

// _ensure_episode_source_revision_columns · Python L1058
const ddlIdxEpisodeRebuildClaim = `
CREATE INDEX IF NOT EXISTS idx_episode_rebuild_claim
ON episode_rebuild_sources(lease_until, next_attempt_at, ready_at, first_requested_at)
`

// _ensure_episode_source_revision_columns · Python L1064
const ddlIdxEpisodeRebuildRevision = `
CREATE INDEX IF NOT EXISTS idx_episode_rebuild_revision
ON episode_rebuild_sources(desired_revision, built_revision)
`

// _ensure_fuzzy_modify_plan_tables · Python L1092
const ddlIdxMemoryFuzzyModifyPlansCreated = `
CREATE INDEX IF NOT EXISTS idx_memory_fuzzy_modify_plans_created
ON memory_fuzzy_modify_plans(created_at DESC)
`

// _ensure_fuzzy_modify_plan_tables · Python L1096
const ddlIdxMemoryFuzzyModifyPlansStatusUpdated = `
CREATE INDEX IF NOT EXISTS idx_memory_fuzzy_modify_plans_status_updated
ON memory_fuzzy_modify_plans(status, updated_at DESC)
`

// _ensure_fuzzy_modify_plan_tables · Python L1100
const ddlIdxMemoryFuzzyModifyPlansTarget = `
CREATE INDEX IF NOT EXISTS idx_memory_fuzzy_modify_plans_target
ON memory_fuzzy_modify_plans(target_person_id, target_chat_id)
`

// _ensure_knowledge_package_tables · Python L1126
const ddlIdxKnowledgePackagesUpdated = `
CREATE INDEX IF NOT EXISTS idx_knowledge_packages_updated
ON knowledge_packages(status, updated_at DESC)
`

// _ensure_knowledge_package_tables · Python L1130
const ddlIdxKnowledgePackagesPackage = `
CREATE INDEX IF NOT EXISTS idx_knowledge_packages_package
ON knowledge_packages(package_id, version, status)
`

// _ensure_knowledge_package_tables · Python L1144
const ddlIdxKnowledgePackageParagraphsHash = `
CREATE INDEX IF NOT EXISTS idx_knowledge_package_paragraphs_hash
ON knowledge_package_paragraphs(paragraph_hash, installation_id)
`

// _ensure_knowledge_package_tables · Python L1158
const ddlIdxKnowledgePackageResourcesLookup = `
CREATE INDEX IF NOT EXISTS idx_knowledge_package_resources_lookup
ON knowledge_package_resources(resource_type, resource_key, created_by_installation)
`

// _initialize_tables · Python L1460
const ddlIdxParagraphsVector = `
CREATE INDEX IF NOT EXISTS idx_paragraphs_vector
ON paragraphs(vector_index)
`

// _initialize_tables · Python L1464
const ddlIdxEntitiesVector = `
CREATE INDEX IF NOT EXISTS idx_entities_vector
ON entities(vector_index)
`

// _initialize_tables · Python L1468
const ddlIdxRelationsVector = `
CREATE INDEX IF NOT EXISTS idx_relations_vector
ON relations(vector_index)
`

// _initialize_tables · Python L1472
const ddlIdxRelationsSubject = `
CREATE INDEX IF NOT EXISTS idx_relations_subject
ON relations(subject)
`

// _initialize_tables · Python L1476
const ddlIdxRelationsObject = `
CREATE INDEX IF NOT EXISTS idx_relations_object
ON relations(object)
`

// _initialize_tables · Python L1480
const ddlIdxEntitiesName = `
CREATE INDEX IF NOT EXISTS idx_entities_name
ON entities(name)
`

// _initialize_tables · Python L1484
const ddlIdxParagraphsSource = `
CREATE INDEX IF NOT EXISTS idx_paragraphs_source
ON paragraphs(source)
`

// _initialize_tables · Python L1488
const ddlIdxParagraphsDeleted = `
CREATE INDEX IF NOT EXISTS idx_paragraphs_deleted
ON paragraphs(is_deleted, deleted_at)
`

// _initialize_tables · Python L1492
const ddlIdxEntitiesDeleted = `
CREATE INDEX IF NOT EXISTS idx_entities_deleted
ON entities(is_deleted, deleted_at)
`

// _initialize_tables · Python L1496
const ddlIdxRelationsInactive = `
CREATE INDEX IF NOT EXISTS idx_relations_inactive
ON relations(is_inactive, inactive_since)
`

// _initialize_tables · Python L1500
const ddlIdxRelationsProtected = `
CREATE INDEX IF NOT EXISTS idx_relations_protected
ON relations(is_pinned, protected_until)
`

// _initialize_tables · Python L1559
const ddlIdxPersonProfileSwitchesEnabled = `
CREATE INDEX IF NOT EXISTS idx_person_profile_switches_enabled
ON person_profile_switches(enabled)
`

// _initialize_tables · Python L1563
const ddlIdxPersonProfileSnapshotsPerson = `
CREATE INDEX IF NOT EXISTS idx_person_profile_snapshots_person
ON person_profile_snapshots(person_id, updated_at DESC)
`

// _initialize_tables · Python L1567
const ddlIdxPersonProfileActiveSeen = `
CREATE INDEX IF NOT EXISTS idx_person_profile_active_seen
ON person_profile_active_persons(last_seen_at DESC)
`

// _initialize_tables · Python L1571
const ddlIdxPersonProfileOverridesUpdated = `
CREATE INDEX IF NOT EXISTS idx_person_profile_overrides_updated
ON person_profile_overrides(updated_at DESC)
`

// _initialize_tables · Python L1641
const ddlIdxEpisodesSourceTimeEnd = `
CREATE INDEX IF NOT EXISTS idx_episodes_source_time_end
ON episodes(source, event_time_end DESC)
`

// _initialize_tables · Python L1645
const ddlIdxEpisodesUpdatedAt = `
CREATE INDEX IF NOT EXISTS idx_episodes_updated_at
ON episodes(updated_at DESC)
`

// _initialize_tables · Python L1649
const ddlIdxEpisodeParagraphsParagraph = `
CREATE INDEX IF NOT EXISTS idx_episode_paragraphs_paragraph
ON episode_paragraphs(paragraph_hash)
`

// _initialize_tables · Python L1653
const ddlIdxEpisodeRebuildStatusUpdated = `
CREATE INDEX IF NOT EXISTS idx_episode_rebuild_status_updated
ON episode_rebuild_sources(status, updated_at)
`

// _initialize_tables · Python L1657
const ddlIdxEpisodeRebuildUpdatedAt = `
CREATE INDEX IF NOT EXISTS idx_episode_rebuild_updated_at
ON episode_rebuild_sources(updated_at DESC)
`

// _initialize_tables · Python L1672
const ddlIdxParagraphVectorBackfillStatusUpdated = `
CREATE INDEX IF NOT EXISTS idx_paragraph_vector_backfill_status_updated
ON paragraph_vector_backfill(status, updated_at)
`

// _initialize_tables · Python L1700
const ddlIdxMemoryFeedbackTasksStatusDue = `
CREATE INDEX IF NOT EXISTS idx_memory_feedback_tasks_status_due
ON memory_feedback_tasks(status, due_at, updated_at)
`

// _initialize_tables · Python L1704
const ddlIdxMemoryFeedbackTasksSessionQuery = `
CREATE INDEX IF NOT EXISTS idx_memory_feedback_tasks_session_query
ON memory_feedback_tasks(session_id, query_timestamp DESC)
`

// _initialize_tables · Python L1722
const ddlIdxMemoryFeedbackActionLogsTask = `
CREATE INDEX IF NOT EXISTS idx_memory_feedback_action_logs_task
ON memory_feedback_action_logs(task_id, created_at ASC)
`

// _initialize_tables · Python L1726
const ddlIdxMemoryFeedbackActionLogsQuery = `
CREATE INDEX IF NOT EXISTS idx_memory_feedback_action_logs_query
ON memory_feedback_action_logs(query_tool_id, created_at DESC)
`

// _initialize_tables · Python L1730
const ddlIdxMemoryFeedbackActionLogsTarget = `
CREATE INDEX IF NOT EXISTS idx_memory_feedback_action_logs_target
ON memory_feedback_action_logs(target_hash)
`

// _initialize_tables · Python L1752
const ddlIdxParagraphStaleRelationMarksParagraph = `
CREATE INDEX IF NOT EXISTS idx_paragraph_stale_relation_marks_paragraph
ON paragraph_stale_relation_marks(paragraph_hash, updated_at DESC)
`

// _initialize_tables · Python L1756
const ddlIdxParagraphStaleRelationMarksRelation = `
CREATE INDEX IF NOT EXISTS idx_paragraph_stale_relation_marks_relation
ON paragraph_stale_relation_marks(relation_hash, updated_at DESC)
`

// _initialize_tables · Python L1760
const ddlIdxParagraphStaleRelationMarksUpdated = `
CREATE INDEX IF NOT EXISTS idx_paragraph_stale_relation_marks_updated
ON paragraph_stale_relation_marks(updated_at DESC)
`

// _initialize_tables · Python L1765
const ddlIdxParagraphStaleRelationMarksSource = `
CREATE INDEX IF NOT EXISTS idx_paragraph_stale_relation_marks_source
ON paragraph_stale_relation_marks(source_type, source_id, updated_at DESC)
`

// _initialize_tables · Python L1781
const ddlIdxPersonProfileRefreshQueueStatusUpdated = `
CREATE INDEX IF NOT EXISTS idx_person_profile_refresh_queue_status_updated
ON person_profile_refresh_queue(status, updated_at)
`

// _initialize_tables · Python L1785
const ddlIdxPersonProfileRefreshQueueRequested = `
CREATE INDEX IF NOT EXISTS idx_person_profile_refresh_queue_requested
ON person_profile_refresh_queue(requested_at DESC)
`

// _initialize_tables · Python L1816
const ddlIdxMemoryV5OperationsCreated = `
CREATE INDEX IF NOT EXISTS idx_memory_v5_operations_created
ON memory_v5_operations(created_at DESC)
`

// _initialize_tables · Python L1833
const ddlIdxDeleteOperationsCreated = `
CREATE INDEX IF NOT EXISTS idx_delete_operations_created
ON delete_operations(created_at DESC)
`

// _initialize_tables · Python L1837
const ddlIdxDeleteOperationsMode = `
CREATE INDEX IF NOT EXISTS idx_delete_operations_mode
ON delete_operations(mode, created_at DESC)
`

// _initialize_tables · Python L1853
const ddlIdxDeleteOperationItemsOperation = `
CREATE INDEX IF NOT EXISTS idx_delete_operation_items_operation
ON delete_operation_items(operation_id, id ASC)
`

// _initialize_tables · Python L1857
const ddlIdxDeleteOperationItemsHash = `
CREATE INDEX IF NOT EXISTS idx_delete_operation_items_hash
ON delete_operation_items(item_hash)
`

// _create_temporal_indexes_if_ready · Python L2392
const ddlIdxParagraphsEventTime = `
CREATE INDEX IF NOT EXISTS idx_paragraphs_event_time ON paragraphs(event_time)
`

// _create_temporal_indexes_if_ready · Python L2394
const ddlIdxParagraphsEventStart = `
CREATE INDEX IF NOT EXISTS idx_paragraphs_event_start ON paragraphs(event_time_start)
`

// _create_temporal_indexes_if_ready · Python L2396
const ddlIdxParagraphsEventEnd = `
CREATE INDEX IF NOT EXISTS idx_paragraphs_event_end ON paragraphs(event_time_end)
`

// _create_performance_indexes · Python L2406
const ddlIdxParagraphRelationsRelation = `
CREATE INDEX IF NOT EXISTS idx_paragraph_relations_relation
ON paragraph_relations(relation_hash, paragraph_hash)
`

// _create_performance_indexes · Python L2412
const ddlIdxParagraphEntitiesEntity = `
CREATE INDEX IF NOT EXISTS idx_paragraph_entities_entity
ON paragraph_entities(entity_hash, paragraph_hash)
`

// _create_performance_indexes · Python L2419
const ddlIdxParagraphsSourceLiveCreated = `
CREATE INDEX IF NOT EXISTS idx_paragraphs_source_live_created
ON paragraphs(source, is_deleted, created_at, hash)
`

// _create_performance_indexes · Python L2426
const ddlIdxRelationsSubjectObjectActive = `
CREATE INDEX IF NOT EXISTS idx_relations_subject_object_active
ON relations(LOWER(TRIM(subject)), LOWER(TRIM(object)), is_inactive)
`

// _create_performance_indexes · Python L2432
const ddlIdxRelationsObjectActive = `
CREATE INDEX IF NOT EXISTS idx_relations_object_active
ON relations(LOWER(TRIM(object)), is_inactive)
`

// _create_performance_indexes · Python L2438
const ddlIdxParagraphVectorBackfillStatusRetryUpdated = `
CREATE INDEX IF NOT EXISTS idx_paragraph_vector_backfill_status_retry_updated
ON paragraph_vector_backfill(status, retry_count, updated_at)
`

// _create_performance_indexes · Python L2444
const ddlIdxEpisodeRebuildStatusRetryUpdated = `
CREATE INDEX IF NOT EXISTS idx_episode_rebuild_status_retry_updated
ON episode_rebuild_sources(status, retry_count, requested_at, updated_at)
`

// _create_performance_indexes · Python L2450
const ddlIdxPersonProfileRefreshStatusRetryUpdated = `
CREATE INDEX IF NOT EXISTS idx_person_profile_refresh_status_retry_updated
ON person_profile_refresh_queue(status, retry_count, requested_at, updated_at)
`

// _initialize_tables · Python L1800（与 L2120 同形）
const ddlIdxExternalMemoryRefsParagraph = `
CREATE INDEX IF NOT EXISTS idx_external_memory_refs_paragraph
ON external_memory_refs(paragraph_hash)
`
