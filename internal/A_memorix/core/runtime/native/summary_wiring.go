package native

// 摘要服务装配，对齐 Python MemorySummaryService
// 摘要导入依赖宿主消息源、LLM 网关与提示词，缺失时由导入器显式返回未接入错误

import (
	"context"
	"strings"

	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/runtime/services"
	"maibot-go/internal/A_memorix/core/utils"
)

// summaryAdapter 把服务层参数收敛为内核参数
type summaryAdapter struct {
	inner *services.SummaryService
}

// SummarizeChatStream 转调服务层摘要实现
func (a *summaryAdapter) SummarizeChatStream(ctx context.Context, params memruntime.SummarizeChatStreamParams) (map[string]any, error) {
	return a.inner.SummarizeChatStream(ctx, services.SummarizeChatStreamParams{
		ChatID: params.ChatID, ContextLength: params.ContextLength,
		IncludePersonality: params.IncludePersonality, TimeEnd: params.TimeEnd, Metadata: params.Metadata,
	})
}

// summaryServiceDeps 构造摘要服务依赖
func (r *Runtime) summaryServiceDeps() services.SummaryDeps {
	deps := services.SummaryDeps{
		Initialize: func(ctx context.Context) error {
			return r.kernel.Initialize(ctx)
		},
		Persist: r.persistKernel,
		BuildSource: func(sourceType, chatID string, personIDs []string) string {
			return utils.BuildSource(sourceType, chatID, personIDs)
		},
	}
	importer := r.newSummaryImporter()
	if importer == nil {
		return deps
	}
	deps.ImportFromStream = func(ctx context.Context, params services.SummaryImportParams) (*services.SummaryImportResult, error) {
		result := importer.ImportFromStream(ctx, params.StreamID, utils.SummaryImportParams{
			ContextLength: params.ContextLength, IncludePersonality: params.IncludePersonality,
			TimeEnd: params.TimeEnd, Metadata: params.Metadata,
		})
		return &services.SummaryImportResult{
			Success: result.Success, Detail: result.Detail,
			ParagraphHash: result.ParagraphHash, Skipped: result.Skipped, Source: result.Source,
		}, nil
	}
	return deps
}

// newSummaryImporter 按当前存储与宿主能力构造摘要导入器
func (r *Runtime) newSummaryImporter() *utils.SummaryImporter {
	if r.metadataStore == nil || r.vectorStore == nil {
		return nil
	}
	store := summaryStoreAdapter{store: r.metadataStore}
	options := utils.SummaryImporterOptions{
		Store:         store,
		VectorStore:   &webImportVectorAdapter{inner: r.vectorStore},
		GraphStore:    &webImportGraphAdapter{inner: r.graphStore},
		Embedding:     &webImportEmbeddingAdapter{inner: r.embeddingManager},
		LLM:           r.llmGateway,
		Messages:      r.messageSource,
		IdentityStore: summaryIdentityStoreAdapter{store: r.metadataStore},
		IdentityHost: summaryIdentityHostAdapter{
			store: r.metadataStore, findMessages: r.findMessages,
			botSelfIDs: r.botSelfIDs, botSelfResolver: r.botSelfResolver,
		},
		Prompt:              r.summaryPrompt(),
		DeduplicatorFactory: r.summaryDedupFactory(store),
		PluginConfig:        r.kernel.Config(),
		ModelTasks:          r.modelTasks,
		ModelDict:           r.modelDict,
		BotName:             r.summaryBotName(),
		Personality:         r.summaryPersonality(),
		Logger:              r.logger,
	}
	if graphStore := r.graphPoolStore(); graphStore != nil {
		options.GraphVectorStore = &webImportVectorAdapter{inner: graphStore}
	}
	if r.relationWriteService != nil {
		options.RelationWriter = &webImportRelationWriterAdapter{inner: r.relationWriteService}
	}
	return utils.NewSummaryImporter(options)
}

// summaryPrompt 返回提示词提供者
func (r *Runtime) summaryPrompt() utils.SummaryPromptProvider {
	if r.promptProvider != nil {
		return r.promptProvider
	}
	return newSummaryPromptProvider()
}

// summaryBotName 读取机器人昵称
func (r *Runtime) summaryBotName() string {
	if strings.TrimSpace(r.botName) != "" {
		return strings.TrimSpace(r.botName)
	}
	config := r.kernel.Config()
	if bot, ok := config["bot"].(map[string]any); ok {
		return strings.TrimSpace(utils.PyStr(bot["nickname"]))
	}
	return ""
}

// summaryPersonality 读取人格设定
func (r *Runtime) summaryPersonality() string {
	if strings.TrimSpace(r.personality) != "" {
		return strings.TrimSpace(r.personality)
	}
	config := r.kernel.Config()
	if personality, ok := config["personality"].(map[string]any); ok {
		return strings.TrimSpace(utils.PyStr(personality["personality"]))
	}
	return ""
}

// summaryDedupFactory 构造事实去重器工厂
func (r *Runtime) summaryDedupFactory(store summaryStoreAdapter) utils.SummaryDedupFactory {
	return func(taskName string) *utils.FactDeduplicator {
		var embedding utils.FactDedupEmbedding
		if r.embeddingManager != nil {
			embedding = &webImportEmbeddingAdapter{inner: r.embeddingManager}
		}
		var vectors utils.FactDedupVectorStore
		if r.vectorStore != nil {
			vectors = summarySearchAdapter{inner: r.vectorStore}
		}
		var llm utils.FactDedupLLM
		if r.llmGateway != nil {
			llm = summaryDedupLLM{gateway: r.llmGateway}
		}
		prompt := r.summaryPrompt()
		builder := func(payloadJSON string) (string, error) {
			return prompt.Render("a_memorix_fact_deduplication", map[string]any{"payload": payloadJSON})
		}
		return utils.NewFactDeduplicator(store, embedding, vectors, llm, builder, taskName)
	}
}

// summarySearchAdapter 把向量存储适配为去重检索端口
type summarySearchAdapter struct {
	inner interface {
		Search(query []float32, k int, filterDeleted bool, allowedIDs []string) ([]string, []float64, error)
	}
}

// Search 返回命中的段落哈希
func (a summarySearchAdapter) Search(ctx context.Context, vector []float32, k int, allowedIDs []string) ([]string, error) {
	ids, _, err := a.inner.Search(vector, k, false, allowedIDs)
	return ids, err
}

// summaryDedupLLM 把宿主文本网关适配为去重模型端口
type summaryDedupLLM struct {
	gateway utils.TextGenerationGateway
}

// Generate 转调宿主文本生成
func (a summaryDedupLLM) Generate(ctx context.Context, req utils.FactDedupLLMRequest) (utils.FactDedupLLMResponse, error) {
	temperature := req.Temperature
	result, err := a.gateway.Generate(ctx, utils.TextGenerationRequest{
		TaskName: req.TaskName, RequestType: req.RequestType, Prompt: req.Prompt, Temperature: &temperature,
	})
	if err != nil {
		return utils.FactDedupLLMResponse{}, err
	}
	return utils.FactDedupLLMResponse{Success: result.Success, Error: result.Error, Completion: result.Completion}, nil
}
