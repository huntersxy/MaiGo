package memruntime

// 内核依赖的运行时服务接口，对齐 core/runtime/services 下的运行时类服务

import (
	"context"

	"maibot-go/internal/A_memorix/core/storage"
)

// RuntimeLifecycleService 对齐 MemoryRuntimeLifecycleService
type RuntimeLifecycleService interface {
	Initialize(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Close()
}

// RequestDedupService 对齐 MemoryRequestDedupService
type RequestDedupService interface {
	ExecuteRequestWithDedup(ctx context.Context, requestKey string, executor func() (map[string]any, error)) (bool, map[string]any, error)
}

// SummaryService 对齐 MemorySummaryService
type SummaryService interface {
	SummarizeChatStream(ctx context.Context, params SummarizeChatStreamParams) (map[string]any, error)
}

// SummarizeChatStreamParams 对齐 summarize_chat_stream 的关键字参数
type SummarizeChatStreamParams struct {
	ChatID             string
	ContextLength      *int
	IncludePersonality *bool
	TimeEnd            *float64
	Metadata           map[string]any
}

// IngestTextParams 对齐 ingest_text 的关键字参数
type IngestTextParams struct {
	ExternalID    string
	SourceType    string
	Text          string
	ChatID        string
	PersonIDs     []string
	Participants  []string
	Timestamp     *float64
	TimeStart     *float64
	TimeEnd       *float64
	Tags          []string
	Metadata      map[string]any
	Entities      []string
	Relations     []map[string]any
	// RespectFilter 为空表示未设置，按 Python 默认 true 处理
	RespectFilter *bool
	UserID        string
	GroupID       string
}

// IngestSummaryParams 对齐 ingest_summary 的关键字参数
type IngestSummaryParams struct {
	ExternalID    string
	ChatID        string
	Text          string
	Participants  []string
	TimeStart     *float64
	TimeEnd       *float64
	Tags          []string
	Metadata      map[string]any
	// RespectFilter 为空表示未设置，按 Python 默认 true 处理
	RespectFilter *bool
	UserID        string
	GroupID       string
}

// IngestService 对齐 MemoryIngestService
type IngestService interface {
	IngestSummary(ctx context.Context, params IngestSummaryParams) (map[string]any, error)
	IngestText(ctx context.Context, params IngestTextParams) (map[string]any, error)
	ProcessEpisodeSourceRebuildBatch(ctx context.Context, params map[string]any) (map[string]any, error)
	WriteParagraphVectorOrEnqueue(ctx context.Context, paragraphHash string, content string, contextText string) (map[string]any, error)
	EnsureVectorForText(ctx context.Context, params map[string]any) (bool, error)
	EnsureRelationVector(ctx context.Context, payload map[string]any) (bool, error)
	EnsureParagraphVector(ctx context.Context, payload map[string]any) (bool, error)
	EnsureEntityVector(ctx context.Context, payload map[string]any) (bool, error)
	RestoreRelationHashes(ctx context.Context, payload map[string]any) (map[string]any, error)
}

// SearchService 对齐 MemorySearchService
type SearchService interface {
	SearchMemory(ctx context.Context, request KernelSearchRequest) (map[string]any, error)
	AggregateSearch(ctx context.Context, query string, limit int, request KernelSearchRequest) (map[string]any, error)
	AggregateTime(ctx context.Context, query string, limit int, request KernelSearchRequest, window NormalizedSearchTimeWindow) (map[string]any, error)
	AggregateEpisode(ctx context.Context, query string, limit int, request KernelSearchRequest, window NormalizedSearchTimeWindow) (map[string]any, error)
	SearchExecutionOnce(ctx context.Context, params map[string]any) (map[string]any, error)
	SearchExecutionForChatScope(ctx context.Context, params map[string]any) (map[string]any, error)
	EpisodeQueryForChatScope(ctx context.Context, params map[string]any) (any, error)
}

// SearchHitService 对齐 MemorySearchHitProcessingService
type SearchHitService interface {
	RelationStatusIsInactive(ctx context.Context, payload map[string]any) (any, error)
	LoadParagraphStaleMarks(ctx context.Context, payload map[string]any) (any, error)
	ParagraphHiddenByStaleMarks(ctx context.Context, payload map[string]any) (any, error)
	FilterEpisodeHits(ctx context.Context, payload map[string]any) (any, error)
	FilterUserVisibleHits(ctx context.Context, payload map[string]any) (any, error)
	FilterCurrentEffectiveHits(ctx context.Context, payload map[string]any) (any, error)
	CurrentEffectiveFilterStoreCheckNeeded(ctx context.Context, payload map[string]any) (any, error)
	FilterHitsByMemoryChangeMetadata(ctx context.Context, payload map[string]any) (any, error)
	ChatSource(ctx context.Context, payload map[string]any) (any, error)
	ChatSourceForSearchScope(ctx context.Context, payload map[string]any) (any, error)
	ScopedSearchLimit(ctx context.Context, payload map[string]any) (any, error)
	ResolveAllowedChatIDs(ctx context.Context, payload map[string]any) (any, error)
	RankScoreFromItem(ctx context.Context, payload map[string]any) (any, error)
	DedupeRankedItems(ctx context.Context, payload map[string]any) (any, error)
	ParagraphMatchesChatScope(ctx context.Context, payload map[string]any) (any, error)
	HitMetadataMatchesChatScope(ctx context.Context, payload map[string]any) (any, error)
	ExtendChatScopeIDs(ctx context.Context, payload map[string]any) (any, error)
	MetadataChatScopeIDs(ctx context.Context, payload map[string]any) (any, error)
	FilterHitsByChatScope(ctx context.Context, payload map[string]any) (any, error)
	FilterHitsByRetrievalTypeScope(ctx context.Context, payload map[string]any) (any, error)
	RetrievalFilterKind(ctx context.Context, payload map[string]any) (any, error)
	SourceStreamID(ctx context.Context, payload map[string]any) (any, error)
	RetrievalFilterContext(ctx context.Context, payload map[string]any) (any, error)
	CurrentRetrievalFilterContext(ctx context.Context, payload map[string]any) (any, error)
	RetrievalFilterContextIsCurrentSource(ctx context.Context, payload map[string]any) (any, error)
	RetrievalFilterContextAllowed(ctx context.Context, payload map[string]any) (any, error)
	NormalizeSearchTimeBound(ctx context.Context, payload map[string]any) (any, error)
	NormalizeSearchTimeWindow(ctx context.Context, payload map[string]any) (any, error)
	RetrievalResultHit(ctx context.Context, payload map[string]any) (any, error)
	EpisodeHit(ctx context.Context, payload map[string]any) (any, error)
	Summary(ctx context.Context, payload map[string]any) (any, error)
	FilterHits(ctx context.Context, payload map[string]any) (any, error)
	HasEnabledRetrievalTypeFilter(ctx context.Context) bool
	RetrievalTypeFilterRoot(ctx context.Context) map[string]any
	RetrievalTypeFilterConfig(ctx context.Context, kind string) map[string]any
	RetrievalFilterContextsForHit(ctx context.Context, hit map[string]any, paragraphMap map[string]map[string]any, relationParagraphMap map[string][]map[string]any) []map[string]string
	RetrievalFilterContextFromHit(ctx context.Context, hit map[string]any) map[string]string
	RetrievalFilterContextFromParagraph(ctx context.Context, paragraph map[string]any) map[string]string
	FilterActiveRelationHits(ctx context.Context, hits []map[string]any) ([]map[string]any, error)
}

// ChatFilterService 对齐 MemoryChatFilterService
type ChatFilterService interface {
	IsChatEnabled(ctx context.Context, streamID string, groupID string, userID string) bool
	ChatFilterConfigAllows(ctx context.Context, filterConfig map[string]any, streamID string, groupID string, userID string, defaultWhenEmpty bool) bool
	IsChatFiltered(ctx context.Context, respectFilter bool, streamID string, groupID string, userID string) bool
}

// RuntimeConfigService 对齐 MemoryRuntimeConfigService
type RuntimeConfigService interface {
	BuildRuntimeConfig(ctx context.Context, baseConfig map[string]any) (map[string]any, error)
	ApplyRetrievalTuningProfile(ctx context.Context, profile map[string]any, validate bool) (map[string]any, error)
}

// RuntimeDependencyService 对齐 MemoryRuntimeDependencyService
type RuntimeDependencyService interface {
	RefreshRelationWriteService(ctx context.Context)
	Persist(ctx context.Context, forceVectors bool) error
}

// EmbeddingStateService 对齐 MemoryEmbeddingStateService
type EmbeddingStateService interface {
	StoredVectorDimension(ctx context.Context, store *storage.VectorStore) *int
	CurrentEmbeddingStatusDimension(ctx context.Context) int
	CurrentEmbeddingFingerprint(ctx context.Context, dimension *int) map[string]any
	CurrentEmbeddingFingerprintForValidation(ctx context.Context, dimension *int) map[string]any
	StoredEmbeddingFingerprint(ctx context.Context, store *storage.VectorStore) map[string]any
	StampMissingEmbeddingFingerprintIfDimensionMatches(ctx context.Context, store *storage.VectorStore) bool
	StoredVectorsCompatibleWithCurrentEmbedding(ctx context.Context, store *storage.VectorStore) bool
	VectorMismatchError(ctx context.Context, storedDimension int, detectedDimension int) string
	EmbeddingFallbackEnabled(ctx context.Context) bool
	AllowMetadataOnlyWrite(ctx context.Context) bool
	EmbeddingProbeIntervalSeconds(ctx context.Context) float64
	ParagraphVectorBackfillEnabled(ctx context.Context) bool
	ParagraphVectorBackfillIntervalSeconds(ctx context.Context) float64
	ParagraphVectorBackfillBatchSize(ctx context.Context) int
	ParagraphVectorBackfillMaxRetry(ctx context.Context) int
	IsEmbeddingDegraded(ctx context.Context) bool
	EmbeddingDegradedSnapshot(ctx context.Context) map[string]any
	SetEmbeddingDegraded(ctx context.Context, active bool, reason string, checkedAt *float64)
	ApplyRuntimeSparseMode(ctx context.Context)
	RefreshRuntimeSelfCheck(ctx context.Context, sampleText string) (map[string]any, error)
	MarkStartupSelfCheckDeferred(ctx context.Context)
	IsStartupSelfCheckDeferred(ctx context.Context) bool
	ApplySelfCheckDimensionResult(ctx context.Context, report map[string]any) string
	EnqueueParagraphVectorBackfill(ctx context.Context, paragraphHash string, errorText string)
	ParagraphVectorBackfillCounts(ctx context.Context) map[string]int
	RunParagraphBackfillOnce(ctx context.Context, limit int) (map[string]any, error)
	NormalizeEmbeddingFingerprint(value any) map[string]any
	EmbeddingFingerprintStatus(current map[string]any, stored map[string]any, hasStoredVectors bool) string
	SelfCheckEffectiveDimension(report map[string]any) int
}

// respectFilterOrDefault 归一内核参数：未设置按 Python 默认 true
func respectFilterOrDefault(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}
