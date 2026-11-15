package native

// 服务依赖端口装配：检索、写入、画像、维护、统计
// 所有端口在调用时读取本包当前存储与内核状态，初始化后无需重新接线

import (
	"context"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/retrieval"
	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/runtime/services"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// searchDeps 绑定统一检索服务的真实端口
func (r *Runtime) searchDeps() services.SearchDeps {
	return services.SearchDeps{
		IsChatFiltered: func(respectFilter bool, streamID string, groupID string, userID string) bool {
			return r.kernel.IsChatFiltered(context.Background(), respectFilter, streamID, groupID, userID)
		},
		Initialize: func(ctx context.Context) error { return r.kernel.Initialize(ctx) },
		ExecuteSearch: func(ctx context.Context, request services.SearchExecutionRequest) (services.SearchExecutionResult, error) {
			config, err := r.buildRuntimeConfig()
			if err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 构造检索运行时配置失败: %v", err))
				config = map[string]any{}
			}
			result := retrieval.ExecuteSearch(ctx, r.retriever, r.thresholdFilter, config, toRetrievalRequest(request), request.EnforceChatFilt)
			return toServicesSearchResult(result), nil
		},
		EpisodeQueryForChatScope: r.episodeQueryForChatScope,
		NormalizeTimeWindow:      services.BuildSearchTimeWindow,
		FilterEpisodeHits: func(hits []map[string]any) []map[string]any {
			return r.filterHitsBySearchHit("episode", hits, nil)
		},
		FilterHits: func(hits []map[string]any, personID string) []map[string]any {
			return r.filterHitsBySearchHit("person", hits, map[string]any{"person_id": personID})
		},
		FilterUserVisibleHits: func(hits []map[string]any) []map[string]any {
			return r.filterHitsBySearchHit("visible", hits, nil)
		},
		FilterHitsByRetrievalTypeScope: func(hits []map[string]any, streamID string, groupID string, userID string) []map[string]any {
			return r.filterHitsBySearchHit("scope", hits, map[string]any{
				"current_stream_id": streamID, "current_group_id": groupID, "current_user_id": userID,
			})
		},
		ResolveRetrievalScope: func(ctx context.Context, chatID string, sharedChatIDs []string) (*services.RetrievalScope, error) {
			if r.metadataStore == nil {
				return nil, nil
			}
			return services.ResolveRetrievalScopeForStore(r.metadataStore, chatID, sharedChatIDs)
		},
		BuildRuntimeConfig: func(ctx context.Context) map[string]any {
			config, err := r.buildRuntimeConfig()
			if err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 构造检索运行时配置失败: %v", err))
				return map[string]any{}
			}
			return config
		},
		ReinforceAccess: func(ctx context.Context, relationHashes []string) {
			facade := r.kernel.RuntimeFacade()
			if facade == nil {
				return
			}
			if err := facade.ReinforceAccess(ctx, relationHashes); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 强化关系访问失败: %v", err))
			}
		},
		RuntimeCapabilityStatus: r.kernel.RuntimeCapabilityStatus,
	}
}

