package native

// 管理类服务依赖装配：事实、来源、episode、图、删除、V5、向量删除与 bundle
// 端口全部延迟读取当前存储与内核状态，初始化完成后无需重新绑定

import (
	"context"
	"errors"
	"strings"
	"time"

	"maibot-go/internal/A_memorix/core/runtime/services"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// factAdminDeps 绑定事实 claim 存储与画像刷新队列
func (r *Runtime) factAdminDeps() services.FactAdminDeps {
	requireStore := func() (*storage.Store, error) {
		if r.metadataStore == nil {
			return nil, errors.New("metadata store unavailable")
		}
		return r.metadataStore, nil
	}
	return services.FactAdminDeps{
		Initialize: r.kernel.Initialize,
		GetFactClaim: func(claimID string) (map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.GetFactClaim(claimID)
		},
		ListFactClaims: func(query storage.FactClaimQuery) ([]map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.ListFactClaims(query)
		},
		UpsertFactClaim: func(input storage.FactClaimUpsert) (map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.UpsertFactClaim(input)
		},
		UpdateFactClaimClassification: func(claimID string, input storage.FactClaimClassification) (map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.UpdateFactClaimClassification(claimID, input)
		},
		RetractFactClaim: func(claimID string, audit storage.FactClaimAudit) (map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.RetractFactClaim(claimID, audit)
		},
		RestoreFactClaim: func(claimID string, audit storage.FactClaimAudit) (map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.RestoreFactClaim(claimID, audit)
		},
		EnqueuePersonProfileRefresh: func(input storage.PersonProfileRefreshEnqueueInput) (map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.EnqueuePersonProfileRefresh(input)
		},
	}
}

// sourceAdminDeps 绑定来源统计、删除与导入清单失效
func (r *Runtime) sourceAdminDeps() services.SourceAdminDeps {
	return services.SourceAdminDeps{
		Initialize: r.kernel.Initialize,
		GetAllSources: func() ([]map[string]any, error) {
			store := r.metadataStore
			if store == nil {
				return nil, errors.New("metadata store unavailable")
			}
			items, err := store.GetAllSources()
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(items))
			for _, item := range items {
				row := map[string]any{"source": item.Source, "count": item.Count}
				if item.LastUpdated != nil {
					row["last_updated"] = *item.LastUpdated
				} else {
					row["last_updated"] = nil
				}
				out = append(out, row)
			}
			return out, nil
		},
		GetEpisodeSourceQueryBlockedFlags: func(sources []string) (map[string]bool, error) {
			store := r.metadataStore
			if store == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return store.GetEpisodeSourceQueryBlockedFlags(sources, nil)
		},
		ExecuteDeleteAction: func(ctx context.Context, mode string, selector map[string]any, requestedBy string, reason string) (map[string]any, error) {
			result, err := r.kernel.ExecuteDeleteAction(ctx, map[string]any{
				"mode": mode, "selector": selector, "requested_by": requestedBy, "reason": reason,
			})
			if err != nil {
				return nil, err
			}
			return mapFromAny(result), nil
		},
		InvalidateImportManifestForSources: func(result map[string]any) {
			if r.importManager == nil || result == nil {
				return
			}
			r.importManager.InvalidateManifestForSources(anyStringList(result["sources"]))
		},
	}
}

