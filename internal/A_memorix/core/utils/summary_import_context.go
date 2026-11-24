package utils

// A_memorix 摘要导入宿主与配置访问，对齐 Python SummaryImporter 的初始化与幂等查询

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// SummaryImporterOptions 摘要导入器依赖
type SummaryImporterOptions struct {
	// Store 元数据存储
	Store SummaryImportStore
	// VectorStore 段落向量存储
	VectorStore SummaryVectorStore
	// GraphVectorStore 双池模式下的图谱向量存储，对齐 plugin_config["graph_vector_store"]
	GraphVectorStore SummaryVectorStore
	// GraphStore 图存储
	GraphStore SummaryGraphStore
	// Embedding 向量编码器
	Embedding SummaryEmbeddingManager
	// LLM 文本生成网关
	LLM TextGenerationGateway
	// Messages 聊天消息来源
	Messages SummaryMessageSource
	// IdentityStore 摘要身份存储
	IdentityStore SummaryIdentityStore
	// IdentityHost 摘要身份宿主
	IdentityHost SummaryIdentityHost
	// Prompt 提示词提供者
	Prompt SummaryPromptProvider
	// RelationWriter 关系写入服务，对齐 plugin_config["relation_write_service"]
	RelationWriter SummaryRelationWriter
	// DeduplicatorFactory 事实去重器工厂
	DeduplicatorFactory SummaryDedupFactory
	// KnowledgeTypeResolver 知识类型解析，未注入时按显式类型校验
	KnowledgeTypeResolver KnowledgeTypeResolver
	// PluginConfig 对齐 Python plugin_config 字典的纯配置部分
	PluginConfig map[string]any
	// Host 对齐 plugin_instance，按可选接口取用能力
	Host any
	// BotName 对齐 global_config.bot.nickname
	BotName string
	// Personality 对齐 global_config.personality.personality
	Personality string
	// SelfCheckCache 自检报告缓存，nil 时使用私有缓存
	SelfCheckCache *SelfCheckCache
	// Logger 日志注入
	Logger Logger
}

// SummaryImporter 总结并导入知识的工具类，对齐 SummaryImporter
type SummaryImporter struct {
	store            SummaryImportStore
	vectorStore      SummaryVectorStore
	graphVectorStore SummaryVectorStore
	graphStore       SummaryGraphStore
	embedding        SummaryEmbeddingManager
	llm              TextGenerationGateway
	messages         SummaryMessageSource
	identityStore    SummaryIdentityStore
	identityHost     SummaryIdentityHost
	prompt           SummaryPromptProvider
	relationWriter   SummaryRelationWriter
	dedupFactory     SummaryDedupFactory
	knowledgeTypes   KnowledgeTypeResolver
	pluginConfig     map[string]any
	host             any
	botName          string
	personality      string
	selfCheckCache   *SelfCheckCache
	logger           Logger
	locks            sync.Map
}

// NewSummaryImporter 创建摘要导入器
func NewSummaryImporter(options SummaryImporterOptions) *SummaryImporter {
	pluginConfig := options.PluginConfig
	if pluginConfig == nil {
		pluginConfig = map[string]any{}
	}
	cache := options.SelfCheckCache
	if cache == nil {
		cache = &SelfCheckCache{}
	}
	return &SummaryImporter{
		store:            options.Store,
		vectorStore:      options.VectorStore,
		graphVectorStore: options.GraphVectorStore,
		graphStore:       options.GraphStore,
		embedding:        options.Embedding,
		llm:              options.LLM,
		messages:         options.Messages,
		identityStore:    options.IdentityStore,
		identityHost:     options.IdentityHost,
		prompt:           options.Prompt,
		relationWriter:   options.RelationWriter,
		dedupFactory:     options.DeduplicatorFactory,
		knowledgeTypes:   options.KnowledgeTypeResolver,
		pluginConfig:     pluginConfig,
		host:             options.Host,
		botName:          options.BotName,
		personality:      options.Personality,
		selfCheckCache:   cache,
		logger:           OrLogger(options.Logger),
	}
}

