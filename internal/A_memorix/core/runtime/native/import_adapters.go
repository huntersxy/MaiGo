package native

// Web 导入与检索调优管理器的存储适配器
// storage 的批量写入签名带 vectorIndex/metadata 参数，这里按 WebImport 端口收敛，不改变内核语义

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/A_memorix/core/embedding"
	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// webImportStoreAdapter 把 storage.Store 适配为 WebImportStore
type webImportStoreAdapter struct {
	inner *storage.Store
}

func (a *webImportStoreAdapter) AddParagraph(content string, source string, metadata map[string]any, knowledgeType string, timeMeta map[string]any) (string, error) {
	return a.inner.AddParagraph(content, nil, source, metadata, knowledgeType, timeMeta)
}

func (a *webImportStoreAdapter) AddEntitiesBatch(names []string, sourceParagraph string) ([]string, error) {
	return a.inner.AddEntitiesBatch(names, nil, sourceParagraph, nil)
}

func (a *webImportStoreAdapter) AddRelationsBatch(relations [][3]string, confidence float64, sourceParagraph string) ([]string, error) {
	return a.inner.AddRelationsBatch(relations, nil, confidence, sourceParagraph, nil)
}

func (a *webImportStoreAdapter) AddRelation(subject string, predicate string, object string, confidence float64, sourceParagraph string) (string, error) {
	return a.inner.AddRelation(subject, predicate, object, nil, confidence, sourceParagraph, nil)
}

func (a *webImportStoreAdapter) SetRelationVectorState(hash string, state string, errorText string, bumpRetry bool) error {
	var errPtr *string
	if errorText != "" {
		errPtr = &errorText
	}
	_, err := a.inner.SetRelationVectorState(hash, state, errPtr, bumpRetry)
	return err
}

func (a *webImportStoreAdapter) GetLiveParagraphsBySource(source string) ([]map[string]any, error) {
	return a.inner.GetLiveParagraphsBySource(source, true, nil)
}

// webImportGraphAdapter 把 storage.GraphStore 适配为 WebImportGraphStore
type webImportGraphAdapter struct {
	inner *storage.GraphStore
}

func (a *webImportGraphAdapter) AddNodes(names []string) error {
	a.inner.AddNodes(names, nil)
	return nil
}

func (a *webImportGraphAdapter) AddEdges(relations [][3]string, relationHashes []string) error {
	edges := make([]storage.Edge, 0, len(relations))
	for _, relation := range relations {
		edges = append(edges, storage.Edge{Source: relation[0], Target: relation[2]})
	}
	_, err := a.inner.AddEdges(edges, nil, relationHashes)
	return err
}

func (a *webImportGraphAdapter) BatchUpdate(fn func() error) error {
	return a.inner.BatchUpdate(fn)
}

func (a *webImportGraphAdapter) HasData() bool { return a.inner.HasData() }

func (a *webImportGraphAdapter) Load() error { return a.inner.Load(a.inner.DataDir()) }

func (a *webImportGraphAdapter) Save() error { return a.inner.Save(a.inner.DataDir()) }

// webImportVectorAdapter 把 storage.VectorStore 适配为 WebImportVectorStore
type webImportVectorAdapter struct {
	inner *storage.VectorStore
}

func (a *webImportVectorAdapter) Dimension() int { return a.inner.Dimension() }

func (a *webImportVectorAdapter) Contains(id string) bool { return a.inner.Contains(id) }

func (a *webImportVectorAdapter) Add(vectors [][]float32, ids []string) (int, error) {
	return a.inner.Add(vectors, ids)
}

func (a *webImportVectorAdapter) Save(embeddingFingerprint map[string]any) error {
	return a.inner.Save(a.inner.DataDir(), embeddingFingerprint)
}

func (a *webImportVectorAdapter) HasData() bool { return a.inner.HasData() }

func (a *webImportVectorAdapter) Load() error {
	return a.inner.Load(a.inner.DataDir(), nil, nil, "")
}

// webImportEmbeddingAdapter 把 EmbeddingAPIAdapter 适配为 WebImportEmbedding
type webImportEmbeddingAdapter struct {
	inner *embedding.EmbeddingAPIAdapter
}

func (a *webImportEmbeddingAdapter) Encode(ctx context.Context, text string) ([]float32, error) {
	if a.inner == nil {
		return nil, errors.New("嵌入管理器未初始化")
	}
	return a.inner.EncodeOne(ctx, text, nil)
}

func (a *webImportEmbeddingAdapter) EncodeBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if a.inner == nil {
		return nil, errors.New("嵌入管理器未初始化")
	}
	return a.inner.Encode(ctx, texts, nil)
}

func (a *webImportEmbeddingAdapter) DetectDimension(ctx context.Context) (int, error) {
	if a.inner == nil {
		return 0, errors.New("嵌入管理器未初始化")
	}
	return a.inner.DetectDimension(ctx)
}

// webImportRelationWriterAdapter 把内核关系写入服务适配为 SummaryRelationWriter
type webImportRelationWriterAdapter struct {
	inner memruntime.RelationWriteService
}