// episodeAdminDeps 绑定 episode 查询、重建与队列状态
func (r *Runtime) episodeAdminDeps() services.EpisodeAdminDeps {
	return services.EpisodeAdminDeps{
		Initialize: r.kernel.Initialize,
		EnqueueEpisodeSourceRebuild: func(source string, reason string, debounceSeconds float64) {
			if r.metadataStore == nil {
				return
			}
			r.metadataStore.EnqueueEpisodeSourceRebuild(source, reason, storage.EpisodeEnqueueOptions{DebounceSeconds: debounceSeconds})
		},
		ProcessEpisodeSourceRebuildBatch: r.kernel.ProcessEpisodeSourceRebuildBatch,
		QueryEpisodes: func(query string, filter storage.EpisodeQueryFilter, limit int) ([]map[string]any, error) {
			store := r.metadataStore
			if store == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return store.QueryEpisodes(query, filter, limit, nil, nil)
		},
		GetEpisodeByID: func(episodeID string) (map[string]any, error) {
			store := r.metadataStore
			if store == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return store.GetEpisodeByID(episodeID, nil)
		},
		GetEpisodeParagraphs: func(episodeID string, limit int) ([]map[string]any, error) {
			store := r.metadataStore
			if store == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return store.GetEpisodeParagraphs(episodeID, limit, nil)
		},
		GetEpisodeSourceRebuildSummary: func(failedLimit int) (map[string]any, error) {
			store := r.metadataStore
			if store == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return store.GetEpisodeSourceRebuildSummary(failedLimit, nil)
		},
		DiscardMigrationEpisodeRebuilds: func(dryRun bool) (map[string]any, error) {
			store := r.metadataStore
			if store == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return store.DiscardMigrationEpisodeRebuilds(dryRun, nil)
		},
		ListEpisodeSourcesForRebuild: func() ([]string, error) {
			store := r.metadataStore
			if store == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return store.ListEpisodeSourcesForRebuild(nil)
		},
		GetAllSources: func() ([]map[string]any, error) {
			return r.sourceAdminDeps().GetAllSources()
		},
	}
}

// graphAdminOptions 绑定图查询、删除、投影与向量补齐
func (r *Runtime) graphAdminOptions() services.GraphAdminOptions {
	return services.GraphAdminOptions{
		Base:                   r.base,
		Delete:                 kernelDeleteExecutor{runtime: r},
		Projection:             maintenanceGraphProjection{runtime: r},
		RelationVectors:        graphRelationVectorWriter{runtime: r},
		EntityVectors:          graphEntityVectorWriter{runtime: r},
		RelationVectorsEnabled: r.kernel.RelationVectorsEnabled(),
		TypedRelationIDs:       true,
		LifecyclePolicy:        utils.DefaultRelationLifecyclePolicy,
		AliasRebuilder:         graphAliasRebuilder{runtime: r},
		VectorInvalidator:      graphVectorInvalidator{runtime: r},
	}
}

// deleteAdminOptions 绑定删除执行、图重建与导入清单失效
func (r *Runtime) deleteAdminOptions() services.DeleteAdminOptions {
	return services.DeleteAdminOptions{
		Base:                   r.base,
		RelationVectorsEnabled: r.kernel.RelationVectorsEnabled(),
		GraphRebuild:           kernelGraphRebuilder{runtime: r},
		ImportInvalidator:      importManifestInvalidator{runtime: r},
	}
}

// vectorDeleteDeps 绑定三类向量库与图向量 id
func (r *Runtime) vectorDeleteDeps() services.VectorDeleteDeps {
	return services.VectorDeleteDeps{
		LegacyStore:            func() *storage.VectorStore { return r.vectorStore },
		ParagraphStore:         func() *storage.VectorStore { return r.paragraphVectorStore },
		GraphStore:             func() *storage.VectorStore { return r.graphVectorStore },
		DualVectorPoolsEnabled: func() bool { return r.kernel.DualVectorPoolsEnabled() },
		GraphVectorID: func(itemType string, hashValue string) string {
			if typed := r.kernel.GraphVectorID(itemType, hashValue); typed != "" {
				return typed
			}
			return hashValue
		},
	}
}

