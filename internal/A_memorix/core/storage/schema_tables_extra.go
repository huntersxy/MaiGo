package storage

// 元数据库建表 DDL 续（删除操作与历史 V5 表），对齐 Python core/storage/metadata_schema.py

// _initialize_tables · Python L1820
const ddlDeleteOperations = `
CREATE TABLE IF NOT EXISTS delete_operations (
    operation_id TEXT PRIMARY KEY,
    mode TEXT NOT NULL,
    selector TEXT,
    reason TEXT,
    requested_by TEXT,
    status TEXT NOT NULL,
    created_at REAL NOT NULL,
    restored_at REAL,
    summary_json TEXT
)
`

// _initialize_tables · Python L1841
const ddlDeleteOperationItems = `
CREATE TABLE IF NOT EXISTS delete_operation_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id TEXT NOT NULL,
    item_type TEXT NOT NULL,
    item_hash TEXT,
    item_key TEXT,
    payload_json TEXT,
    created_at REAL NOT NULL,
    FOREIGN KEY (operation_id) REFERENCES delete_operations(operation_id) ON DELETE CASCADE
)
`

// _migrate_schema · Python L2264（V5 历史版本，仅在极旧库缺表时使用）
const ddlDeletedRelationsLegacyV5 = `
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
    is_inactive BOOLEAN DEFAULT 0,
    inactive_since REAL,
    is_pinned BOOLEAN DEFAULT 0,
    protected_until REAL,
    last_reinforced REAL,
    deleted_at REAL  -- 用于记录删除时间的额外列
)
`

// _ensure_fact_tables · metadata_fact.py FACT_SCHEMA_STATEMENTS[0]
const ddlFactClaims = `
CREATE TABLE IF NOT EXISTS fact_claims (
    claim_id TEXT PRIMARY KEY,
    scope_type TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    fact_key TEXT NOT NULL,
    value_text TEXT NOT NULL,
    value_normalized TEXT NOT NULL,
    polarity TEXT NOT NULL,
    cardinality TEXT NOT NULL,
    conflict_group TEXT NOT NULL,
    stability TEXT NOT NULL,
    profile_section TEXT NOT NULL,
    authority TEXT NOT NULL,
    status TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0,
    valid_from REAL,
    valid_to REAL,
    first_observed_at REAL NOT NULL,
    last_confirmed_at REAL NOT NULL,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
)
`

// _ensure_fact_tables · metadata_fact.py FACT_SCHEMA_STATEMENTS[1]
const ddlIdxFactClaimsScopeStatus = `
CREATE INDEX IF NOT EXISTS idx_fact_claims_scope_status
ON fact_claims(scope_type, scope_id, status, profile_section, last_confirmed_at DESC)
`

// _ensure_fact_tables · metadata_fact.py FACT_SCHEMA_STATEMENTS[2]
const ddlIdxFactClaimsConflict = `
CREATE INDEX IF NOT EXISTS idx_fact_claims_conflict
ON fact_claims(scope_type, scope_id, conflict_group, status)
`

// _ensure_fact_tables · metadata_fact.py FACT_SCHEMA_STATEMENTS[3]
const ddlFactEvidence = `
CREATE TABLE IF NOT EXISTS fact_evidence (
    claim_id TEXT NOT NULL,
    evidence_type TEXT NOT NULL,
    evidence_id TEXT NOT NULL,
    stance TEXT NOT NULL,
    weight REAL NOT NULL DEFAULT 1.0,
    observed_at REAL NOT NULL,
    metadata_json TEXT,
    PRIMARY KEY(claim_id, evidence_type, evidence_id, stance),
    FOREIGN KEY(claim_id) REFERENCES fact_claims(claim_id) ON DELETE CASCADE
)
`

// _ensure_fact_tables · metadata_fact.py FACT_SCHEMA_STATEMENTS[4]
const ddlIdxFactEvidenceClaim = `
CREATE INDEX IF NOT EXISTS idx_fact_evidence_claim
ON fact_evidence(claim_id, observed_at DESC)
`

