package storage

// 元数据库建表 DDL，逐字对齐 Python core/storage/metadata_schema.py

// _ensure_person_identity_tables · Python L29
const ddlPersonIdentities = `
CREATE TABLE IF NOT EXISTS person_identities (
    person_id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    user_id TEXT NOT NULL,
    canonical_name TEXT NOT NULL UNIQUE,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    UNIQUE(platform, user_id)
)
`

// _ensure_person_identity_tables · Python L40
const ddlPersonIdentityAliases = `
CREATE TABLE IF NOT EXISTS person_identity_aliases (
    person_id TEXT NOT NULL REFERENCES person_identities(person_id) ON DELETE CASCADE,
    alias TEXT NOT NULL,
    group_id TEXT NOT NULL DEFAULT '',
    first_seen REAL NOT NULL,
    last_seen REAL NOT NULL,
    PRIMARY KEY(person_id, alias, group_id)
)
`

// _ensure_episode_empty_group_table · Python L55
const ddlEpisodeEmptyGroups = `
CREATE TABLE IF NOT EXISTS episode_empty_groups (
    source TEXT NOT NULL,
    input_fingerprint TEXT NOT NULL,
    PRIMARY KEY (source, input_fingerprint)
)
`

// _ensure_summary_checkpoint_tables · Python L66
const ddlChatSummaryCheckpoints = `
CREATE TABLE IF NOT EXISTS chat_summary_checkpoints (
    external_id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    trigger_message_count INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL
)
`

// _ensure_person_profile_alias_override_table · Python L454
const ddlPersonProfileAliasOverrides = `
CREATE TABLE IF NOT EXISTS person_profile_alias_overrides (
    person_id TEXT PRIMARY KEY,
    aliases_json TEXT NOT NULL,
    updated_at REAL NOT NULL,
    updated_by TEXT,
    source TEXT
)
`

// _ensure_relation_graph_projection_tables · Python L598
const ddlRelationGraphProjectionJobs = `
CREATE TABLE IF NOT EXISTS relation_graph_projection_jobs (
    relation_hash TEXT PRIMARY KEY,
    subject TEXT NOT NULL,
    object TEXT NOT NULL,
    desired_active INTEGER NOT NULL CHECK(desired_active IN (0, 1)),
    desired_lifecycle_revision INTEGER NOT NULL,
    job_revision INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending', 'running', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    lease_token TEXT,
    lease_expires_at REAL,
    last_error TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
)
`

// _ensure_external_memory_refs_foreign_key · Python L849
const ddlExternalMemoryRefsSchema19 = `
CREATE TABLE external_memory_refs_schema_19 (
    external_id TEXT PRIMARY KEY,
    paragraph_hash TEXT NOT NULL,
    source_type TEXT,
    created_at REAL NOT NULL,
    metadata_json TEXT,
    FOREIGN KEY (paragraph_hash) REFERENCES paragraphs(hash) ON DELETE CASCADE
)
`

// _ensure_storage_cleanup_tables · Python L885
const ddlStorageCleanupJobs = `
CREATE TABLE IF NOT EXISTS storage_cleanup_jobs (
    job_id INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    action TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    expected_state_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at REAL NOT NULL,
    lease_token TEXT,
    lease_until REAL,
    last_error TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    completed_at REAL,
    UNIQUE(operation_id, resource_type, resource_id, action),
    FOREIGN KEY (operation_id) REFERENCES delete_operations(operation_id) ON DELETE CASCADE
)
`

// _ensure_fuzzy_modify_plan_tables · Python L1073
const ddlMemoryFuzzyModifyPlans = `
CREATE TABLE IF NOT EXISTS memory_fuzzy_modify_plans (
    plan_id TEXT PRIMARY KEY,
    request_text TEXT NOT NULL,
    scope TEXT NOT NULL,
    target_person_id TEXT,
    target_chat_id TEXT,
    status TEXT NOT NULL,
    confidence REAL DEFAULT 0,
    plan_json TEXT NOT NULL,
    preview_json TEXT,
    execution_json TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    executed_at REAL,
    requested_by TEXT,
    reason TEXT
)
`

// _ensure_knowledge_package_tables · Python L1108
const ddlKnowledgePackages = `
CREATE TABLE IF NOT EXISTS knowledge_packages (
    installation_id TEXT PRIMARY KEY,
    package_id TEXT NOT NULL,
    version TEXT NOT NULL,
    name TEXT NOT NULL,
    content_level TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_key TEXT NOT NULL,
    chat_id TEXT,
    status TEXT NOT NULL DEFAULT 'installed',
    installed_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    UNIQUE(package_id, version, scope_type, scope_key)
)
`