// v5AdminDeps 绑定生命周期、回收站与 V5 操作记录
func (r *Runtime) v5AdminDeps() services.V5AdminDeps {
	return services.V5AdminDeps{
		Initialize: r.kernel.Initialize,
		GetRelation: func(ctx context.Context, hashValue string) map[string]any {
			if r.metadataStore == nil {
				return nil
			}
			row, _ := r.metadataStore.GetRelation(hashValue, true)
			return row
		},
		GetDeletedRelation: func(ctx context.Context, hashValue string) map[string]any {
			if r.metadataStore == nil {
				return nil
			}
			row, _ := r.metadataStore.GetDeletedRelation(hashValue)
			return row
		},
		SearchRelationHashesByText: func(ctx context.Context, text string, limit int) ([]string, error) {
			if r.metadataStore == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return r.metadataStore.SearchRelationHashesByText(text, limit)
		},
		SearchDeletedRelationHashesByText: func(ctx context.Context, text string, limit int) ([]string, error) {
			if r.metadataStore == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return r.metadataStore.SearchDeletedRelationHashesByText(text, limit)
		},
		GetRelationsBySubject: func(ctx context.Context, subject string) ([]map[string]any, error) {
			if r.metadataStore == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return r.metadataStore.GetRelations(subject, "", "", true)
		},
		GetDeletedRelations: func(ctx context.Context, limit int) ([]map[string]any, error) {
			if r.metadataStore == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return r.metadataStore.GetDeletedRelations(limit)
		},
		GetMemoryStatusSummary: func(ctx context.Context, now float64) (map[string]int, error) {
			if r.metadataStore == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return r.metadataStore.GetMemoryStatusSummary(&now)
		},
		GetRelationStatusBatch: func(ctx context.Context, hashes []string) (map[string]map[string]any, error) {
			if r.metadataStore == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return r.metadataStore.GetRelationStatusBatch(hashes)
		},
		ProtectRelations: func(ctx context.Context, hashes []string, isPinned bool, ttlSeconds float64) error {
			if r.metadataStore == nil {
				return errors.New("metadata store unavailable")
			}
			return r.metadataStore.ProtectRelations(hashes, isPinned, ttlSeconds)
		},
		UpdateRelationsProtection: func(ctx context.Context, hashes []string, protectedUntil *float64, isPinned *bool, lastReinforced *float64) error {
			if r.metadataStore == nil {
				return errors.New("metadata store unavailable")
			}
			return r.metadataStore.UpdateRelationsProtection(hashes, protectedUntil, isPinned, lastReinforced)
		},
		RecordV5Operation: func(ctx context.Context, action string, target string, hashes []string, reason string, updatedBy string, result map[string]any) (map[string]any, error) {
			if r.metadataStore == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return r.metadataStore.RecordV5Operation(action, target, hashes, reason, updatedBy, result, nil)
		},
		ApplyRelationLifecycleEvent: func(ctx context.Context, hashes []string, event utils.RelationLifecycleEvent, strength float64, now float64) ([]map[string]any, error) {
			if r.metadataStore == nil {
				return nil, errors.New("metadata store unavailable")
			}
			return r.metadataStore.ApplyRelationLifecycleEvent(hashes, event, utils.DefaultRelationLifecyclePolicy(), now, strength)
		},
		RelationLifecyclePolicy: func(ctx context.Context) utils.RelationLifecyclePolicy {
			return utils.DefaultRelationLifecyclePolicy()
		},
		Persist: func(ctx context.Context) {
			r.persistKernel()
		},
		LastMaintenanceAt: func() any {
			r.maintenanceMu.Lock()
			defer r.maintenanceMu.Unlock()
			return r.lastMaintenance
		},
	}
}

// kernelDeleteExecutor 把内核删除动作适配成图服务端口
type kernelDeleteExecutor struct {
	runtime *Runtime
}

func (e kernelDeleteExecutor) ExecuteDeleteAction(ctx context.Context, payload map[string]any) (map[string]any, error) {
	result, err := e.runtime.kernel.ExecuteDeleteAction(ctx, payload)
	if err != nil {
		return nil, err
	}
	return mapFromAny(result), nil
}

// maintenanceGraphProjection 把维护服务的投影结果适配成通用字典
type maintenanceGraphProjection struct {
	runtime *Runtime
}