// ingestDeps 绑定记忆写入服务的真实端口，缺关系写入实现时返回真实错误
func (r *Runtime) ingestDeps() services.IngestDeps {
	return services.IngestDeps{
		IsChatFiltered: func(respectFilter bool, streamID string, groupID string, userID string) bool {
			return r.kernel.IsChatFiltered(context.Background(), respectFilter, streamID, groupID, userID)
		},
		Initialize: func(ctx context.Context) error { return r.kernel.Initialize(ctx) },
		Metadata:   func() *storage.Store { return r.metadataStore },
		// Episode 物化：后台循环按来源重建，缺这三项会每轮报 generation_hash 为空
		GenerationSignature: r.episodeGenerationSignature,
		GenerationHash:      r.episodeGenerationHash,
		PlanSourceRebuild:   r.planEpisodeSourceRebuild,
		Persist: func() {
			if err := r.kernel.Persist(context.Background(), false); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 持久化内核状态失败: %v", err))
			}
		},
		MarkPersonActive: func(personID string) {
			r.markPersonActiveSeen(personID)
		},
		EnqueuePersonProfileRefresh: func(personID string, reason string) {
			servicesRef := r.kernel.Services()
			if servicesRef == nil || servicesRef.ProfileAdmin == nil {
				return
			}
			servicesRef.ProfileAdmin.EnqueuePersonProfileRefresh(context.Background(), personID, reason)
		},
		EnqueueParagraphVectorBackfill: func(paragraphHash string, errorText string) {
			store := r.metadataStore
			if store == nil {
				return
			}
			if err := store.EnqueueParagraphVectorBackfill(paragraphHash, nil, errorText, nil); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 登记段落向量回填失败: %v", err))
			}
		},
		AllowMetadataOnlyWrite: func() bool {
			return memruntime.PyTruthy(r.kernel.Cfg("embedding.fallback.allow_metadata_only_write", true))
		},
		EmbeddingFallbackEnabled: func() bool {
			return memruntime.PyTruthy(r.kernel.Cfg("embedding.fallback.enabled", true))
		},
		IsEmbeddingDegraded: func() bool {
			return memruntime.PyTruthy(r.kernel.EmbeddingDegraded()["active"])
		},
		SetEmbeddingDegraded: func(active bool, reason string, checkedAt *float64) {
			r.kernel.SetEmbeddingDegraded(active, reason, checkedAt)
		},
		EmbeddingEncode: func(ctx context.Context, texts []string) ([][]float32, error) {
			if r.embeddingManager == nil {
				return nil, fmt.Errorf("嵌入管理器未初始化")
			}
			return r.embeddingManager.Encode(ctx, texts, nil)
		},
		ParagraphStore:         r.paragraphPoolStore,
		GraphVectorStore:       r.graphPoolStore,
		VectorStore:            func() *storage.VectorStore { return r.vectorStore },
		DualVectorPoolsEnabled: func(ctx context.Context) bool { return r.kernel.DualVectorPoolsEnabled() },
		GraphVectorID: func(kind string, hashValue string) string {
			if typed := r.kernel.GraphVectorID(kind, hashValue); typed != "" {
				return typed
			}
			return hashValue
		},
		RecordVectorInput: func(pool string, hashValue string, text string) {
			r.kernel.RecordVectorSpaceInput(pool, hashValue, text)
		},
		RelationVectorsEnabled:  r.kernel.RelationVectorsEnabled,
		BuildRelationVectorText: memruntime.BuildRelationVectorText,
		UpsertRelationWithVector: func(ctx context.Context, subject, predicate, object string, confidence float64, sourceParagraph string, metadata map[string]any, writeVector bool) (string, error) {
			if r.relationWriteService == nil {
				return "", fmt.Errorf("关系写入服务未初始化")
			}
			result, err := r.relationWriteService.UpsertRelationWithVector(ctx, subject, predicate, object, confidence, sourceParagraph, metadata, writeVector)
			return result.HashValue, err
		},
		EnsureRelationVector: func(ctx context.Context, hashValue, subject, predicate, object string, typedID bool, beforeVectorWrite func()) (bool, bool, error) {
			if beforeVectorWrite != nil {
				beforeVectorWrite()
			}
			if r.relationWriteService == nil {
				return false, false, fmt.Errorf("关系写入服务未初始化")
			}
			result, err := r.relationWriteService.EnsureRelationVector(ctx, hashValue, subject, predicate, object, typedID)
			return result.VectorWritten, result.VectorAlreadyExists, err
		},
		ShouldAutoEnqueueEpisode: func(sourceType string) bool {
			return utils.ShouldAutoEnqueueEpisode(r.kernel.Cfg, sourceType)
		},
		NewFactDeduplicator: func() *utils.FactDeduplicator {
			store := r.metadataStore
			if store == nil {
				return nil
			}
			adapter := summaryStoreAdapter{store: store}
			return r.summaryDedupFactory(adapter)("utils")
		},
	}
}