// _ensure_knowledge_package_tables · Python L1134
const ddlKnowledgePackageParagraphs = `
CREATE TABLE IF NOT EXISTS knowledge_package_paragraphs (
    installation_id TEXT NOT NULL,
    doc_id TEXT NOT NULL,
    paragraph_hash TEXT NOT NULL,
    PRIMARY KEY (installation_id, doc_id),
    FOREIGN KEY (installation_id) REFERENCES knowledge_packages(installation_id) ON DELETE CASCADE,
    FOREIGN KEY (paragraph_hash) REFERENCES paragraphs(hash) ON DELETE CASCADE
)
`

// _ensure_knowledge_package_tables · Python L1148
const ddlKnowledgePackageResources = `
CREATE TABLE IF NOT EXISTS knowledge_package_resources (
    installation_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_key TEXT NOT NULL,
    created_by_installation INTEGER NOT NULL CHECK(created_by_installation IN (0, 1)),
    PRIMARY KEY (installation_id, resource_type, resource_key),
    FOREIGN KEY (installation_id) REFERENCES knowledge_packages(installation_id) ON DELETE CASCADE
)
`

// _initialize_tables · Python L1310
const ddlParagraphs = `
CREATE TABLE IF NOT EXISTS paragraphs (
    hash TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    vector_index INTEGER,
    created_at REAL,
    updated_at REAL,
    metadata TEXT,
    source TEXT,
    word_count INTEGER,
    event_time REAL,
    event_time_start REAL,
    event_time_end REAL,
    time_granularity TEXT,
    time_confidence REAL DEFAULT 1.0,
    knowledge_type TEXT DEFAULT 'mixed',
    is_permanent BOOLEAN DEFAULT 0,
    last_accessed REAL,
    access_count INTEGER DEFAULT 0,
    is_deleted INTEGER DEFAULT 0,
    deleted_at REAL,
    expires_at REAL,
    deletion_reason TEXT
)
`

// _initialize_tables · Python L1337
const ddlEntities = `
CREATE TABLE IF NOT EXISTS entities (
    hash TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    vector_index INTEGER,
    appearance_count INTEGER DEFAULT 1,
    created_at REAL,
    metadata TEXT,
    is_deleted INTEGER DEFAULT 0,
    deleted_at REAL
)
`

// _initialize_tables · Python L1351
const ddlRelations = `
CREATE TABLE IF NOT EXISTS relations (
    hash TEXT PRIMARY KEY,
    subject TEXT NOT NULL,
    predicate TEXT NOT NULL,
    object TEXT NOT NULL,
    vector_index INTEGER,
    confidence REAL DEFAULT 1.0,
    vector_state TEXT DEFAULT 'none',
    vector_updated_at REAL,
    vector_error TEXT,
    vector_retry_count INTEGER DEFAULT 0,
    created_at REAL,
    source_paragraph TEXT,
    metadata TEXT,
    is_permanent BOOLEAN DEFAULT 0,
    last_accessed REAL,
    access_count INTEGER DEFAULT 0,
    last_access_reinforced_at REAL,
    is_inactive BOOLEAN DEFAULT 0,
    inactive_since REAL,
    is_pinned BOOLEAN DEFAULT 0,
    protected_until REAL,
    last_reinforced REAL,
    retention_strength REAL NOT NULL DEFAULT 1.0,
    retention_anchor_at REAL NOT NULL,
    next_lifecycle_at REAL,
    reinforcement_count INTEGER NOT NULL DEFAULT 0,
    lifecycle_revision INTEGER NOT NULL DEFAULT 0,
    inactive_reason TEXT,
    UNIQUE(subject, predicate, object)
)
`

// _initialize_tables · Python L1386
const ddlDeletedRelations = `
CREATE TABLE IF NOT EXISTS deleted_relations (
    hash TEXT PRIMARY KEY,
    subject TEXT NOT NULL,
    predicate TEXT NOT NULL,
    object TEXT NOT NULL,
    vector_index INTEGER,
    confidence REAL DEFAULT 1.0,
    vector_state TEXT DEFAULT 'none',
    vector_updated_at REAL,
    vector_error TEXT,
    vector_retry_count INTEGER DEFAULT 0,
    created_at REAL,
    source_paragraph TEXT,
    metadata TEXT,
    is_permanent BOOLEAN DEFAULT 0,
    last_accessed REAL,
    access_count INTEGER DEFAULT 0,
    last_access_reinforced_at REAL,
    is_inactive BOOLEAN DEFAULT 0,
    inactive_since REAL,
    is_pinned BOOLEAN DEFAULT 0,
    protected_until REAL,
    last_reinforced REAL,
    retention_strength REAL NOT NULL DEFAULT 1.0,
    retention_anchor_at REAL NOT NULL,
    next_lifecycle_at REAL,
    reinforcement_count INTEGER NOT NULL DEFAULT 0,
    lifecycle_revision INTEGER NOT NULL DEFAULT 0,
    inactive_reason TEXT,
    deleted_at REAL
)
`