// lockFor 取聊天流或外部标识对应的互斥锁，对齐 WeakValueDictionary 锁表
func (i *SummaryImporter) lockFor(key string) *sync.Mutex {
	value, _ := i.locks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// allowMetadataOnlyWrite 对齐 _allow_metadata_only_write
func (i *SummaryImporter) allowMetadataOnlyWrite() bool {
	if i.host != nil {
		if getter, ok := i.host.(SummaryConfigHost); ok {
			return isTruthy(getter.GetConfig("embedding.fallback.allow_metadata_only_write", true))
		}
	}
	value := summaryConfigValue(i.pluginConfig, true, "embedding", "fallback", "allow_metadata_only_write")
	return isTruthy(value)
}

// dualVectorPoolsEnabled 对齐 _dual_vector_pools_enabled
func (i *SummaryImporter) dualVectorPoolsEnabled() bool {
	if i.host != nil {
		if checker, ok := i.host.(SummaryDualPoolsHost); ok {
			return checker.DualVectorPoolsEnabled()
		}
	}
	pools := summaryNestedMap(i.pluginConfig, "retrieval", "vector_pools")
	configured := "dual"
	if value, exists := pools["mode"]; exists && isTruthy(value) {
		configured = pyLower(pyStrip(pyStr(value)))
	}
	runtimeConfig := summaryNestedMap(i.pluginConfig, "runtime")
	if value, exists := runtimeConfig["vector_pools_ready"]; exists {
		return configured == "dual" && isTruthy(value)
	}
	return configured == "dual"
}

// paragraphVectorTarget 对齐 _graph_vector_store
func (i *SummaryImporter) graphVectorTarget() SummaryVectorStore {
	if i.dualVectorPoolsEnabled() && i.graphVectorStore != nil {
		return i.graphVectorStore
	}
	return i.vectorStore
}

// graphVectorID 对齐 _graph_vector_id
func graphVectorID(itemType string, hashValue string) string {
	return pyStrip(itemType) + ":" + pyStrip(hashValue)
}

// embeddingWriteBatchSize 对齐 _embedding_write_batch_size
func (i *SummaryImporter) embeddingWriteBatchSize() int {
	batchSize := 32
	if provider, ok := i.embedding.(SummaryEmbeddingBatchSizeProvider); ok {
		if value := provider.BatchSize(); value >= 1 {
			batchSize = value
		}
	}
	maxConcurrent := 1
	if provider, ok := i.embedding.(SummaryEmbeddingConcurrencyProvider); ok {
		if value := provider.MaxConcurrent(); value >= 1 {
			maxConcurrent = value
		}
	}
	limit := batchSize * maxConcurrent
	if limit > 512 {
		limit = 512
	}
	return limit
}

// imageEvidenceCatalog 对齐 _image_evidence_catalog
func (i *SummaryImporter) imageEvidenceCatalog(messages []SessionMessage, streamID string) (string, map[[2]string]struct{}, error) {
	lines := make([]string, 0, len(messages))
	allowed := make(map[[2]string]struct{}, len(messages))
	for _, message := range messages {
		messageID := pyStrip(message.MessageID)
		for _, componentPath := range imageComponentPaths(message.RawMessage.Components) {
			if messageID == "" {
				continue
			}
			key := [2]string{messageID, componentPath}
			allowed[key] = struct{}{}
			externalRef := buildChatExternalRef(streamID, messageID, componentPath)
			occurrence, err := i.store.GetImageOccurrenceByRef(externalRef)
			if err != nil {
				return "", nil, err
			}
			observations := make([]map[string]any, 0)
			if occurrence != nil {
				occurrenceID := pyStr(occurrence["occurrence_id"])
				observations, err = i.store.ListImageObservations([]string{occurrenceID})
				if err != nil {
					return "", nil, err
				}
			}
			descriptions := make([]string, 0, len(observations))
			for _, item := range observations {
				descriptions = append(descriptions, fmt.Sprintf("%s(%s): %s", pyStr(item["source_kind"]), pyStr(item["confirm_status"]), pyStr(item["text"])))
			}
			suffix := ""
			if len(descriptions) > 0 {
				suffix = "；图片上下文=" + strings.Join(descriptions, " | ")
			}
			lines = append(lines, fmt.Sprintf("- message_id=%s, component_path=%s%s", messageID, componentPath, suffix))
		}
	}
	if len(lines) == 0 {
		return "无", allowed, nil
	}
	return strings.Join(lines, "\n"), allowed, nil
}

// linkSummaryImageEvidence 对齐 _link_summary_image_evidence，返回关联条数
func (i *SummaryImporter) linkSummaryImageEvidence(streamID string, paragraphHash string, facts []summaryPayloadFact, allowed map[[2]string]struct{}) (int, error) {
	linked := 0
	for _, fact := range facts {
		for _, evidence := range fact.ImageEvidence {
			key := [2]string{evidence.MessageID, evidence.ComponentPath}
			if _, ok := allowed[key]; !ok {
				return 0, fmt.Errorf("总结模型返回了窗口外图片证据: %s:%s", key[0], key[1])
			}
			occurrence, err := i.store.GetImageOccurrenceByRef(buildChatExternalRef(streamID, key[0], key[1]))
			if err != nil {
				return 0, err
			}
			if occurrence == nil {
				continue
			}
			_, err = i.store.UpsertImageMemoryLink(SummaryImageMemoryLink{
				OccurrenceID: pyStr(occurrence["occurrence_id"]),
				TargetType:   "paragraph",
				TargetID:     paragraphHash,
				LinkKind:     "fact_evidence",
				Evidence:     map[string]any{"fact_id": fact.FactID, "fact_text": fact.Text},
			})
			if err != nil {
				return 0, err
			}
			linked++
		}
	}
	return linked, nil
}

// summaryReviewCount 对齐 _summary_review_count
func (i *SummaryImporter) summaryReviewCount(metadata map[string]any) int {
	rawValue, exists := metadata["summary_review_count"]
	if !exists || rawValue == nil {
		rawValue = summaryConfigValue(i.pluginConfig, 2, "summarization", "history_review_count")
	}
	parsed, err := pyFloat64(rawValue)
	if err != nil {
		return 2
	}
	count := int(parsed)
	if count < 0 {
		return 0
	}
	return count
}

// existingSummaryResult 对齐 _existing_summary_result，模型调用前解析 external ID
func (i *SummaryImporter) existingSummaryResult(streamID string, metadata map[string]any) (*SummaryImportResult, error) {
	if metadata == nil {
		return nil, nil
	}
	externalID := pyStrip(importValueText(metadata["external_id"]))
	if externalID == "" {
		return nil, nil
	}
	checkpoint, err := i.store.GetSummaryCheckpoint(externalID)
	if err != nil {
		return nil, err
	}
	if checkpoint != nil && pyStrip(pyStr(checkpoint["chat_id"])) != streamID {
		return nil, fmt.Errorf("摘要标识已绑定到其他聊天流")
	}
	if checkpoint != nil && pyStrip(pyStr(checkpoint["phase"])) == "pending" {
		return nil, nil
	}
	existing, err := i.store.GetExternalMemoryRef(externalID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		if checkpoint != nil {
			return &SummaryImportResult{
				Success: true,
				Detail:  "该窗口已完成空增量总结",
				Source:  "chat_summary:" + streamID,
				Skipped: true,
			}, nil
		}
		return nil, nil
	}
	paragraphHash := pyStrip(importValueText(existing["paragraph_hash"]))
	var paragraph map[string]any
	if paragraphHash != "" {
		paragraph, err = i.store.GetParagraph(paragraphHash)
		if err != nil {
			return nil, err
		}
	}
	if paragraph == nil || isTruthy(paragraph["is_deleted"]) {
		return nil, fmt.Errorf("摘要 external ID 指向无效段落: external_id=%s paragraph=%s", externalID, paragraphHash)
	}
	expectedSource := "chat_summary:" + streamID
	actualSource := pyStrip(importValueText(paragraph["source"]))
	refMetadata := summaryMetadata(existing["metadata"])
	refChatID := pyStrip(importValueText(refMetadata["chat_id"]))
	originMatches := actualSource == expectedSource
	if refChatID != "" {
		originMatches = pyStrip(pyStr(existing["source_type"])) == "chat_summary" && refChatID == streamID
	}
	if !originMatches {
		return nil, fmt.Errorf("摘要 external ID 已绑定到其他聊天流: external_id=%s expected=%s actual=%s", externalID, expectedSource, actualSource)
	}
	if checkpoint == nil && isTruthy(metadata["trigger_message_count"]) {
		triggerCount := summaryIntValue(metadata["trigger_message_count"], 0)
		if err := i.store.RecordSummaryCheckpoint(externalID, streamID, triggerCount, "complete", nil); err != nil {
			return nil, err
		}
	}
	return &SummaryImportResult{
		Success:       true,
		Detail:        "总结已存在，跳过重复生成",
		ParagraphHash: paragraphHash,
		Source:        expectedSource,
	}, nil
}

// buildPreviousSummaryContext 对齐 _build_previous_summary_context
func (i *SummaryImporter) buildPreviousSummaryContext(streamID string, limit int) string {
	if limit <= 0 {
		return ""
	}
	paragraphs, err := i.store.GetChatSummaryHistory(streamID)
	if err != nil {
		i.logger.Debug(fmt.Sprintf("读取历史摘要回顾失败: stream_id=%s error=%v", streamID, err))
		return ""
	}
	if len(paragraphs) == 0 {
		return ""
	}
	ordered := append([]map[string]any{}, paragraphs...)
	sort.SliceStable(ordered, func(left int, right int) bool {
		return paragraphCreatedAt(ordered[left]) > paragraphCreatedAt(ordered[right])
	})
	lines := make([]string, 0, limit)
	now := summaryNow()
	for _, paragraph := range ordered {
		paragraphMetadata := summaryMetadata(paragraph["metadata"])
		memoryChange := map[string]any{}
		if typed, ok := importObjectMap(paragraphMetadata["memory_change"]); ok {
			memoryChange = typed
		}
		if validTo := OptionalFloat(memoryChange["valid_to"]); validTo != nil && *validTo <= now {
			continue
		}
		cleaned := summaryCleanReviewSummary(importValueText(paragraph["content"]))
		if cleaned == "" {
			continue
		}
		lines = append(lines, "- "+cleaned)
		if len(lines) >= limit {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n历史净化摘要回顾（只用于理解上下文和识别变化，不得直接复制到本轮增量）：\n" +
		strings.Join(lines, "\n") + "\n"
}

// summaryIntValue 对齐 int(value or 0)
func summaryIntValue(value any, defaultValue int) int {
	if !isTruthy(value) {
		return defaultValue
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return defaultValue
	}
	return int(parsed)
}

// summaryBoolValue 对齐 bool(value)
func summaryBoolValue(value any, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return isTruthy(value)
}
