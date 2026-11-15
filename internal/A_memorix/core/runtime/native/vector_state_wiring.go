package native

// Embedding 状态与双池向量状态的服务依赖装配
// 端口只暴露当前内核状态，避免服务层直接依赖 Runtime 具体类型

import (
	"context"
	"sync"

	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/retrieval"
	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/runtime/services"
	"MaiGo/internal/A_memorix/core/storage"
)

// embeddingStateDeps 绑定 embedding 维度、指纹与向量库状态
func (r *Runtime) embeddingStateDeps() services.EmbeddingStateDeps {
	return services.EmbeddingStateDeps{
		DualVectorPoolsConfigEnabled: func(ctx context.Context) bool {
			return r.dualVectorPoolsConfigured()
		},
		ReadDualVectorReadyManifest: func(ctx context.Context) map[string]any {
			return r.kernel.ReadDualVectorReadyManifest()
		},
		VectorsRoot: func(ctx context.Context) string {
			return r.kernel.VectorsRoot()
		},
		RequestedDimension: func() int {
			if r.embeddingManager == nil {
				return 0
			}
			return r.embeddingManager.GetRequestedDimension()
		},
		DefaultDimension: func() int {
			return r.kernel.EmbeddingDimension()
		},
		EmbeddingDimension: func() int {
			return r.kernel.EmbeddingDimension()
		},
		SetEmbeddingDimension: func(dimension int) {
			r.kernel.SetEmbeddingDimension(dimension)
		},
		Fingerprint: func(dimension int) (map[string]any, error) {
			if r.embeddingManager == nil {
				return nil, nil
			}
			return r.embeddingManager.GetEmbeddingFingerprint(&embedding.EmbeddingFingerprintOptions{Dimension: &dimension})
		},
		MetadataStore: func() *storage.Store {
			return r.metadataStore
		},
		VectorStore: func() *storage.VectorStore {
			return r.vectorStore
		},
		SetVectorStore: func(store *storage.VectorStore) {
			r.vectorStore = store
			r.kernel.SetVectorStore(store)
		},
		ParagraphVectorStore: func() *storage.VectorStore {
			return r.paragraphVectorStore
		},
		GraphVectorStore: func() *storage.VectorStore {
			return r.graphVectorStore
		},
		SetParagraphVectorStore: func(store *storage.VectorStore) {
			r.paragraphVectorStore = store
			r.kernel.SetParagraphVectorStore(store)
		},
		SetGraphVectorStore: func(store *storage.VectorStore) {
			r.graphVectorStore = store
			r.kernel.SetGraphVectorStore(store)
		},
		MakeVectorStore: func(ctx context.Context, dataDir string, dimension *int) (*storage.VectorStore, error) {
			return r.kernel.MakeVectorStore(dataDir, dimension)
		},
		ReloadDualVectorStoresFromDisk: func(ctx context.Context) bool {
			return r.kernel.ReloadDualVectorStoresFromDisk()
		},
		DualVectorPoolsEnabled: func(ctx context.Context) bool {
			return r.kernel.DualVectorPoolsEnabled()
		},
		ParagraphVectorDir: func(ctx context.Context) string {
			return r.kernel.ParagraphVectorDir()
		},
		GraphVectorDir: func(ctx context.Context) string {
			return r.kernel.GraphVectorDir()
		},
		V1ValidHashesForPool: func(ctx context.Context, pool string) []string {
			return r.kernel.V1ValidHashesForPool(ctx, pool)
		},
		V1ReconciliationEvidenceRoot: func(ctx context.Context) string {
			return r.kernel.V1ReconciliationEvidenceRoot(ctx)
		},
		LegacyVectorViewPresent: func() bool { return r.legacyVectorView != nil },
		EmbeddingDegraded: func() map[string]any {
			return r.kernel.EmbeddingDegraded()
		},
		SetEmbeddingDegraded: func(active bool, reason string, checkedAt *float64) {
			r.setEmbeddingDegradedDirect(active, reason, checkedAt)
		},
		SetRuntimeCapability: func(name string, enabled bool) {
			_ = r.kernel.SetRuntimeCapability(name, enabled)
		},
		RuntimeCapability: func(name string) bool {
			return r.kernel.RuntimeCapabilities()[name]
		},
		SetRuntimeSparseOnly: func(sparseOnly bool) {
			if r.retriever != nil {
				r.retriever.SetRuntimeSparseOnly(sparseOnly)
			}
		},
		RunSelfCheck: func(ctx context.Context, sampleText string, config map[string]any) (map[string]any, error) {
			report := r.webImportSelfCheck(ctx)
			return report.ToDict(), nil
		},
		BuildRuntimeConfig: func(ctx context.Context) map[string]any {
			config, err := r.buildRuntimeConfig()
			if err != nil {
				return map[string]any{}
			}
			return config
		},
		SelfCheckReport: func() map[string]any {
			return r.kernel.RuntimeFacade().RuntimeSelfCheckReport()
		},
		SetSelfCheckReport: func(report map[string]any) {
			r.kernel.RuntimeFacade().SetRuntimeSelfCheckReport(report)
		},
		RefreshRelationWriteService: func(ctx context.Context) {
			r.refreshRelationWriteService()
		},
		RefreshRuntimeDependents: func(ctx context.Context) {
			r.refreshRuntimeDependents(false)
		},
		StartBackgroundTasks: r.startBackgroundTasks,
		SetVectorHealth: func(ctx context.Context, patch map[string]any) {
			r.kernel.ApplyVectorHealthPatch(patch)
		},
		VectorHealthSnapshot: func() map[string]any {
			return r.kernel.VectorHealthSnapshot()
		},
		VectorHealthErrorCode: func() string {
			return memruntime.PyStr(r.kernel.VectorHealth()["error_code"])
		},
		DisableVectorChannel: func(ctx context.Context, err error) {
			r.kernel.DisableVectorChannel(err)
		},
		RecoverKnownVectorFailure: func(ctx context.Context, err error) bool {
			return r.kernel.RecoverKnownVectorFailure(err)
		},
		BuildSearchRuntime: func(ctx context.Context) (services.SearchRuntimeBundle, error) {
			config, err := r.buildRuntimeConfig()
			if err != nil {
				return services.SearchRuntimeBundle{}, err
			}
			return r.buildSearchRuntime(config)
		},
		SetRetriever: func(value any) {
			if typed, ok := value.(*retrieval.DualPathRetriever); ok {
				r.retriever = typed
				r.kernel.SetRetriever(typed)
			}
		},
		SetThresholdFilter: func(value any) {
			if typed, ok := value.(*retrieval.DynamicThresholdFilter); ok {
				r.thresholdFilter = typed
				r.kernel.SetThresholdFilter(typed)
			}
		},
		SetSparseIndex: func(value any) {
			if typed, ok := value.(*retrieval.SparseBM25Index); ok {
				r.sparseIndex = typed
				r.kernel.SetSparseIndex(typed)
			}
		},
		RebuildLock: func() *sync.Mutex {
			return r.kernel.VectorRebuildLock()
		},
		SetVectorPersistBlockedUntilRebuild: func(blocked bool) {
			r.kernel.SetVectorPersistBlockedUntilRebuild(blocked)
		},
		SetVectorRebuildSourceDimension: func(dimension int) {
			r.kernel.SetVectorRebuildSourceDimension(dimension)
		},
		RunParagraphBackfillOnce: func(ctx context.Context, limit int, maxRetry int, trigger string) (map[string]any, error) {
			return r.runParagraphBackfillOnce(ctx, &limit, &maxRetry, trigger)
		},
		SynchronizeVectorSpace: func(ctx context.Context) (map[string]any, error) {
			return r.SynchronizeVectorSpaceNative(ctx, map[string]any{"force": false})
		},
	}
}