// _ensure_fact_tables · metadata_fact.py FACT_SCHEMA_STATEMENTS[5]
const ddlFactTransitions = `
CREATE TABLE IF NOT EXISTS fact_transitions (
    transition_id INTEGER PRIMARY KEY AUTOINCREMENT,
    old_claim_id TEXT,
    new_claim_id TEXT,
    transition_type TEXT NOT NULL,
    reason TEXT,
    evidence_type TEXT,
    evidence_id TEXT,
    created_at REAL NOT NULL
)
`

// _ensure_fact_tables · metadata_fact.py FACT_SCHEMA_STATEMENTS[6]
const ddlIdxFactTransitionsOld = `
CREATE INDEX IF NOT EXISTS idx_fact_transitions_old
ON fact_transitions(old_claim_id, created_at DESC)
`

// _ensure_fact_tables · metadata_fact.py FACT_SCHEMA_STATEMENTS[7]
const ddlIdxFactTransitionsNew = `
CREATE INDEX IF NOT EXISTS idx_fact_transitions_new
ON fact_transitions(new_claim_id, created_at DESC)
`

// _ensure_image_memory_tables · executescript #1
const ddlImageAssets = `
CREATE TABLE IF NOT EXISTS image_assets (
    asset_id TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL UNIQUE,
    storage_key TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    byte_size INTEGER NOT NULL,
    width INTEGER NOT NULL,
    height INTEGER NOT NULL,
    status TEXT NOT NULL,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
)
`

// _ensure_image_memory_tables · executescript #2
const ddlIdxImageAssetsStatusUpdated = `
CREATE INDEX IF NOT EXISTS idx_image_assets_status_updated
ON image_assets(status, updated_at DESC)
`

// _ensure_image_memory_tables · executescript #3
const ddlImageOccurrences = `
CREATE TABLE IF NOT EXISTS image_occurrences (
    occurrence_id TEXT PRIMARY KEY,
    asset_id TEXT NOT NULL,
    external_ref TEXT NOT NULL UNIQUE,
    source_kind TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    chat_id TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '',
    component_path TEXT NOT NULL DEFAULT '',
    occurred_at REAL,
    installation_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    FOREIGN KEY (asset_id) REFERENCES image_assets(asset_id)
)
`

// _ensure_image_memory_tables · executescript #4
const ddlIdxImageOccurrencesScope = `
CREATE INDEX IF NOT EXISTS idx_image_occurrences_scope
ON image_occurrences(scope_type, chat_id, status, occurred_at DESC)
`

// _ensure_image_memory_tables · executescript #5
const ddlIdxImageOccurrencesAsset = `
CREATE INDEX IF NOT EXISTS idx_image_occurrences_asset
ON image_occurrences(asset_id, status)
`

// _ensure_image_memory_tables · executescript #6
const ddlIdxImageOccurrencesInstallation = `
CREATE INDEX IF NOT EXISTS idx_image_occurrences_installation
ON image_occurrences(installation_id, status)
`

// _ensure_image_memory_tables · executescript #7
const ddlImageObservations = `
CREATE TABLE IF NOT EXISTS image_observations (
    observation_id TEXT PRIMARY KEY,
    occurrence_id TEXT NOT NULL,
    text TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    confirm_status TEXT NOT NULL,
    evidence_json TEXT NOT NULL DEFAULT '{}',
    version INTEGER NOT NULL DEFAULT 1,
    supersedes_id TEXT NOT NULL DEFAULT '',
    superseded_by TEXT NOT NULL DEFAULT '',
    created_at REAL NOT NULL,
    FOREIGN KEY (occurrence_id) REFERENCES image_occurrences(occurrence_id) ON DELETE CASCADE
)
`