// profileAdminDeps 绑定人物画像服务的真实端口
func (r *Runtime) profileAdminDeps() services.ProfileAdminDeps {
	return services.ProfileAdminDeps{
		Metadata:      func() *storage.Store { return r.metadataStore },
		PersonProfile: r.personProfileService,
		Initialize:    func(ctx context.Context) error { return r.kernel.Initialize(ctx) },
		Cfg:           r.kernel.Cfg,
		MarkPersonActive: func(personID string) {
			r.markPersonActiveSeen(personID)
		},
		FeedbackProfileRefreshEnabled: func() bool {
			return memruntime.PyTruthy(r.kernel.Cfg("feedback_correction.profile_refresh_enabled", true))
		},
		FeedbackProfileForceRefreshOnRead: func() bool {
			return memruntime.PyTruthy(r.kernel.Cfg("feedback_correction.profile_force_refresh_on_read", true))
		},
		FeedbackReconcileBatchSize: func() int {
			if size, err := memruntime.PyInt(r.kernel.Cfg("feedback_correction.reconcile_batch_size", 20)); err == nil && size > 0 {
				return size
			}
			return 20
		},
		ProfileRefreshMaxRetry: func() int { return utils.PersonProfileRefreshMaxRetry(r.kernel.Cfg) },
		DeleteAction: func(ctx context.Context, mode string, selector map[string]any, requestedBy string, reason string) (map[string]any, error) {
			result, err := r.kernel.ExecuteDeleteAction(ctx, map[string]any{
				"mode": mode, "selector": selector, "requested_by": requestedBy, "reason": reason,
			})
			if err != nil {
				return nil, err
			}
			return mapFromAny(result), nil
		},
		InvalidateImportManifest: func(ctx context.Context, result map[string]any) {
			if r.importManager == nil {
				return
			}
			r.importManager.InvalidateManifestForSources(anyStringList(result["sources"]))
		},
		ProcessProfileRefreshBatch: func(ctx context.Context, limit int) (map[string]any, error) {
			// 必须走 feedback 服务的批次实现。接到 kernel.ProcessPersonProfileRefreshQueueBatch
			// 会绕回本服务自身，形成无限递归直到栈溢出。
			result, err := r.kernel.ProcessFeedbackProfileRefreshBatch(ctx, map[string]any{"limit": limit})
			if err != nil {
				return nil, err
			}
			return mapFromAny(result), nil
		},
		FilterUserVisibleHits: func(items []map[string]any) []map[string]any {
			return r.filterHitsBySearchHit("visible", items, nil)
		},
		TrimText: func(value string, limit int) string {
			return trimRunes(value, limit)
		},
		ObserveIdentity: func(platform string, userID string, aliases []string, groupID string) (map[string]any, error) {
			if r.metadataStore == nil {
				return nil, fmt.Errorf("元数据存储未初始化")
			}
			return utils.ObserveIdentity(
				summaryIdentityStoreAdapter{store: r.metadataStore},
				summaryIdentityHostAdapter{store: r.metadataStore, findMessages: r.findMessages},
				utils.ObserveIdentityParams{Platform: platform, UserID: userID, Aliases: aliases, GroupID: groupID},
			)
		},
		ProfileTTLSeconds: func() float64 { return 6 * 3600 },
	}
}

// trimRunes 按字符截断文本
func trimRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// personProfileService 懒构造画像服务，存储打开后才会真正创建
func (r *Runtime) personProfileService() *utils.PersonProfileService {
	r.profileMu.Lock()
	defer r.profileMu.Unlock()
	if r.personProfile != nil {
		return r.personProfile
	}
	store := r.metadataStore
	if store == nil {
		return nil
	}
	llm, modelSignature := r.personProfileLLM()
	r.personProfile = utils.NewPersonProfileService(utils.PersonProfileServiceOptions{
		Config:         r.kernel.Cfg,
		Logger:         r.logger,
		Store:          personProfileStoreAdapter{store: store},
		Retriever:      profileRetrieverAdapter{runtime: r},
		LLM:            llm,
		ModelSignature: modelSignature,
		Now:            func() float64 { return float64(time.Now().UnixNano()) / 1e9 },
	})
	return r.personProfile
}

// personProfileLLM 构造人物画像分类 LLM 端口并返回模型签名回调
// 缺少网关或模型任务时显式记录降级原因，由服务回退到规则分桶
func (r *Runtime) personProfileLLM() (utils.PersonProfileLLM, func() map[string]any) {
	logger := utils.OrLogger(r.logger)
	if r.llmGateway == nil {
		reason := "LLM 网关未初始化"
		logger.Warning("A_Memorix 人物画像分类降级为规则分桶: " + reason)
		r.markDegraded("人物画像分类 LLM 未接线: " + reason)
		return nil, nil
	}
	adapter := utils.NewPersonProfileClassificationLLM(r.llmGateway, r.modelTasks, logger)
	if _, err := adapter.ResolveModel(); err != nil {
		logger.Warning("A_Memorix 人物画像分类降级为规则分桶: " + err.Error())
		r.markDegraded("人物画像分类 LLM 未就绪: " + err.Error())
	}
	return adapter, adapter.ModelSignature
}

