package storage

// 带参数的迁移 SQL，逐字对齐 Python core/storage/metadata_schema.py
// 只放无法直接作为 DDL 常量执行、需要绑定参数的语句

const episodeRebuildRevisionResetSQL = `
UPDATE episode_rebuild_sources
SET desired_revision = MAX(1, COALESCE(desired_revision, 1)),
    built_revision = CASE
        WHEN status = 'done' THEN MAX(1, COALESCE(desired_revision, 1))
        ELSE MIN(COALESCE(built_revision, 0), MAX(0, COALESCE(desired_revision, 1) - 1))
    END,
    status = CASE WHEN status = 'running' THEN 'pending' ELSE status END,
    first_requested_at = COALESCE(first_requested_at, requested_at, ?),
    ready_at = COALESCE(ready_at, requested_at, ?),
    next_attempt_at = COALESCE(next_attempt_at, requested_at, ?),
    lease_token = NULL,
    lease_until = NULL,
    claimed_revision = NULL,
    claimed_generation_hash = NULL,
    retry_revision = NULL,
    retry_generation_hash = NULL
`

const episodePendingParagraphsMigrateSQL = `
INSERT INTO episode_rebuild_sources (
    source, status, retry_count, last_error, reason,
    requested_at, updated_at, desired_revision, built_revision,
    first_requested_at, ready_at, next_attempt_at
)
SELECT source, 'pending', 0, NULL, 'schema_19_pending_migration',
       MIN(updated_at), ?, 1, 0, MIN(updated_at), MIN(updated_at), MIN(updated_at)
FROM episode_pending_paragraphs
WHERE source IS NOT NULL AND TRIM(source) != ''
GROUP BY source
ON CONFLICT(source) DO UPDATE SET
    desired_revision = episode_rebuild_sources.desired_revision + 1,
    status = 'pending',
    reason = 'schema_19_pending_migration',
    updated_at = excluded.updated_at,
    ready_at = MIN(episode_rebuild_sources.ready_at, excluded.ready_at),
    next_attempt_at = MIN(episode_rebuild_sources.next_attempt_at, excluded.next_attempt_at)
`

const episodeSourceDiscoverySQL = `
INSERT INTO episode_rebuild_sources (
    source, status, retry_count, last_error, reason,
    requested_at, updated_at, desired_revision, built_revision,
    first_requested_at, ready_at, next_attempt_at
)
SELECT source, 'pending', 0, NULL, 'schema_19_source_discovery',
       ?, ?, 1, 0, ?, ?, ?
FROM (
    SELECT DISTINCT source
    FROM paragraphs
    WHERE source IS NOT NULL AND TRIM(source) != ''
      AND (is_deleted IS NULL OR is_deleted = 0)
    UNION
    SELECT DISTINCT source
    FROM episodes
    WHERE source IS NOT NULL AND TRIM(source) != ''
) discovered
WHERE 1 = 1
ON CONFLICT(source) DO NOTHING
`

const relationsLifecycleNormalizeSQL = `
UPDATE relations
SET retention_strength = MIN(1.0, MAX(0.0, COALESCE(retention_strength, 1.0))),
    retention_anchor_at = COALESCE(retention_anchor_at, ?),
    next_lifecycle_at = CASE
        WHEN is_pinned = 1 OR is_permanent = 1 THEN NULL
        ELSE COALESCE(next_lifecycle_at, ?)
    END,
    reinforcement_count = COALESCE(reinforcement_count, 0),
    lifecycle_revision = COALESCE(lifecycle_revision, 0),
    inactive_reason = CASE
        WHEN is_inactive = 1 THEN COALESCE(inactive_reason, 'schema_19_migrated_inactive')
        ELSE inactive_reason
    END,
    is_pinned = CASE WHEN is_permanent = 1 THEN 1 ELSE is_pinned END
`

const deletedRelationsLifecycleNormalizeSQL = `
UPDATE deleted_relations
SET retention_strength = MIN(1.0, MAX(0.0, COALESCE(retention_strength, 1.0))),
    retention_anchor_at = COALESCE(retention_anchor_at, ?),
    reinforcement_count = COALESCE(reinforcement_count, 0),
    lifecycle_revision = COALESCE(lifecycle_revision, 0),
    inactive_reason = COALESCE(inactive_reason, 'deleted')
`

const paragraphsSoftDeleteReasonSQL = `
UPDATE paragraphs
SET deletion_reason = 'schema_19_migrated_soft_delete'
WHERE is_deleted = 1 AND deletion_reason IS NULL
`

const externalMemoryRefsRebuildInsertSQL = `
INSERT INTO external_memory_refs_schema_19 (
    external_id, paragraph_hash, source_type, created_at, metadata_json
)
SELECT r.external_id, r.paragraph_hash, r.source_type, r.created_at, r.metadata_json
FROM external_memory_refs r
JOIN paragraphs p ON p.hash = r.paragraph_hash
`

const relationGraphProjectionSeedSQL = `
INSERT INTO relation_graph_projection_jobs (
    relation_hash, subject, object, desired_active,
    desired_lifecycle_revision, job_revision, status,
    attempt_count, lease_token, lease_expires_at, last_error,
    created_at, updated_at
)
SELECT hash, subject, object,
       CASE WHEN COALESCE(is_inactive, 0) = 0 THEN 1 ELSE 0 END,
       COALESCE(lifecycle_revision, 0), 1, 'pending',
       0, NULL, NULL, NULL, ?, ?
FROM relations
WHERE 1 = 1
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
`

const migrationColumnSwapCountSQL = `
SELECT count(*) FROM paragraphs
WHERE typeof(vector_index) = 'text'
AND source IN ('mixed', 'factual', 'narrative', 'structured', 'auto')
`

const migrationColumnSwapFixSQL = `
UPDATE paragraphs
SET
    knowledge_type = source,
    source = vector_index,
    vector_index = NULL
WHERE typeof(vector_index) = 'text'
AND source IN ('mixed', 'factual', 'narrative', 'structured', 'auto')
`
