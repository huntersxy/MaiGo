package memorix

import "time"

// Hit 对齐 memory_service.MemoryHit.to_dict 的常用字段（chat/工具消费）
type Hit struct {
	Content   string         `json:"content"`
	Score     float64        `json:"score"`
	HitType   string         `json:"type"`
	Source    string         `json:"source"`
	Hash      string         `json:"hash"`
	Metadata  map[string]any `json:"metadata"`
	EpisodeID string         `json:"episode_id"`
	Title     string         `json:"title"`
}

// SearchResult 对齐 host_service search_memory / memory_service._coerce_search_result
type SearchResult struct {
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
	Summary  string `json:"summary"`
	Hits     []Hit  `json:"hits"`
	Filtered bool   `json:"filtered"`
	Disabled bool   `json:"disabled,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// SearchRequest 对齐 MemoryService.search → invoke("search_memory", …)
type SearchRequest struct {
	Query         string
	Limit         int
	Mode          string
	ChatID        string
	PersonID      string
	TimeStart     any
	TimeEnd       any
	RespectFilter bool
	UserID        string
	GroupID       string
}

// ReadRequest 读单条记忆；id 可能是 episode/paragraph id
type ReadRequest struct {
	ID        uint
	EpisodeID string
}

// ReadResult 读单条记忆的详情载荷
type ReadResult struct {
	Success  bool           `json:"success"`
	Error    string         `json:"error,omitempty"`
	Disabled bool           `json:"disabled,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	ItemID   uint           `json:"item_id,omitempty"`
	Content  string         `json:"content"`
	Label    string         `json:"label"`
	Metadata map[string]any `json:"metadata"`
}

// IngestMessageRequest 对齐 ingest_text 的聊天写入子集（非 ingest_summary）
type IngestMessageRequest struct {
	ExternalID    string
	SourceType    string
	Text          string
	ChatID        string
	PersonIDs     []string
	Participants  []string
	Timestamp     *time.Time
	TimeStart     *time.Time
	TimeEnd       *time.Time
	Tags          []string
	Metadata      map[string]any
	RespectFilter bool
	UserID        string
	GroupID       string
	MemoryType    string
	Label         string
}

// IngestResult 对齐 MemoryWriteResult / ingest_text 响应
type IngestResult struct {
	Success    bool     `json:"success"`
	StoredIDs  []string `json:"stored_ids"`
	SkippedIDs []string `json:"skipped_ids"`
	Detail     string   `json:"detail"`
	Disabled   bool     `json:"disabled,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Queued     bool     `json:"queued,omitempty"`
}

// AdminListRequest 管理面列表；Python 侧分散在 memory_source_admin.list、episode_admin 等
type AdminListRequest struct {
	Domain string
	Action string
	Limit  int
	Cursor string
	Status string
}

// AdminListItem 列表行（episode / 记忆摘要）
type AdminListItem struct {
	ID            uint      `json:"id"`
	SubjectType   string    `json:"subject_type,omitempty"`
	SubjectTarget string    `json:"subject_target,omitempty"`
	MemoryType    string    `json:"memory_type,omitempty"`
	Label         string    `json:"label"`
	Preview       string    `json:"preview"`
	Status        string    `json:"status"`
	UpdatedAt     time.Time `json:"updated_at"`
	EpisodeID     string    `json:"episode_id,omitempty"`
	EpisodeKind   string    `json:"episode_kind,omitempty"`
	HonestNote    string    `json:"honest_note,omitempty"`
}

// AdminListResult 管理列表分页
type AdminListResult struct {
	Success    bool            `json:"success"`
	Error      string          `json:"error,omitempty"`
	Disabled   bool            `json:"disabled,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Items      []AdminListItem `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
	HasMore    bool            `json:"has_more"`
}

// HealthResult 对齐 memory_runtime_admin + host_service._startup_status_payload 的常用字段
type HealthResult struct {
	Success             bool           `json:"success"`
	Enabled             bool           `json:"enabled"`
	MemoryEnabled       bool           `json:"memory_enabled"`
	Disabled            bool           `json:"disabled,omitempty"`
	Reason              string         `json:"reason,omitempty"`
	RuntimeReady        bool           `json:"runtime_ready"`
	RetrievalReady      bool           `json:"retrieval_ready"`
	Degraded            bool           `json:"degraded"`
	DegradedReasons     []string       `json:"degraded_reasons,omitempty"`
	MissingServices     []string       `json:"missing_services,omitempty"`
	RetrievalMode       string         `json:"retrieval_mode"`
	AvailableChannels   []string       `json:"available_channels"`
	UnavailableChannels []string       `json:"unavailable_channels"`
	VectorHealth        map[string]any `json:"vector_health"`
	Backend             string         `json:"backend"`
	Message             string         `json:"message,omitempty"`
	// 启动态字段对齐 host_service._startup_status_payload（/memory 健康与管理端可见）
	StartupState         string `json:"startup_state,omitempty"`
	Initializing         bool   `json:"initializing,omitempty"`
	InitializationFailed bool   `json:"initialization_failed,omitempty"`
	Error                string `json:"error,omitempty"`
	ErrorStage           string `json:"error_stage,omitempty"`
	StartupStartedAt     any    `json:"startup_started_at,omitempty"`
	StartupFinishedAt    any    `json:"startup_finished_at,omitempty"`
	StartupQueuePending  int    `json:"startup_queue_pending,omitempty"`
	DataDir              string `json:"data_dir,omitempty"`
}