// _initialize_tables · Python L1421
const ddlRelationHashAliases = `
CREATE TABLE IF NOT EXISTS relation_hash_aliases (
    alias32 TEXT PRIMARY KEY,
    hash TEXT NOT NULL
)
`

// _initialize_tables · Python L1429
const ddlSchemaMigrations = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at REAL NOT NULL
)
`

// _initialize_tables · Python L1437
const ddlParagraphRelations = `
CREATE TABLE IF NOT EXISTS paragraph_relations (
    paragraph_hash TEXT NOT NULL,
    relation_hash TEXT NOT NULL,
    PRIMARY KEY (paragraph_hash, relation_hash),
    FOREIGN KEY (paragraph_hash) REFERENCES paragraphs(hash) ON DELETE CASCADE,
    FOREIGN KEY (relation_hash) REFERENCES relations(hash) ON DELETE CASCADE
)
`

// _initialize_tables · Python L1448
const ddlParagraphEntities = `
CREATE TABLE IF NOT EXISTS paragraph_entities (
    paragraph_hash TEXT NOT NULL,
    entity_hash TEXT NOT NULL,
    mention_count INTEGER DEFAULT 1,
    PRIMARY KEY (paragraph_hash, entity_hash),
    FOREIGN KEY (paragraph_hash) REFERENCES paragraphs(hash) ON DELETE CASCADE,
    FOREIGN KEY (entity_hash) REFERENCES entities(hash) ON DELETE CASCADE
)
`

// _initialize_tables · Python L1506
const ddlPersonProfileSwitches = `
CREATE TABLE IF NOT EXISTS person_profile_switches (
    stream_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL,
    PRIMARY KEY (stream_id, user_id)
)
`

// _initialize_tables · Python L1517
const ddlPersonProfileSnapshots = `
CREATE TABLE IF NOT EXISTS person_profile_snapshots (
    snapshot_id INTEGER PRIMARY KEY AUTOINCREMENT,
    person_id TEXT NOT NULL,
    profile_version INTEGER NOT NULL,
    profile_text TEXT NOT NULL,
    aliases_json TEXT,
    relation_edges_json TEXT,
    vector_evidence_json TEXT,
    evidence_ids_json TEXT,
    evidence_fingerprint TEXT,
    fact_claim_ids_json TEXT,
    updated_at REAL NOT NULL,
    expires_at REAL,
    source_note TEXT,
    UNIQUE(person_id, profile_version)
)
`

// _initialize_tables · Python L1538
const ddlPersonProfileActivePersons = `
CREATE TABLE IF NOT EXISTS person_profile_active_persons (
    stream_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    person_id TEXT NOT NULL,
    last_seen_at REAL NOT NULL,
    PRIMARY KEY (stream_id, user_id, person_id)
)
`

// _initialize_tables · Python L1547
const ddlPersonProfileOverrides = `
CREATE TABLE IF NOT EXISTS person_profile_overrides (
    person_id TEXT PRIMARY KEY,
    override_text TEXT NOT NULL,
    updated_at REAL NOT NULL,
    updated_by TEXT,
    source TEXT
)
`

// _initialize_tables · Python L1578
const ddlEpisodes = `
CREATE TABLE IF NOT EXISTS episodes (
    episode_id TEXT PRIMARY KEY,
    source TEXT,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    event_time_start REAL,
    event_time_end REAL,
    time_granularity TEXT,
    time_confidence REAL DEFAULT 1.0,
    participants_json TEXT,
    keywords_json TEXT,
    evidence_ids_json TEXT,
    paragraph_count INTEGER DEFAULT 0,
    llm_confidence REAL DEFAULT 0.0,
    segmentation_model TEXT,
    segmentation_version TEXT,
    input_fingerprint TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
)
`

// _initialize_tables · Python L1603
const ddlEpisodeParagraphs = `
CREATE TABLE IF NOT EXISTS episode_paragraphs (
    episode_id TEXT NOT NULL,
    paragraph_hash TEXT NOT NULL,
    position INTEGER DEFAULT 0,
    PRIMARY KEY (episode_id, paragraph_hash),
    FOREIGN KEY (episode_id) REFERENCES episodes(episode_id) ON DELETE CASCADE,
    FOREIGN KEY (paragraph_hash) REFERENCES paragraphs(hash) ON DELETE CASCADE
)
`

// _initialize_tables · Python L1615
const ddlEpisodeRebuildSources = `
CREATE TABLE IF NOT EXISTS episode_rebuild_sources (
    source TEXT PRIMARY KEY,
    status TEXT DEFAULT 'pending',
    retry_count INTEGER DEFAULT 0,
    last_error TEXT,
    reason TEXT,
    requested_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    desired_revision INTEGER NOT NULL DEFAULT 1,
    built_revision INTEGER NOT NULL DEFAULT 0,
    claimed_revision INTEGER,
    dirty_start REAL,
    dirty_end REAL,
    first_requested_at REAL,
    ready_at REAL,
    lease_token TEXT,
    lease_until REAL,
    next_attempt_at REAL,
    built_generation_hash TEXT,
    claimed_generation_hash TEXT,
    retry_revision INTEGER,
    retry_generation_hash TEXT
)
`

// _initialize_tables · Python L1662
const ddlParagraphVectorBackfill = `
CREATE TABLE IF NOT EXISTS paragraph_vector_backfill (
    paragraph_hash TEXT PRIMARY KEY,
    status TEXT DEFAULT 'pending',
    retry_count INTEGER DEFAULT 0,
    last_error TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
)
`

// _initialize_tables · Python L1676
const ddlMemoryFeedbackTasks = `
CREATE TABLE IF NOT EXISTS memory_feedback_tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    query_tool_id TEXT NOT NULL UNIQUE,
    session_id TEXT NOT NULL,
    query_timestamp REAL NOT NULL,
    due_at REAL NOT NULL,
    status TEXT DEFAULT 'pending',
    attempt_count INTEGER DEFAULT 0,
    query_snapshot_json TEXT,
    decision_json TEXT,
    last_error TEXT,
    rollback_status TEXT DEFAULT 'none',
    rollback_plan_json TEXT,
    rollback_result_json TEXT,
    rollback_error TEXT,
    rollback_requested_by TEXT,
    rollback_reason TEXT,
    rollback_requested_at REAL,
    rolled_back_at REAL,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
)
`

// _initialize_tables · Python L1708
const ddlMemoryFeedbackActionLogs = `
CREATE TABLE IF NOT EXISTS memory_feedback_action_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL,
    query_tool_id TEXT NOT NULL,
    action_type TEXT NOT NULL,
    target_hash TEXT,
    before_json TEXT,
    after_json TEXT,
    reason TEXT,
    created_at REAL NOT NULL,
    FOREIGN KEY (task_id) REFERENCES memory_feedback_tasks(id) ON DELETE CASCADE
)
`

// _initialize_tables · Python L1734
const ddlParagraphStaleRelationMarks = `
CREATE TABLE IF NOT EXISTS paragraph_stale_relation_marks (
    paragraph_hash TEXT NOT NULL,
    relation_hash TEXT NOT NULL,
    query_tool_id TEXT,
    task_id INTEGER,
    reason TEXT,
    source_type TEXT,
    source_id TEXT,
    source_operation_id TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    PRIMARY KEY (paragraph_hash, relation_hash),
    FOREIGN KEY (paragraph_hash) REFERENCES paragraphs(hash) ON DELETE CASCADE,
    FOREIGN KEY (relation_hash) REFERENCES relations(hash) ON DELETE CASCADE,
    FOREIGN KEY (task_id) REFERENCES memory_feedback_tasks(id) ON DELETE SET NULL
)
`

// _initialize_tables · Python L1769
const ddlPersonProfileRefreshQueue = `
CREATE TABLE IF NOT EXISTS person_profile_refresh_queue (
    person_id TEXT PRIMARY KEY,
    status TEXT DEFAULT 'pending',
    reason TEXT,
    source_query_tool_id TEXT,
    retry_count INTEGER DEFAULT 0,
    last_error TEXT,
    requested_at REAL NOT NULL,
    updated_at REAL NOT NULL
)
`

// _initialize_tables · Python L1790
const ddlExternalMemoryRefs = `
CREATE TABLE IF NOT EXISTS external_memory_refs (
    external_id TEXT PRIMARY KEY,
    paragraph_hash TEXT NOT NULL,
    source_type TEXT,
    created_at REAL NOT NULL,
    metadata_json TEXT,
    FOREIGN KEY (paragraph_hash) REFERENCES paragraphs(hash) ON DELETE CASCADE
)
`

// _initialize_tables · Python L1804
const ddlMemoryV5Operations = `
CREATE TABLE IF NOT EXISTS memory_v5_operations (
    operation_id TEXT PRIMARY KEY,
    action TEXT NOT NULL,
    target TEXT,
    reason TEXT,
    updated_by TEXT,
    created_at REAL NOT NULL,
    resolved_hashes_json TEXT,
    result_json TEXT
)
`