func (a *webImportRelationWriterAdapter) UpsertRelationsWithVectors(ctx context.Context, relations [][3]string, confidence float64, sourceParagraph string, writeVector bool) error {
	if a.inner == nil {
		return errors.New("关系写入服务未初始化")
	}
	_, err := a.inner.UpsertRelationsWithVectors(ctx, relations, confidence, sourceParagraph, nil, writeVector)
	return err
}

// selfCheckEmbeddingAdapter 把嵌入适配器收敛为自检端口
type selfCheckEmbeddingAdapter struct {
	inner *embedding.EmbeddingAPIAdapter
}

func (a selfCheckEmbeddingAdapter) DetectDimension(ctx context.Context) (int, error) {
	if a.inner == nil {
		return 0, errors.New("嵌入管理器未初始化")
	}
	return a.inner.DetectDimension(ctx)
}

func (a selfCheckEmbeddingAdapter) Encode(ctx context.Context, text string) ([]float32, error) {
	if a.inner == nil {
		return nil, errors.New("嵌入管理器未初始化")
	}
	return a.inner.EncodeOneUncached(ctx, text)
}

// vectorSnapshotSource 避免 typed-nil 进入自检接口
func vectorSnapshotSource(store *storage.VectorStore) utils.VectorStoreSnapshotSource {
	if store == nil {
		return nil
	}
	return store
}

// webImportSelfCheck 执行运行时自检
func (r *Runtime) webImportSelfCheck(ctx context.Context) utils.RuntimeSelfCheckReport {
	return utils.RunEmbeddingRuntimeSelfCheck(ctx, utils.SelfCheckParams{
		Config:               r.kernel.Cfg,
		VectorStore:          vectorSnapshotSource(r.vectorStore),
		ParagraphVectorStore: vectorSnapshotSource(r.paragraphVectorStore),
		GraphVectorStore:     vectorSnapshotSource(r.graphVectorStore),
		EmbeddingManager:     selfCheckEmbeddingAdapter{inner: r.embeddingManager},
		Logger:               r.logger,
	})
}

// webImportCommandRunner 返回 WebUI 命令执行器，未注入时返回 nil 让导入显式失败
func (r *Runtime) webImportCommandRunner() utils.WebImportCommandRunner {
	return r.webImportRunner
}

// webImportDeps 构造 Web 导入管理器依赖
func (r *Runtime) webImportDeps() utils.WebImportDeps {
	dataDir := r.kernel.DataDir()
	scriptsRoot := memorix.ScriptsRoot()
	deps := utils.WebImportDeps{
		Config:         r.kernel.Cfg,
		Logger:         r.logger,
		DataDir:        dataDir,
		ImportRoot:     filepath.Join(dataDir, "imports"),
		RepoRoot:       memorix.RepoRoot(),
		ScriptsRoot:    scriptsRoot,
		MaibotSourceDB: filepath.Join(memorix.RepoRoot(), "data", "MaiBot.db"),
		MigrationPath:  filepath.Join(scriptsRoot, "migrate_maibot_memory.py"),
		ConvertPath:    filepath.Join(scriptsRoot, "convert_lpmm.py"),
		RuntimeReady:   r.kernel.IsRuntimeReady,
		AllowMetadataOnly: func() bool {
			return r.kernel.AllowMetadataOnlyWrite()
		},
		EmbeddingDegraded: func() bool {
			return memruntime.PyTruthy(r.kernel.EmbeddingDegraded()["active"])
		},
		DualVectorPools: r.kernel.DualVectorPoolsEnabled,
		PersistVectorStore: func(store utils.WebImportVectorStore) {
			if typed, ok := store.(*webImportVectorAdapter); ok && typed.inner != nil {
				r.kernel.SaveVectorStore(typed.inner)
			}
		},
		EnqueueBackfill: func(paragraphHash string, errorText string) error {
			r.kernel.EnqueueParagraphVectorBackfill(paragraphHash, errorText)
			return nil
		},
		RecordPersonEvidence: func(personIDs []string, reason string) {
			r.kernel.RuntimeFacade().RecordPersonEvidenceWritten(personIDs, reason)
		},
		SelfCheck: r.webImportSelfCheck,
		StrategySelector: func(content string, override string, chatLog bool) (string, error) {
			strategy, err := storage.SelectImportStrategy(content, override, chatLog)
			if err != nil {
				return "", err
			}
			return string(strategy), nil
		},
		KnowledgeTypeResolver: func(value any, content string) (string, error) {
			knowledgeType, err := storage.ResolveStoredKnowledgeType(value, content, true, nil)
			if err != nil {
				return "", err
			}
			return string(knowledgeType), nil
		},
		CommandRunner: r.webImportCommandRunner(),
		Now:           func() float64 { return float64(time.Now().UnixNano()) / 1e9 },
		WriteChanged:  func(payload map[string]any) {},
	}
	if r.metadataStore != nil {
		deps.Store = &webImportStoreAdapter{inner: r.metadataStore}
	}
	if r.graphStore != nil {
		deps.Graph = &webImportGraphAdapter{inner: r.graphStore}
	}
	if paragraphStore := r.paragraphPoolStore(); paragraphStore != nil {
		deps.ParagraphVec = &webImportVectorAdapter{inner: paragraphStore}
	}
	if graphStore := r.graphPoolStore(); graphStore != nil {
		deps.GraphVec = &webImportVectorAdapter{inner: graphStore}
	}
	if r.embeddingManager != nil {
		deps.Embedding = &webImportEmbeddingAdapter{inner: r.embeddingManager}
	}
	if r.relationWriteService != nil {
		deps.RelationWriter = &webImportRelationWriterAdapter{inner: r.relationWriteService}
	}
	return deps
}