// dualVectorStateDeps 绑定双池目录、manifest 与向量库读写
func (r *Runtime) dualVectorStateDeps() services.DualVectorStateDeps {
	return services.DualVectorStateDeps{
		ActiveVectorSpaceID: func() string {
			space, err := r.readActiveVectorSpace()
			if err != nil {
				return ""
			}
			return space
		},
		DualVectorPoolsReady:    r.kernel.DualVectorPoolsReady,
		SetDualVectorPoolsReady: r.kernel.SetDualVectorPoolsReady,
		EmbeddingDimension:      r.kernel.EmbeddingDimension,
		CurrentEmbeddingStatusDimension: func(ctx context.Context) int {
			return r.kernel.CurrentEmbeddingStatusDimension()
		},
		CurrentEmbeddingFingerprint: func(ctx context.Context, dimension *int) map[string]any {
			return r.kernel.CurrentEmbeddingFingerprint(dimension)
		},
		CurrentEmbeddingFingerprintForValidation: func(ctx context.Context, dimension *int) map[string]any {
			return r.kernel.CurrentEmbeddingFingerprintForValidation(dimension)
		},
		NormalizeEmbeddingFingerprint: func(value any) map[string]any {
			return r.kernel.NormalizeEmbeddingFingerprint(value)
		},
		StoredVectorsCompatibleWithCurrentEmbedding: func(ctx context.Context, store *storage.VectorStore) bool {
			return r.kernel.StoredVectorsCompatibleWithCurrentEmbedding(store)
		},
		MetadataStore: func() *storage.Store { return r.metadataStore },
		CountVectorRebuildTargets: func(ctx context.Context) map[string]int {
			return r.kernel.CountVectorRebuildTargets()
		},
		RelationVectorsEnabled: r.kernel.RelationVectorsEnabled,
		ReadParagraphVectorStore: func() *storage.VectorStore {
			return r.paragraphVectorStore
		},
		ReadGraphVectorStore: func() *storage.VectorStore {
			return r.graphVectorStore
		},
		SetParagraphVectorStore: func(store *storage.VectorStore) {
			r.paragraphVectorStore = store
			r.kernel.SetParagraphVectorStore(store)
		},
		SetGraphVectorStore: func(store *storage.VectorStore) {
			r.graphVectorStore = store
			r.kernel.SetGraphVectorStore(store)
		},
		ReadVectorStore: func() *storage.VectorStore { return r.vectorStore },
		SetVectorStore: func(store *storage.VectorStore) {
			r.vectorStore = store
			r.kernel.SetVectorStore(store)
		},
		ReconcileV1EvidenceRoot: func(ctx context.Context) string {
			return r.kernel.V1ReconciliationEvidenceRoot(ctx)
		},
		V1ValidHashesForPool: func(ctx context.Context, pool string) []string {
			return r.kernel.V1ValidHashesForPool(ctx, pool)
		},
	}
}

// setEmbeddingDegradedDirect 直接写入内核降级状态，避免回调 EmbeddingStateService 形成递归
func (r *Runtime) setEmbeddingDegradedDirect(active bool, reason string, checkedAt *float64) {
	r.kernel.SetEmbeddingDegradedSnapshot(active, reason, checkedAt)
	vectorEnabled := !active && r.kernel.EmbeddingDimension() > 0
	_ = r.kernel.SetRuntimeCapability("embedding", vectorEnabled)
	if r.retriever != nil {
		sparseOnly := active || !r.kernel.RuntimeCapabilities()["vector_read"]
		r.retriever.SetRuntimeSparseOnly(sparseOnly)
	}
}