// _ensure_image_memory_tables · executescript #8
const ddlIdxImageObservationsOccurrence = `
CREATE INDEX IF NOT EXISTS idx_image_observations_occurrence
ON image_observations(occurrence_id, created_at)
`

// _ensure_image_memory_tables · executescript #9
const ddlImageMemoryLinks = `
CREATE TABLE IF NOT EXISTS image_memory_links (
    link_id TEXT PRIMARY KEY,
    occurrence_id TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    link_kind TEXT NOT NULL,
    evidence_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    UNIQUE(occurrence_id, target_type, target_id),
    FOREIGN KEY (occurrence_id) REFERENCES image_occurrences(occurrence_id) ON DELETE CASCADE
)
`

// _ensure_image_memory_tables · executescript #10
const ddlIdxImageMemoryLinksTarget = `
CREATE INDEX IF NOT EXISTS idx_image_memory_links_target
ON image_memory_links(target_type, target_id, status)
`

// _ensure_image_memory_tables · executescript #11
const ddlImageDescriptionCompensations = `
CREATE TABLE IF NOT EXISTS image_description_compensations (
    occurrence_id TEXT NOT NULL,
    description_hash TEXT NOT NULL,
    summary_external_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until REAL NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    PRIMARY KEY(occurrence_id, description_hash),
    FOREIGN KEY (occurrence_id) REFERENCES image_occurrences(occurrence_id) ON DELETE CASCADE
)
`

// _ensure_image_memory_tables · executescript #12
const ddlIdxImageDescriptionCompensationsClaim = `
CREATE INDEX IF NOT EXISTS idx_image_description_compensations_claim
ON image_description_compensations(status, lease_until, attempt_count, created_at)
`

// _ensure_image_memory_tables · executescript #13
const ddlImagePendingDescriptions = `
CREATE TABLE IF NOT EXISTS image_pending_descriptions (
    content_hash TEXT NOT NULL,
    description_hash TEXT NOT NULL,
    text TEXT NOT NULL,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    PRIMARY KEY(content_hash, description_hash)
)
`

// _ensure_image_memory_tables · executescript #14
const ddlImageEmbeddings = `
CREATE TABLE IF NOT EXISTS image_embeddings (
    asset_id TEXT NOT NULL,
    fingerprint_hash TEXT NOT NULL,
    vector_id TEXT NOT NULL,
    generation INTEGER NOT NULL,
    dimension INTEGER NOT NULL,
    status TEXT NOT NULL,
    updated_at REAL NOT NULL,
    PRIMARY KEY(asset_id, fingerprint_hash),
    FOREIGN KEY (asset_id) REFERENCES image_assets(asset_id) ON DELETE CASCADE
)
`

// _ensure_image_memory_tables · executescript #15
const ddlImageEmbeddingJobs = `
CREATE TABLE IF NOT EXISTS image_embedding_jobs (
    job_id TEXT PRIMARY KEY,
    asset_id TEXT NOT NULL,
    fingerprint_hash TEXT NOT NULL,
    generation INTEGER NOT NULL,
    status TEXT NOT NULL,
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until REAL NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    UNIQUE(asset_id, fingerprint_hash, generation),
    FOREIGN KEY (asset_id) REFERENCES image_assets(asset_id) ON DELETE CASCADE
)
`

// _ensure_image_memory_tables · executescript #16
const ddlIdxImageEmbeddingJobsClaim = `
CREATE INDEX IF NOT EXISTS idx_image_embedding_jobs_claim
ON image_embedding_jobs(status, lease_until, attempt_count, created_at)
`

// _ensure_image_memory_tables · executescript #17
const ddlImageRuntimeState = `
CREATE TABLE IF NOT EXISTS image_runtime_state (
    singleton_id INTEGER PRIMARY KEY CHECK(singleton_id = 1),
    generation INTEGER NOT NULL,
    fingerprint_hash TEXT NOT NULL,
    dimension INTEGER NOT NULL,
    status TEXT NOT NULL,
    updated_at REAL NOT NULL
);
`