// tuningRuntimeAdapter 把原生运行时适配为检索调优运行时
type tuningRuntimeAdapter struct {
	runtime *Runtime
}

func (a *tuningRuntimeAdapter) MissingDependencies() []string {
	missing := make([]string, 0, 4)
	if a.runtime.metadataStore == nil {
		missing = append(missing, "metadata_store")
	}
	if a.runtime.vectorStore == nil {
		missing = append(missing, "vector_store")
	}
	if a.runtime.embeddingManager == nil {
		missing = append(missing, "embedding_manager")
	}
	if a.runtime.retriever == nil {
		missing = append(missing, "retriever")
	}
	return missing
}

func (a *tuningRuntimeAdapter) RuntimeReady() bool { return a.runtime.kernel.IsRuntimeReady() }

func (a *tuningRuntimeAdapter) ApplyRetrievalTuningProfile(ctx context.Context, profile map[string]any, validate bool) (utils.RetrievalTuningApplyOutcome, error) {
	result, err := a.runtime.kernel.ApplyRetrievalTuningProfile(ctx, profile, validate)
	if err != nil {
		return utils.RetrievalTuningApplyOutcome{}, err
	}
	outcome := utils.RetrievalTuningApplyOutcome{
		RuntimeRebuilt:   memruntime.PyTruthy(result["runtime_rebuilt"]),
		ValidationPassed: memruntime.PyTruthy(result["validation_passed"]),
	}
	if text, ok := result["error"].(string); ok {
		outcome.ApplyError = text
	}
	return outcome, nil
}

// newTuningManager 构造检索调优管理器
func (r *Runtime) newTuningManager() *utils.RetrievalTuningManager {
	return utils.NewRetrievalTuningManager(utils.RetrievalTuningManagerOptions{
		Config:        r.kernel.Cfg,
		Logger:        r.logger,
		ArtifactsRoot: filepath.Join(r.kernel.DataDir(), "artifacts", "retrieval_tuning"),
		ImportWriteBlocked: func() bool {
			return r.importManager != nil && r.importManager.IsWriteBlocked()
		},
		Runtime: &tuningRuntimeAdapter{runtime: r},
	})
}

// startTaskWorkers 启动导入与调优后台消费协程
func (r *Runtime) startTaskWorkers() {
	if r.importManager != nil && r.importCancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		r.importCancel = cancel
		r.importDone = make(chan struct{})
		done := r.importDone
		go func() {
			defer close(done)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if r.importManager.ProcessNext(ctx) {
					continue
				}
				timer := time.NewTimer(500 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	if r.tuningManager != nil {
		r.tuningManager.StartWorker(context.Background())
	}
}

// shutdownImportManager 停止导入消费协程并关闭管理器
func (r *Runtime) shutdownImportManager(ctx context.Context) error {
	if r.importDone != nil || r.importCancel != nil {
		task := utils.StoppableTask{Name: "web_import_worker", Cancel: r.importCancel, Done: r.importDone}
		if err := utils.CancelAndWaitForTasks(ctx, "关闭 Web 导入任务", []utils.StoppableTask{task}, func(message string) {
			r.logger.Error(message)
		}); err != nil {
			return err
		}
		r.importCancel = nil
		r.importDone = nil
	}
	if r.importManager != nil {
		r.importManager.Shutdown(ctx)
	}
	return nil
}

// shutdownTuningManager 停止检索调优协程
func (r *Runtime) shutdownTuningManager(ctx context.Context) error {
	if r.tuningManager == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		r.tuningManager.Shutdown()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ensureImportManager 在存储就绪后构造导入管理器
func (r *Runtime) ensureImportManager() {
	if r.importManager != nil || r.metadataStore == nil {
		return
	}
	r.importManager = utils.NewWebImportManager(r.webImportDeps())
	r.kernel.SetImportTaskManager(r.importManager)
}

// ensureTuningManager 在存储就绪后构造调优管理器
func (r *Runtime) ensureTuningManager() {
	if r.tuningManager != nil {
		return
	}
	r.tuningManager = r.newTuningManager()
	r.kernel.SetRetrievalTuningManager(r.tuningManager)
}

// importManagerStatus 返回导入管理器是否可用
func (r *Runtime) importManagerStatus() map[string]any {
	if r.importManager == nil {
		return map[string]any{"available": false}
	}
	return map[string]any{"available": true, "write_blocked": r.importManager.IsWriteBlocked()}
}
