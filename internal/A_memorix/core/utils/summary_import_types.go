package utils

// A_memorix 聊天摘要导入公共类型，对齐 Python core/utils/summary_importer.py
// 存储、向量、图、模型、消息与提示词全部通过接口注入，避免 utils 反向依赖上游

import (
	"context"
)

// SUMMARY_PROMPT_NAME 对齐 Python 常量
const SummaryPromptName = "a_memorix_chat_summary"

// SummaryImportResult 聊天摘要导入结果，对齐 SummaryImportResult
type SummaryImportResult struct {
	Success       bool
	Detail        string
	ParagraphHash string
	Source        string
	Skipped       bool
}

// SummaryImportParams 对齐 import_from_stream 的关键字参数
type SummaryImportParams struct {
	ContextLength      *int
	IncludePersonality *bool
	TimeEnd            *float64
	Metadata           map[string]any
}

// SummaryVectorStore 摘要导入使用的向量通道
// 对齐 Python VectorStore 的 dimension/__contains__/add/save 子集
type SummaryVectorStore interface {
	Dimension() int
	Contains(id string) bool
	Add(vectors [][]float32, ids []string) (int, error)
	Save(embeddingFingerprint map[string]any) error
}

// SummaryGraphStore 摘要导入使用的图写入通道
// 对齐 Python GraphStore 的 add_nodes/add_edges/batch_update/save 子集
type SummaryGraphStore interface {
	AddNodes(nodes []string) error
	AddEdges(relations [][3]string, relationHashes []string) error
	BatchUpdate(fn func() error) error
	Save() error
}

// SummaryEmbeddingManager 摘要导入使用的向量编码能力
type SummaryEmbeddingManager interface {
	// Encode 单条编码，对齐 embedding_manager.encode
	Encode(ctx context.Context, text string) ([]float32, error)
	// EncodeBatch 批量编码，对齐 embedding_manager.encode_batch
	EncodeBatch(ctx context.Context, texts []string) ([][]float32, error)
	// DetectDimension 探测模型维度，对齐 _detect_dimension
	DetectDimension(ctx context.Context) (int, error)
}

// SummaryEmbeddingBatchSizeProvider 可选的批大小提供者
type SummaryEmbeddingBatchSizeProvider interface {
	BatchSize() int
}

// SummaryEmbeddingConcurrencyProvider 可选的并发数提供者
type SummaryEmbeddingConcurrencyProvider interface {
	MaxConcurrent() int
}

// SummaryEmbeddingFingerprintProvider 可选的向量指纹提供者
type SummaryEmbeddingFingerprintProvider interface {
	GetEmbeddingFingerprint(dimension int) map[string]any
}

// SummaryImageMemoryLink 图片与记忆关联，对齐 upsert_image_memory_link 的关键字参数
type SummaryImageMemoryLink struct {
	OccurrenceID string
	TargetType   string
	TargetID     string
	LinkKind     string
	Evidence     map[string]any
}

// SummaryImportStore 摘要导入所需存储能力
// FactDedupStore 提供去重与事务能力，其余方法对齐 Python metadata_store
type SummaryImportStore interface {
	FactDedupStore

	GetSummaryCheckpoint(externalID string) (map[string]any, error)
	RecordSummaryCheckpoint(externalID string, chatID string, triggerMessageCount int, phase string, result map[string]any) error
	GetExternalMemoryRef(externalID string) (map[string]any, error)
	UpsertExternalMemoryRef(externalID string, paragraphHash string, sourceType string, metadata map[string]any) (map[string]any, error)
	GetChatSummaryHistory(chatID string) ([]map[string]any, error)

	AddParagraph(content string, vectorIndex *int, source string, metadata map[string]any, knowledgeType string, timeMeta map[string]any) (string, error)
	// AddEntitiesForParagraph 事务内写入实体，对齐 Python 事务内的 add_entities_batch
	AddEntitiesForParagraph(names []string, sourceParagraph string) ([]string, error)
	// AddRelationsForParagraph 事务内写入关系，对齐 Python 事务内的 add_relations_batch
	AddRelationsForParagraph(relations [][3]string, confidence float64, sourceParagraph string) ([]string, error)
	UpdateParagraphMetadata(paragraphHash string, patch map[string]any, merge bool) (map[string]any, error)
	ComputeRelationHash(subject string, predicate string, object string) (string, error)

	EnqueueEpisodeSourceRebuild(source string, reason string) bool
	EnqueueParagraphVectorBackfill(paragraphHash string, errorText string) error
	EnqueuePersonProfileRefresh(personID string, reason string) error

	GetImageOccurrenceByRef(externalRef string) (map[string]any, error)
	ListImageObservations(occurrenceIDs []string) ([]map[string]any, error)
	UpsertImageMemoryLink(link SummaryImageMemoryLink) (map[string]any, error)
}

// SummaryMessageSource 聊天消息查询，对齐 message_api.get_messages_by_time_in_chat
type SummaryMessageSource interface {
	GetMessagesByTimeInChat(ctx context.Context, chatID string, startTime float64, endTime float64, limit int, limitMode string) ([]SessionMessage, error)
}

// SummaryIngestRequest 公共 ingest 入参，对齐 plugin.ingest_text 的关键字参数
type SummaryIngestRequest struct {
	ExternalID    string
	SourceType    string
	Text          string
	ChatID        string
	PersonIDs     []string
	TimeStart     *float64
	TimeEnd       *float64
	Metadata      map[string]any
	Entities      []string
	Relations     []map[string]string
	RespectFilter bool
}

// SummaryIngestHost 可选的公共 ingest 能力
type SummaryIngestHost interface {
	IngestText(ctx context.Context, req SummaryIngestRequest) (map[string]any, error)
}

// SummaryVectorWriter 可选的段落向量写入能力
type SummaryVectorWriter interface {
	WriteParagraphVectorOrEnqueue(ctx context.Context, paragraphHash string, content string, context string) (map[string]any, error)
}

// SummaryConfigHost 可选的配置读取能力，对齐 plugin.get_config
type SummaryConfigHost interface {
	GetConfig(key string, defaultValue any) any
}

// SummaryDualPoolsHost 可选的双向量池检查，对齐 plugin._dual_vector_pools_enabled
type SummaryDualPoolsHost interface {
	DualVectorPoolsEnabled() bool
}

// SummaryPersistHost 可选的向量持久化能力，对齐 plugin.persist_vector_store
type SummaryPersistHost interface {
	PersistVectorStore(store SummaryVectorStore)
}

// SummaryBackfillHost 可选的向量回填入队能力
type SummaryBackfillHost interface {
	EnqueueParagraphVectorBackfill(paragraphHash string, errorText string) error
}

// SummarySelfCheckHost 可选的运行时自检能力
type SummarySelfCheckHost interface {
	EnsureRuntimeSelfCheck(ctx context.Context) RuntimeSelfCheckReport
}

// SummaryRelationWriter 关系写入服务，对齐 RelationWriteService.upsert_relations_with_vectors
type SummaryRelationWriter interface {
	UpsertRelationsWithVectors(ctx context.Context, relations [][3]string, confidence float64, sourceParagraph string, writeVector bool) error
}

// SummaryPromptProvider 提示词模板与渲染，对齐 load_prompt_template/load_prompt
type SummaryPromptProvider interface {
	// Template 返回原始模板，用于检查占位符
	Template(name string) (string, error)
	// Render 渲染提示词
	Render(name string, variables map[string]any) (string, error)
}

// SummaryDedupFactory 构造事实去重器，任务名由摘要模型解析结果决定
type SummaryDedupFactory func(taskName string) *FactDeduplicator