// maintenanceDeps 绑定记忆维护服务的真实端口
func (r *Runtime) maintenanceDeps() services.MaintenanceDeps {
	return services.MaintenanceDeps{
		Metadata: func() *storage.Store { return r.metadataStore },
		Graph:    func() *storage.GraphStore { return r.graphStore },
		RebuildGraphFromMetadata: func(ctx context.Context) (map[string]any, error) {
			result, err := r.kernel.RebuildGraphFromMetadata(ctx, nil)
			if err != nil {
				return nil, err
			}
			return mapFromAny(result), nil
		},
		ExecuteDeleteAction: func(ctx context.Context, payload map[string]any) (map[string]any, error) {
			result, err := r.kernel.ExecuteDeleteAction(ctx, payload)
			if err != nil {
				return nil, err
			}
			return mapFromAny(result), nil
		},
		PurgeDeletedMemory: func(ctx context.Context, payload map[string]any) (map[string]any, error) {
			result, err := r.kernel.PurgeDeletedMemory(ctx, payload)
			if err != nil {
				return nil, err
			}
			return mapFromAny(result), nil
		},
		Sleep: func(ctx context.Context, seconds float64) error {
			if seconds <= 0 {
				return nil
			}
			timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		BackgroundStopping: func() bool { return r.stopping.Load() },
		LastMaintenanceAt: func() float64 {
			r.maintenanceMu.Lock()
			defer r.maintenanceMu.Unlock()
			return r.lastMaintenance
		},
		SetLastMaintenanceAt: func(value float64) {
			r.maintenanceMu.Lock()
			r.lastMaintenance = value
			r.maintenanceMu.Unlock()
		},
		Persist: func() {
			if err := r.kernel.Persist(context.Background(), false); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 持久化内核状态失败: %v", err))
			}
		},
	}
}

// episodeQueryForChatScope 用真实存储与检索器查询 episode
func (r *Runtime) episodeQueryForChatScope(ctx context.Context, params map[string]any) (any, error) {
	store := r.metadataStore
	if store == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	query := strings.TrimSpace(memruntime.PyStr(params["query"]))
	topK := 5
	if parsed, err := memruntime.PyInt(params["top_k"]); err == nil && parsed > 0 {
		topK = parsed
	}
	timeFrom := utils.OptionalFloat(params["time_from"])
	timeTo := utils.OptionalFloat(params["time_to"])
	person := strings.TrimSpace(memruntime.PyStr(params["person"]))
	scope, _ := params["scope"].(*services.RetrievalScope)
	deps := retrieval.EpisodeRetrievalStoreDeps{
		QueryEpisodes: func(queryText string, from, to *float64, personID string, source string, limit int, allowedEpisodeIDs []string) ([]map[string]any, error) {
			return store.QueryEpisodes(queryText, storage.EpisodeQueryFilter{
				TimeFrom: from, TimeTo: to, Person: personID, Source: source,
			}, limit, allowedEpisodeIDs, nil)
		},
		GetEpisodeRowsByParagraphHashes: func(hashes []string, source string) ([]map[string]any, error) {
			return store.GetEpisodeRowsByParagraphHashes(hashes, storage.EpisodeQueryFilter{Source: source}, nil)
		},
		GetEpisodeRowsByRelationHashes: func(hashes []string, source string) ([]map[string]any, error) {
			return store.GetEpisodeRowsByRelationHashes(hashes, storage.EpisodeQueryFilter{Source: source}, nil)
		},
		GetEpisodeParagraphs: func(episodeID string, limit int) ([]map[string]any, error) {
			return store.GetEpisodeParagraphs(episodeID, limit, nil)
		},
	}
	service := retrieval.NewEpisodeRetrievalService(deps, r.retriever, r.logger)
	return service.Query(ctx, query, topK, timeFrom, timeTo, person, "", false, toRetrievalScope(scope))
}

// mapFromAny 把内核返回的 any 载荷统一成字典
func mapFromAny(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}
	return map[string]any{"result": value}
}