func (p maintenanceGraphProjection) PublishAuthoritativeGraphProjection(ctx context.Context) (map[string]any, error) {
	service := p.runtime.kernel.Services().Maintenance
	if service == nil {
		return nil, errors.New("维护服务未装配")
	}
	publisher, ok := service.(interface {
		PublishAuthoritativeGraphProjection(context.Context) (map[string]int, error)
	})
	if !ok {
		return nil, errors.New("维护服务不支持权威图投影")
	}
	p.runtime.graphProjectionMu.Lock()
	defer p.runtime.graphProjectionMu.Unlock()
	result, err := publisher.PublishAuthoritativeGraphProjection(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(result))
	for key, value := range result {
		out[key] = value
	}
	return out, nil
}

// graphRelationVectorWriter 复用关系写入服务补齐关系向量
type graphRelationVectorWriter struct {
	runtime *Runtime
}

func (w graphRelationVectorWriter) EnsureRelationVector(ctx context.Context, hashValue string, subject string, predicate string, object string, typedID bool) (map[string]any, error) {
	service := w.runtime.relationWriteService
	if service == nil {
		return nil, errors.New("关系写入服务未初始化")
	}
	result, err := service.EnsureRelationVector(ctx, hashValue, subject, predicate, object, typedID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"hash_value":       result.HashValue,
		"vector_written":   result.VectorWritten,
		"already_existing": result.VectorAlreadyExists,
		"vector_state":     result.VectorState,
	}, nil
}

// graphEntityVectorWriter 复用注入服务补齐实体向量
type graphEntityVectorWriter struct {
	runtime *Runtime
}

func (w graphEntityVectorWriter) EnsureEntityVector(ctx context.Context, hashValue string) (bool, error) {
	return w.runtime.kernel.EnsureEntityVector(ctx, map[string]any{"hash": hashValue})
}

// graphAliasRebuilder 重建关系 hash 别名
type graphAliasRebuilder struct {
	runtime *Runtime
}

func (b graphAliasRebuilder) RebuildRelationHashAliases(conn *storage.Conn) error {
	if b.runtime.metadataStore == nil {
		return errors.New("metadata store unavailable")
	}
	_, err := b.runtime.metadataStore.RebuildRelationHashAliases(conn)
	return err
}

// graphVectorInvalidator 删除节点重命名后的旧向量
type graphVectorInvalidator struct {
	runtime *Runtime
}

func (v graphVectorInvalidator) DeleteVectorsByType(ctx context.Context, entityHashes []string, relationHashes []string) (map[string]any, error) {
	service := services.NewVectorDeleteService(v.runtime.vectorDeleteDeps())
	count, err := service.DeleteVectors(ctx, nil, entityHashes, relationHashes)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "deleted": count}, nil
}

// kernelGraphRebuilder 调用内核图重建
type kernelGraphRebuilder struct {
	runtime *Runtime
}

func (b kernelGraphRebuilder) RebuildGraphFromMetadata(ctx context.Context) (map[string]any, error) {
	result, err := b.runtime.kernel.RebuildGraphFromMetadata(ctx, nil)
	if err != nil {
		return nil, err
	}
	return mapFromAny(result), nil
}

// importManifestInvalidator 让导入清单按删除来源失效
type importManifestInvalidator struct {
	runtime *Runtime
}

func (i importManifestInvalidator) InvalidateImportManifestForSources(ctx context.Context, sources []string) {
	if i.runtime.importManager == nil {
		return
	}
	i.runtime.importManager.InvalidateManifestForSources(anyStringList(sources))
}

// anyStringList 把 []string / []any 统一成 []any
func anyStringList(value any) []any {
	switch typed := value.(type) {
	case []string:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	case []any:
		return typed
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []any{typed}
	default:
		return nil
	}
}

// maintenanceTimestamp 统一时间戳
func maintenanceTimestamp() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
