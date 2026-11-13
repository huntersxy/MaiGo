package native

// 向量重建执行器，对齐 Python vector_runtime_service.py 的
// _encode_and_add_rebuild_vectors / _copy_or_encode_dual_rebuild_vectors /
// _execute_vector_rebuild_locked / _backfill_missing_dual_vector_pool_entries

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/runtime/services"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

type rebuildItem struct {
	ID   string
	Text string
}

type rebuildStats struct {
	Done   int
	Failed int
}

func rebuildStatsMap(stats map[string]rebuildStats) map[string]map[string]int {
	out := map[string]map[string]int{}
	for key, value := range stats {
		out[key] = map[string]int{"done": value.Done, "failed": value.Failed}
	}
	return out
}

// ensureRebuildEmbeddingUnchanged 对齐 _ensure_rebuild_embedding_unchanged
func (r *Runtime) ensureRebuildEmbeddingUnchanged(expected string) error {
	if expected == "" || r.embeddingManager == nil {
		return nil
	}
	current, err := r.embeddingManager.GetEmbeddingConfigurationKey()
	if err != nil {
		return nil
	}
	if strings.TrimSpace(current) != strings.TrimSpace(expected) {
		return &services.EmbeddingConfigurationChangedError{Message: "嵌入模型已切换，停止旧向量构建"}
	}
	return nil
}

// advanceRebuildProgress 对齐 _advance_vector_rebuild_progress
func (r *Runtime) advanceRebuildProgress(stage string, done, failed int) {
	prefix := map[string]string{
		"paragraphs_start": "paragraph",
		"entities_start":   "entity",
		"relations_start":  "relation",
	}[stage]
	if prefix == "" {
		return
	}
	progress := map[string]any{}
	if done > 0 {
		progress[prefix+"_done"] = done
	}
	if failed > 0 {
		progress[prefix+"_failed"] = failed
	}
	if len(progress) > 0 {
		r.dualMigrationService().UpdateAutoMigrationStage(context.Background(), stage, progress)
	}
}

// encodeAndAddRebuildVectors 对齐 _encode_and_add_rebuild_vectors
func (r *Runtime) encodeAndAddRebuildVectors(
	ctx context.Context,
	items []rebuildItem,
	batchSize int,
	target *storage.VectorStore,
	itemType string,
	checkpoint *services.VectorRebuildCheckpoint,
	configKey string,
) (done int, failed int, lastError string, doneIDs []string, failedIDs []string) {
	if target == nil || r.embeddingManager == nil {
		for _, item := range items {
			failedIDs = append(failedIDs, item.ID)
		}
		return 0, len(items), "vector_runtime_components_missing", nil, failedIDs
	}
	if batchSize < 1 {
		batchSize = 1
	}
	for start := 0; start < len(items); start += batchSize {
		if err := r.ensureRebuildEmbeddingUnchanged(configKey); err != nil {
			return done, failed, err.Error(), doneIDs, failedIDs
		}
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[start:end]
		if checkpoint != nil {
			points := make([]services.VectorRebuildCheckpointItem, 0, len(batch))
			for _, item := range batch {
				points = append(points, services.VectorRebuildCheckpointItem{ID: item.ID, Text: item.Text})
			}
			restored, err := checkpoint.Read(itemType, points)
			if err != nil {
				return done, failed, err.Error(), doneIDs, failedIDs
			}
			if len(restored) > 0 {
				ids := make([]string, 0, len(restored))
				vectors := make([][]float32, 0, len(restored))
				for id, vector := range restored {
					ids = append(ids, id)
					vectors = append(vectors, vector)
				}
				sort.Strings(ids)
				sortedVectors := make([][]float32, 0, len(ids))
				for _, id := range ids {
					sortedVectors = append(sortedVectors, restored[id])
				}
				if _, err := target.Add(sortedVectors, ids); err != nil {
					return done, failed, err.Error(), doneIDs, failedIDs
				}
				done += len(ids)
				doneIDs = append(doneIDs, ids...)
				r.advanceRebuildProgress(itemTypeDoneStage(itemType), len(ids), 0)
				remaining := batch[:0]
				for _, item := range batch {
					if _, ok := restored[item.ID]; !ok {
						remaining = append(remaining, item)
					}
				}
				batch = remaining
			}
		}
		if len(batch) == 0 {
			continue
		}
		ids := make([]string, 0, len(batch))
		texts := make([]string, 0, len(batch))
		for _, item := range batch {
			ids = append(ids, item.ID)
			texts = append(texts, item.Text)
		}
		vectors, err := r.embeddingManager.Encode(ctx, texts, nil)
		if err != nil {
			lastError = truncateRebuildError(err.Error())
			failed += len(ids)
			failedIDs = append(failedIDs, ids...)
			r.advanceRebuildProgress(itemTypeDoneStage(itemType), 0, len(ids))
			r.logger.Warning(fmt.Sprintf("[native] 重建向量批次失败: start=%d count=%d error=%s", start, len(ids), lastError))
			continue
		}
		if err := r.ensureRebuildEmbeddingUnchanged(configKey); err != nil {
			return done, failed, err.Error(), doneIDs, failedIDs
		}
		if len(vectors) != len(ids) {
			lastError = fmt.Sprintf("embedding 返回数量异常: expected=%d, got=%d", len(ids), len(vectors))
			failed += len(ids)
			failedIDs = append(failedIDs, ids...)
			r.advanceRebuildProgress(itemTypeDoneStage(itemType), 0, len(ids))
			continue
		}
		if _, err := target.Add(vectors, ids); err != nil {
			lastError = truncateRebuildError(err.Error())
			failed += len(ids)
			failedIDs = append(failedIDs, ids...)
			r.advanceRebuildProgress(itemTypeDoneStage(itemType), 0, len(ids))
			continue
		}
		if checkpoint != nil {
			points := make([]services.VectorRebuildCheckpointItem, 0, len(batch))
			for _, item := range batch {
				points = append(points, services.VectorRebuildCheckpointItem{ID: item.ID, Text: item.Text})
			}
			if err := checkpoint.Write(itemType, points, vectors); err != nil {
				lastError = truncateRebuildError(err.Error())
			}
		}
		done += len(ids)
		doneIDs = append(doneIDs, ids...)
		r.advanceRebuildProgress(itemTypeDoneStage(itemType), len(ids), 0)
	}
	if failed == 0 {
		lastError = ""
	}
	return done, failed, lastError, doneIDs, failedIDs
}

func itemTypeDoneStage(itemType string) string {
	switch itemType {
	case "entity":
		return "entities_start"
	case "relation":
		return "relations_start"
	default:
		return "paragraphs_start"
	}
}

func truncateRebuildError(message string) string {
	if len(message) > 500 {
		return message[:500]
	}
	return message
}

// copyRebuildVectorsFromStore 对齐 _copy_rebuild_vectors_from_store
func (r *Runtime) copyRebuildVectorsFromStore(
	source *storage.VectorStore,
	target *storage.VectorStore,
	pairs [][2]string,
	batchSize int,
) (copied int, copiedIDs []string, missing [][2]string) {
	if source == nil || target == nil || len(pairs) == 0 {
		return 0, nil, pairs
	}
	if batchSize < 1 {
		batchSize = 1024
	}
	pairBySource := make(map[string]string, len(pairs))
	sourceIDs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		pairBySource[pair[0]] = pair[1]
		sourceIDs = append(sourceIDs, pair[0])
	}
	copiedSet := map[string]struct{}{}
	for start := 0; start < len(sourceIDs); start += batchSize {
		end := start + batchSize
		if end > len(sourceIDs) {
			end = len(sourceIDs)
		}
		vectors, err := source.GetVectors(sourceIDs[start:end])
		if err != nil {
			r.logger.Warning("[native] 读取旧向量失败，将回退 embedding 重建: " + err.Error())
			return 0, nil, pairs
		}
		targetIDs := make([]string, 0, len(vectors))
		targetVectors := make([][]float32, 0, len(vectors))
		for sourceID, vector := range vectors {
			targetID, ok := pairBySource[sourceID]
			if !ok {
				continue
			}
			if _, exists := copiedSet[sourceID]; exists {
				continue
			}
			targetIDs = append(targetIDs, targetID)
			targetVectors = append(targetVectors, vector)
			copiedSet[sourceID] = struct{}{}
			copiedIDs = append(copiedIDs, sourceID)
		}
		if len(targetIDs) == 0 {
			continue
		}
		if _, err := target.Add(targetVectors, targetIDs); err != nil {
			r.logger.Warning("[native] 复制旧向量到新池失败，将回退 embedding 重建: " + err.Error())
			return 0, nil, pairs
		}
		r.advanceRebuildProgress("paragraphs_start", len(targetIDs), 0)
	}
	missing = make([][2]string, 0)
	for _, pair := range pairs {
		if _, ok := copiedSet[pair[0]]; !ok {
			missing = append(missing, pair)
		}
	}
	return len(copiedIDs), copiedIDs, missing
}

// CopyOrEncodeDualRebuildVectorsNative 对齐 _copy_or_encode_dual_rebuild_vectors
func (r *Runtime) CopyOrEncodeDualRebuildVectorsNative(ctx context.Context, params map[string]any) (map[string]any, error) {
	itemType := ""
	if value, ok := params["target_id_prefix"].(string); ok {
		itemType = strings.TrimSpace(value)
	}
	items := rebuildItemsFromAny(params["items"], itemType)
	target, _ := params["target_store"].(*storage.VectorStore)
	source, _ := params["source_store"].(*storage.VectorStore)
	checkpoint, _ := params["checkpoint"].(*services.VectorRebuildCheckpoint)
	batchSize := intFromAny(params["batch_size"], 32)
	inputPrefix := itemType
	if inputPrefix == "" {
		inputPrefix = "paragraph"
	}
	inputs := r.kernel.VectorSpaceInputs()
	idPairs := make([][2]string, 0, len(items))
	changedPairs := make([][2]string, 0)
	for _, item := range items {
		targetID := item.ID
		if itemType != "" {
			targetID = itemType + ":" + item.ID
		}
		sourceID := item.ID
		if r.kernel.ActiveVectorSpaceID() != "" {
			sourceID = targetID
		}
		pair := [2]string{sourceID, targetID}
		if stored, ok := inputs[inputPrefix+":"+item.ID]; ok {
			if stored != r.vectorSpace.InputHash(item.Text) {
				changedPairs = append(changedPairs, pair)
				continue
			}
		}
		idPairs = append(idPairs, pair)
	}
	copied, copiedSourceIDs, missingPairs := r.copyRebuildVectorsFromStore(source, target, idPairs, batchSize)
	missingPairs = append(missingPairs, changedPairs...)
	textByID := make(map[string]string, len(items))
	for _, item := range items {
		textByID[item.ID] = item.Text
	}
	encodeItems := make([]rebuildItem, 0, len(missingPairs))
	for _, pair := range missingPairs {
		sourceID := pair[0]
		if r.kernel.ActiveVectorSpaceID() != "" && itemType != "" {
			if index := strings.Index(sourceID, ":"); index >= 0 {
				sourceID = sourceID[index+1:]
			}
		}
		if text := strings.TrimSpace(textByID[sourceID]); text != "" {
			encodeItems = append(encodeItems, rebuildItem{ID: pair[1], Text: text})
		}
	}
	done, failed, lastError, encodedDoneIDs, encodedFailedIDs := r.encodeAndAddRebuildVectors(
		ctx, encodeItems, batchSize, target, inputPrefix, checkpoint, "",
	)
	convert := func(id string) string {
		if r.kernel.ActiveVectorSpaceID() != "" && itemType != "" {
			if index := strings.Index(id, ":"); index >= 0 {
				return id[index+1:]
			}
		}
		return id
	}
	doneSourceIDs := make([]string, 0, len(copiedSourceIDs)+len(encodedDoneIDs))
	for _, id := range copiedSourceIDs {
		doneSourceIDs = append(doneSourceIDs, convert(id))
	}
	for _, id := range encodedDoneIDs {
		doneSourceIDs = append(doneSourceIDs, convert(id))
	}
	failedSourceIDs := make([]string, 0, len(encodedFailedIDs))
	for _, id := range encodedFailedIDs {
		failedSourceIDs = append(failedSourceIDs, convert(id))
	}
	skippedMissing := len(missingPairs) - len(encodeItems)
	if skippedMissing > 0 {
		failed += skippedMissing
	}
	for _, item := range items {
		targetID := item.ID
		if itemType != "" {
			targetID = itemType + ":" + item.ID
		}
		if target != nil && target.Contains(targetID) {
			r.kernel.RecordVectorSpaceInput(inputPrefix, item.ID, item.Text)
		}
	}
	return map[string]any{
		"done": copied + done, "failed": failed, "error": lastError,
		"done_ids": doneSourceIDs, "failed_ids": failedSourceIDs,
		"copy_stats": map[string]int{"copied": copied, "encoded": done, "missing": len(missingPairs)},
	}, nil
}

// EncodeAndAddRebuildVectorsNative 对齐 _encode_and_add_rebuild_vectors 的端口入口
func (r *Runtime) EncodeAndAddRebuildVectorsNative(ctx context.Context, params map[string]any) (map[string]any, error) {
	items := rebuildItemsFromAny(params["items"], "")
	target, _ := params["vector_store"].(*storage.VectorStore)
	checkpoint, _ := params["checkpoint"].(*services.VectorRebuildCheckpoint)
	itemType := "paragraph"
	if value, ok := params["item_type"].(string); ok && strings.TrimSpace(value) != "" {
		itemType = strings.TrimSpace(value)
	}
	done, failed, lastError, doneIDs, failedIDs := r.encodeAndAddRebuildVectors(
		ctx, items, intFromAny(params["batch_size"], 32), target, itemType, checkpoint, "",
	)
	return map[string]any{
		"done": done, "failed": failed, "error": lastError,
		"done_ids": doneIDs, "failed_ids": failedIDs,
	}, nil
}

func rebuildItemsFromAny(value any, prefix string) []rebuildItem {
	out := make([]rebuildItem, 0)
	switch typed := value.(type) {
	case []rebuildItem:
		return typed
	case [][2]string:
		for _, pair := range typed {
			out = append(out, rebuildItem{ID: pair[0], Text: pair[1]})
		}
	case []any:
		for _, item := range typed {
			switch pair := item.(type) {
			case [2]string:
				out = append(out, rebuildItem{ID: pair[0], Text: pair[1]})
			case []any:
				if len(pair) >= 2 {
					out = append(out, rebuildItem{ID: fmt.Sprint(pair[0]), Text: fmt.Sprint(pair[1])})
				}
			}
		}
	}
	return out
}

func intFromAny(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	default:
		return fallback
	}
}

// ---------------------------------------------------------------------------
// 重建执行器
// ---------------------------------------------------------------------------

type rebuildOptions struct {
	BatchSize        int
	IncludeRelations *bool
	DryRun           bool
	ReuseSpace       bool
}

func parseRebuildOptions(options map[string]any) rebuildOptions {
	out := rebuildOptions{BatchSize: 32}
	if options == nil {
		return out
	}
	out.BatchSize = intFromAny(options["batch_size"], 32)
	if out.BatchSize < 1 {
		out.BatchSize = 32
	}
	if value, ok := options["include_relations"].(bool); ok {
		out.IncludeRelations = &value
	}
	out.DryRun = memruntime.PyTruthy(options["dry_run"])
	out.ReuseSpace = memruntime.PyTruthy(options["reuse_space"])
	return out
}

// RebuildAllVectorsNative 对齐 _rebuild_all_vectors
func (r *Runtime) RebuildAllVectorsNative(ctx context.Context, options map[string]any) (map[string]any, error) {
	parsed := parseRebuildOptions(options)
	lock := r.kernel.VectorRebuildLock()
	if lock == nil {
		return r.rebuildAllVectorsLockedNative(ctx, parsed)
	}
	lock.Lock()
	defer lock.Unlock()
	return r.rebuildAllVectorsLockedNative(ctx, parsed)
}

// RebuildAllVectorsLockedNative 对齐 _rebuild_all_vectors_locked
func (r *Runtime) RebuildAllVectorsLockedNative(ctx context.Context, options map[string]any) (map[string]any, error) {
	return r.rebuildAllVectorsLockedNative(ctx, parseRebuildOptions(options))
}

func (r *Runtime) rebuildAllVectorsLockedNative(ctx context.Context, options rebuildOptions) (map[string]any, error) {
	if options.DryRun {
		return r.executeVectorRebuild(ctx, options)
	}
	started := nowSeconds()
	patch := map[string]any{
		"running": true, "attempted": true, "success": false, "stage": "prepare_rebuild",
		"progress": map[string]any{}, "task": "rebuild", "started_at": started,
		"finished_at": nil, "last_error": "",
	}
	r.mergeMigrationStatus(patch)
	configKey := ""
	if r.embeddingManager != nil {
		if value, err := r.embeddingManager.GetEmbeddingConfigurationKey(); err == nil {
			configKey = value
		}
	}
	result, err := r.executeVectorRebuildWithKey(ctx, options, configKey)
	if err != nil {
		merge := map[string]any{
			"running": false, "stage": "failed", "last_error": err.Error(),
			"finished_at": nowSeconds(), "updated_at": nowSeconds(),
		}
		if services.IsEmbeddingConfigurationChanged(err) {
			merge["stage"] = "cancelled"
		}
		r.mergeMigrationStatus(merge)
		return nil, err
	}
	success := memruntime.PyTruthy(result["success"])
	stage := "failed"
	if success {
		stage = "completed"
	}
	lastError := strings.Join(toStrings(result["errors"]), "; ")
	if lastError == "" {
		lastError = fmt.Sprint(result["error"])
		if lastError == "<nil>" {
			lastError = ""
		}
	}
	r.mergeMigrationStatus(map[string]any{
		"running": false, "success": success, "stage": stage,
		"last_error": lastError, "finished_at": nowSeconds(), "updated_at": nowSeconds(),
	})
	return result, nil
}

func (r *Runtime) executeVectorRebuild(ctx context.Context, options rebuildOptions) (map[string]any, error) {
	return r.executeVectorRebuildWithKey(ctx, options, "")
}

func (r *Runtime) executeVectorRebuildWithKey(ctx context.Context, options rebuildOptions, configKey string) (map[string]any, error) {
	if r.metadataStore == nil || r.embeddingManager == nil {
		return map[string]any{"success": false, "error": "runtime_components_missing"}, nil
	}
	counts := r.rebuildTargetCounts(ctx)
	relationEnabled := r.kernel.RelationVectorsEnabled()
	if options.IncludeRelations != nil {
		relationEnabled = *options.IncludeRelations
	}
	if !relationEnabled {
		counts["relations"] = 0
	}
	total := counts["paragraphs"] + counts["entities"] + counts["relations"]
	if options.DryRun {
		result := map[string]any{
			"success": true, "dry_run": true,
			"counts": map[string]any{"paragraphs": counts["paragraphs"], "entities": counts["entities"], "relations": counts["relations"]},
			"total":  total,
		}
		for key, value := range r.vectorRebuildStatusSnapshot(ctx) {
			result[key] = value
		}
		return result, nil
	}

	started := nowSeconds()
	batchSize := options.BatchSize
	if batchSize < 1 {
		batchSize = cfgInt(r.kernel.Config(), "embedding.batch_size", 32)
	}
	if batchSize < 1 {
		batchSize = 32
	}
	dimension := r.kernel.DetectCurrentEmbeddingDimensionForRebuild()
	if dimension < 1 {
		dimension = r.kernel.CurrentEmbeddingStatusDimension()
	}
	if dimension < 1 {
		dimension = 1
	}
	r.kernel.SetEmbeddingDegraded(true, "正在重建全部向量，检索临时降级", &started)

	dualMode := r.kernel.DualVectorPoolsConfigEnabled()
	var legacySource *storage.VectorStore
	if dualMode {
		legacySource = r.vectorStore
		if legacySource != nil && !r.kernel.StoredVectorsCompatibleWithCurrentEmbedding(legacySource) {
			legacySource = nil
		}
	}
	r.updateRebuildStage(ctx, "prepare_rebuild", map[string]any{
		"dual_mode": dualMode, "total": total,
		"counts":                  map[string]any{"paragraphs": counts["paragraphs"], "entities": counts["entities"], "relations": counts["relations"]},
		"legacy_source_available": legacySource != nil,
	})
	if legacySource != nil && legacySource.HasData() {
		if err := legacySource.Load("", nil, nil, ""); err != nil {
			r.logger.Warning("[native] 加载旧单池向量用于双池迁移失败，将回退 embedding 重建: " + err.Error())
		} else {
			legacySource.WarmupIndex(false)
		}
	}

	var singleBuildRoot string
	var dualBuildRoot, buildParagraphDir, buildGraphDir string
	var buildParagraphStore, buildGraphStore *storage.VectorStore
	if !dualMode {
		r.kernel.SetDualVectorPoolsReady(false)
		r.kernel.RemoveDualVectorReadyManifest()
		root, err := os.MkdirTemp(r.kernel.VectorsRoot(), "single_build_")
		if err != nil {
			return nil, err
		}
		singleBuildRoot = root
		buildMain, err := r.kernel.MakeVectorStore(singleBuildRoot, &dimension)
		if err != nil {
			return nil, err
		}
		r.setVectorStore(buildMain)
		paragraphStore, err := r.kernel.MakeVectorStore(r.kernel.ParagraphVectorDir(), nil)
		if err != nil {
			return nil, err
		}
		graphStore, err := r.kernel.MakeVectorStore(r.kernel.GraphVectorDir(), nil)
		if err != nil {
			return nil, err
		}
		r.setParagraphVectorStore(paragraphStore)
		r.setGraphVectorStore(graphStore)
		r.refreshRelationWriteService()
	} else {
		dualBuildRoot, buildParagraphDir, buildGraphDir = r.kernel.PrepareDualVectorBuildDirs()
		var err error
		buildParagraphStore, err = r.kernel.MakeVectorStore(buildParagraphDir, &dimension)
		if err != nil {
			return nil, err
		}
		buildGraphStore, err = r.kernel.MakeVectorStore(buildGraphDir, &dimension)
		if err != nil {
			return nil, err
		}
	}

	fingerprint := r.kernel.CurrentEmbeddingFingerprint(nil)
	if fingerprint == nil || strings.TrimSpace(fmt.Sprint(fingerprint["hash"])) == "" {
		return nil, fmt.Errorf("重建检查点需要已确认的模型指纹")
	}
	checkpoint, err := services.NewVectorRebuildCheckpoint(
		filepath.Join(r.kernel.VectorsRoot(), "rebuild_checkpoint.sqlite3"),
		fmt.Sprint(fingerprint["hash"]), dimension,
	)
	if err != nil {
		return nil, err
	}
	defer checkpoint.Close()

	stats := map[string]rebuildStats{
		"paragraphs": {}, "entities": {}, "relations": {},
	}
	migrationStats := map[string]map[string]int{
		"paragraphs": {"copied": 0, "encoded": 0, "missing": 0},
		"entities":   {"copied": 0, "encoded": 0, "missing": 0},
		"relations":  {"copied": 0, "encoded": 0, "missing": 0},
	}
	paragraphItems := r.rebuildParagraphItems(ctx)
	r.updateRebuildStage(ctx, "paragraphs_start", map[string]any{"paragraph_items": len(paragraphItems)})
	var paragraphFailedIDs []string
	if dualMode {
		result, err := r.CopyOrEncodeDualRebuildVectorsNative(ctx, map[string]any{
			"items": paragraphItems, "batch_size": batchSize, "checkpoint": checkpoint,
			"target_store": buildParagraphStore, "source_store": legacySource,
		})
		if err != nil {
			return nil, err
		}
		stats["paragraphs"] = rebuildStats{Done: intFromAny(result["done"], 0), Failed: intFromAny(result["failed"], 0)}
		if copyStats, ok := result["copy_stats"].(map[string]int); ok {
			migrationStats["paragraphs"] = copyStats
		}
		paragraphFailedIDs = toStrings(result["failed_ids"])
	} else {
		done, failed, _, doneIDs, failedIDs := r.encodeAndAddRebuildVectors(ctx, paragraphItems, batchSize, r.vectorStore, "paragraph", checkpoint, configKey)
		stats["paragraphs"] = rebuildStats{Done: done, Failed: failed}
		_, paragraphFailedIDs = doneIDs, failedIDs
	}
	r.updateRebuildStage(ctx, "paragraphs_done", map[string]any{
		"paragraph_done": stats["paragraphs"].Done, "paragraph_failed": stats["paragraphs"].Failed,
		"paragraph_migration": migrationStats["paragraphs"],
	})

	entityItems := r.rebuildEntityItems(ctx)
	r.updateRebuildStage(ctx, "entities_start", map[string]any{"entity_items": len(entityItems)})
	var entityFailedIDs []string
	if dualMode {
		result, err := r.CopyOrEncodeDualRebuildVectorsNative(ctx, map[string]any{
			"items": entityItems, "batch_size": batchSize, "checkpoint": checkpoint,
			"target_store": buildGraphStore, "target_id_prefix": "entity", "source_store": legacySource,
		})
		if err != nil {
			return nil, err
		}
		stats["entities"] = rebuildStats{Done: intFromAny(result["done"], 0), Failed: intFromAny(result["failed"], 0)}
		if copyStats, ok := result["copy_stats"].(map[string]int); ok {
			migrationStats["entities"] = copyStats
		}
		entityFailedIDs = toStrings(result["failed_ids"])
	} else {
		done, failed, _, _, failedIDs := r.encodeAndAddRebuildVectors(ctx, entityItems, batchSize, r.vectorStore, "entity", checkpoint, configKey)
		stats["entities"] = rebuildStats{Done: done, Failed: failed}
		entityFailedIDs = failedIDs
	}
	r.updateRebuildStage(ctx, "entities_done", map[string]any{
		"entity_done": stats["entities"].Done, "entity_failed": stats["entities"].Failed,
		"entity_migration": migrationStats["entities"],
	})

	var relationDoneIDs, relationFailedIDs []string
	relationError := ""
	if relationEnabled {
		relationItems := r.rebuildRelationItems(ctx)
		r.updateRebuildStage(ctx, "relations_start", map[string]any{"relation_items": len(relationItems)})
		if dualMode {
			result, err := r.CopyOrEncodeDualRebuildVectorsNative(ctx, map[string]any{
				"items": relationItems, "batch_size": batchSize, "checkpoint": checkpoint,
				"target_store": buildGraphStore, "target_id_prefix": "relation", "source_store": legacySource,
			})
			if err != nil {
				return nil, err
			}
			stats["relations"] = rebuildStats{Done: intFromAny(result["done"], 0), Failed: intFromAny(result["failed"], 0)}
			if copyStats, ok := result["copy_stats"].(map[string]int); ok {
				migrationStats["relations"] = copyStats
			}
			relationDoneIDs = toStrings(result["done_ids"])
			relationFailedIDs = toStrings(result["failed_ids"])
			relationError = fmt.Sprint(result["error"])
			if relationError == "<nil>" {
				relationError = ""
			}
		} else {
			done, failed, lastError, doneIDs, failedIDs := r.encodeAndAddRebuildVectors(ctx, relationItems, batchSize, r.vectorStore, "relation", checkpoint, configKey)
			stats["relations"] = rebuildStats{Done: done, Failed: failed}
			relationDoneIDs, relationFailedIDs = doneIDs, failedIDs
			relationError = lastError
		}
		r.updateRebuildStage(ctx, "relations_done", map[string]any{
			"relation_done": stats["relations"].Done, "relation_failed": stats["relations"].Failed,
			"relation_migration": migrationStats["relations"],
		})
	}

	errors := make([]string, 0)
	retryGroups := []struct {
		Group       string
		ItemType    string
		Items       []rebuildItem
		PendingIDs  []string
		TargetStore *storage.VectorStore
	}{
		{"paragraphs", "paragraph", paragraphItems, paragraphFailedIDs, pickStore(dualMode, buildParagraphStore, r.vectorStore)},
		{"entities", "entity", entityItems, entityFailedIDs, pickStore(dualMode, buildGraphStore, r.vectorStore)},
	}
	if relationEnabled {
		retryGroups = append(retryGroups, struct {
			Group       string
			ItemType    string
			Items       []rebuildItem
			PendingIDs  []string
			TargetStore *storage.VectorStore
		}{"relations", "relation", r.rebuildRelationItems(ctx), relationFailedIDs, pickStore(dualMode, buildGraphStore, r.vectorStore)})
	}
	for _, group := range retryGroups {
		if len(group.PendingIDs) == 0 {
			continue
		}
		pending := map[string]struct{}{}
		for _, id := range group.PendingIDs {
			pending[id] = struct{}{}
		}
		retryItems := make([]rebuildItem, 0, len(group.PendingIDs))
		for _, item := range group.Items {
			if _, ok := pending[item.ID]; !ok {
				continue
			}
			retryID := item.ID
			if dualMode && group.ItemType != "paragraph" {
				retryID = group.ItemType + ":" + item.ID
			}
			retryItems = append(retryItems, rebuildItem{ID: retryID, Text: item.Text})
		}
		r.updateRebuildStage(ctx, group.Group+"_start", map[string]any{"retrying": true})
		done, remaining, retryError, retryDoneIDs, retryFailedIDs := r.encodeAndAddRebuildVectors(
			ctx, retryItems, batchSize, group.TargetStore, group.ItemType, checkpoint, configKey,
		)
		stats[group.Group] = rebuildStats{Done: stats[group.Group].Done + done, Failed: remaining}
		if remaining > 0 {
			errors = append(errors, fmt.Sprintf("%s:%s", group.Group, retryError))
		}
		r.updateRebuildStage(ctx, group.Group+"_done", map[string]any{
			"retrying":                         false,
			groupPrefix(group.Group) + "_done": stats[group.Group].Done, groupPrefix(group.Group) + "_failed": remaining,
		})
		if group.Group == "relations" {
			relationDoneIDs = append(relationDoneIDs, retryDoneIDs...)
			relationFailedIDs = retryFailedIDs
			if retryError != "" {
				relationError = retryError
			}
		}
	}
	if relationEnabled && (len(relationDoneIDs) > 0 || len(relationFailedIDs) > 0) {
		r.updateRelationVectorStates(relationDoneIDs, relationFailedIDs, relationError)
	}

	if err := r.ensureRebuildEmbeddingUnchanged(configKey); err != nil {
		return nil, err
	}
	doneTotal, failedTotal := 0, 0
	for _, item := range stats {
		doneTotal += item.Done
		failedTotal += item.Failed
	}
	if failedTotal > 0 {
		_ = checkpoint.Discard()
	}
	activationOK := true
	if dualMode {
		expectedParagraph := stats["paragraphs"].Done
		expectedGraph := stats["entities"].Done + stats["relations"].Done
		actualParagraph := buildParagraphStore.NumVectors()
		actualGraph := buildGraphStore.NumVectors()
		r.updateRebuildStage(ctx, "activation_check", map[string]any{
			"stats": rebuildStatsMap(stats), "migration": migrationStats,
			"actual_paragraph_vectors": actualParagraph, "expected_paragraph_vectors": expectedParagraph,
			"actual_graph_vectors": actualGraph, "expected_graph_vectors": expectedGraph,
		})
		if failedTotal == 0 && actualParagraph == expectedParagraph && actualGraph == expectedGraph {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						activationOK = false
						errors = append(errors, fmt.Sprintf("dual_pool_activation:%v", recovered))
						r.kernel.DropDualBuildRoot(dualBuildRoot)
						r.kernel.ReloadDualVectorStoresFromDisk()
					}
				}()
				r.updateRebuildStage(ctx, "paragraph_pool_warmup", nil)
				buildParagraphStore.WarmupIndex(true)
				r.updateRebuildStage(ctx, "paragraph_pool_save", nil)
				if err := buildParagraphStore.Save(buildParagraphDir, fingerprint); err != nil {
					panic(err)
				}
				r.updateRebuildStage(ctx, "graph_pool_warmup", nil)
				buildGraphStore.WarmupIndex(true)
				r.updateRebuildStage(ctx, "graph_pool_save", nil)
				if err := buildGraphStore.Save(buildGraphDir, fingerprint); err != nil {
					panic(err)
				}
				if err := r.ensureRebuildEmbeddingUnchanged(configKey); err != nil {
					panic(err)
				}
				r.updateRebuildStage(ctx, "activate_dirs", nil)
				r.kernel.ActivateDualVectorBuildDirs(dualBuildRoot)
				r.updateRebuildStage(ctx, "write_manifest", nil)
				r.kernel.WriteDualVectorReadyManifest(rebuildStatsMap(stats), copyMigrationStats(migrationStats), "", fingerprint)
				r.updateRebuildStage(ctx, "reload_dual_stores", nil)
				activationOK = r.kernel.ReloadDualVectorStoresFromDisk()
				if !activationOK {
					errors = append(errors, "dual_pool_activation:ready_manifest_unusable")
					return
				}
				r.updateRebuildStage(ctx, "dual_backfill", nil)
				backfill, err := r.BackfillMissingDualVectorPoolEntriesNative(ctx, batchSize)
				if err != nil {
					errors = append(errors, "dual_backfill:"+err.Error())
				} else {
					if !memruntime.PyTruthy(backfill["success"]) {
						errors = append(errors, toStrings(backfill["errors"])...)
					}
				}
				r.updateRebuildStage(ctx, "dual_backfill_done", nil)
				r.updateRebuildStage(ctx, "clear_legacy_single_pool", nil)
				r.kernel.ClearLegacySingleVectorFilesAfterDualReady()
			}()
		} else {
			activationOK = false
			if failedTotal == 0 {
				errors = append(errors, fmt.Sprintf(
					"dual_pool_activation:vector_count_mismatch paragraph=%d/%d, graph=%d/%d",
					actualParagraph, expectedParagraph, actualGraph, expectedGraph,
				))
			}
			r.kernel.DropDualBuildRoot(dualBuildRoot)
			r.kernel.ReloadDualVectorStoresFromDisk()
		}
		r.refreshRelationWriteService()
	} else {
		if failedTotal > 0 {
			_ = os.RemoveAll(singleBuildRoot)
			store, err := r.kernel.MakeVectorStore(r.kernel.VectorsRoot(), &dimension)
			if err == nil {
				r.setVectorStore(store)
				_ = store.Load("", nil, nil, "")
			}
		} else if singleBuildRoot != "" {
			r.updateRebuildStage(ctx, "single_pool_warmup", nil)
			r.vectorStore.WarmupIndex(true)
			if err := r.activateSingleVectorBuild(singleBuildRoot, fingerprint); err != nil {
				return nil, err
			}
		}
		paragraphStore, _ := r.kernel.MakeVectorStore(r.kernel.ParagraphVectorDir(), nil)
		graphStore, _ := r.kernel.MakeVectorStore(r.kernel.GraphVectorDir(), nil)
		r.setParagraphVectorStore(paragraphStore)
		r.setGraphVectorStore(graphStore)
		r.refreshRelationWriteService()
	}
	r.updateRebuildStage(ctx, "runtime_rebuild", nil)
	if bundle, err := r.buildSearchRuntime(r.kernel.Config()); err == nil {
		_ = r.applySearchRuntimeBundle(bundle)
	}
	r.updateRebuildStage(ctx, "self_check", nil)
	report, _ := r.kernel.RefreshRuntimeSelfCheck("A_Memorix vector rebuild self check")
	if err := r.ensureRebuildEmbeddingUnchanged(configKey); err != nil {
		return nil, err
	}
	success := failedTotal == 0 && len(errors) == 0
	if success && memruntime.PyTruthy(report["ok"]) && activationOK {
		r.kernel.SetEmbeddingDegraded(false, "", nil)
	} else if success && !memruntime.PyTruthy(report["ok"]) {
		_ = report
	}
	r.persistKernel()
	return map[string]any{
		"success": success, "counts": map[string]any{
			"paragraphs": counts["paragraphs"], "entities": counts["entities"], "relations": counts["relations"],
		},
		"stats": rebuildStatsMap(stats), "migration": migrationStats,
		"errors": errors, "total": total, "done": doneTotal, "failed": failedTotal,
		"dual_mode": dualMode, "activation_ok": activationOK,
	}, nil
}

func pickStore(dual bool, dualStore, singleStore *storage.VectorStore) *storage.VectorStore {
	if dual {
		return dualStore
	}
	return singleStore
}

func copyMigrationStats(source map[string]map[string]int) map[string]map[string]int {
	out := make(map[string]map[string]int, len(source))
	for key, value := range source {
		copyValue := make(map[string]int, len(value))
		for innerKey, innerValue := range value {
			copyValue[innerKey] = innerValue
		}
		out[key] = copyValue
	}
	return out
}

func groupPrefix(group string) string {
	switch group {
	case "paragraphs":
		return "paragraph"
	case "entities":
		return "entity"
	default:
		return "relation"
	}
}

func (r *Runtime) updateRebuildStage(ctx context.Context, stage string, progress map[string]any) {
	if progress == nil {
		progress = map[string]any{}
	}
	r.dualMigrationService().UpdateAutoMigrationStage(ctx, stage, progress)
}

func (r *Runtime) activateSingleVectorBuild(buildRoot string, fingerprint map[string]any) error {
	root := r.kernel.VectorsRoot()
	if err := r.vectorStore.Save(buildRoot, fingerprint); err != nil {
		return err
	}
	for _, filename := range []string{"vectors.bin", "vectors_ids.bin"} {
		path := filepath.Join(buildRoot, filename)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = os.WriteFile(path, nil, 0o644)
		}
	}
	entries, err := os.ReadDir(buildRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		source := filepath.Join(buildRoot, entry.Name())
		target := filepath.Join(root, entry.Name())
		_ = os.Remove(target)
		if err := os.Rename(source, target); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(buildRoot)
	store, err := r.kernel.MakeVectorStore(root, nil)
	if err != nil {
		return err
	}
	r.setVectorStore(store)
	if err := store.Load("", fingerprint, nil, ""); err != nil {
		return err
	}
	store.WarmupIndex(true)
	return nil
}

func (r *Runtime) setVectorStore(store *storage.VectorStore) {
	r.vectorStore = store
	r.kernel.SetVectorStore(store)
}

func (r *Runtime) setParagraphVectorStore(store *storage.VectorStore) {
	r.paragraphVectorStore = store
	r.kernel.SetParagraphVectorStore(store)
}

func (r *Runtime) setGraphVectorStore(store *storage.VectorStore) {
	r.graphVectorStore = store
	r.kernel.SetGraphVectorStore(store)
}

func (r *Runtime) rebuildParagraphItems(ctx context.Context) []rebuildItem {
	where := r.kernel.ActiveRowFilterSQL("paragraphs")
	rows, err := r.metadataStore.Query("SELECT hash, content FROM paragraphs WHERE " + where + " ORDER BY created_at ASC")
	if err != nil {
		return nil
	}
	items := make([]rebuildItem, 0, len(rows))
	for _, row := range rows {
		hashValue := strings.TrimSpace(utils.PyStr(row["hash"]))
		content := strings.TrimSpace(utils.PyStr(row["content"]))
		if hashValue == "" || content == "" {
			continue
		}
		items = append(items, rebuildItem{ID: hashValue, Text: content})
	}
	return items
}

func (r *Runtime) rebuildEntityItems(ctx context.Context) []rebuildItem {
	where := r.kernel.ActiveRowFilterSQL("entities")
	rows, err := r.metadataStore.Query("SELECT hash, name FROM entities WHERE " + where + " ORDER BY created_at ASC")
	if err != nil {
		return nil
	}
	items := make([]rebuildItem, 0, len(rows))
	for _, row := range rows {
		hashValue := strings.TrimSpace(utils.PyStr(row["hash"]))
		name := strings.TrimSpace(utils.PyStr(row["name"]))
		if hashValue == "" || name == "" {
			continue
		}
		items = append(items, rebuildItem{ID: hashValue, Text: name})
	}
	return items
}

func (r *Runtime) rebuildRelationItems(ctx context.Context) []rebuildItem {
	where := r.kernel.ActiveRowFilterSQL("relations")
	rows, err := r.metadataStore.Query("SELECT hash, subject, predicate, object FROM relations WHERE " + where + " ORDER BY created_at ASC")
	if err != nil {
		return nil
	}
	items := make([]rebuildItem, 0, len(rows))
	for _, row := range rows {
		hashValue := strings.TrimSpace(utils.PyStr(row["hash"]))
		if hashValue == "" {
			continue
		}
		text := memruntime.BuildRelationVectorText(
			utils.PyStr(row["subject"]), utils.PyStr(row["predicate"]), utils.PyStr(row["object"]),
		)
		items = append(items, rebuildItem{ID: hashValue, Text: text})
	}
	return items
}

func (r *Runtime) updateRelationVectorStates(doneIDs []string, failedIDs []string, failure string) {
	conn, err := r.metadataStore.GetConnection()
	if err != nil {
		return
	}
	now := nowSeconds()
	update := func(ids []string, state string, errorText *string) {
		for start := 0; start < len(ids); start += 500 {
			end := start + 500
			if end > len(ids) {
				end = len(ids)
			}
			batch := ids[start:end]
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
			args := make([]any, 0, len(batch)+2)
			args = append(args, now)
			if errorText == nil {
				query := "UPDATE relations SET vector_state = ?, vector_updated_at = ?, vector_error = NULL WHERE hash IN (" + placeholders + ")"
				args = []any{state, now}
				for _, id := range batch {
					args = append(args, id)
				}
				_, _ = conn.Exec(query, args...)
				continue
			}
			query := "UPDATE relations SET vector_state = ?, vector_updated_at = ?, vector_error = ? WHERE hash IN (" + placeholders + ")"
			args = []any{state, now, *errorText}
			for _, id := range batch {
				args = append(args, id)
			}
			_, _ = conn.Exec(query, args...)
		}
	}
	if len(doneIDs) > 0 {
		update(doneIDs, "ready", nil)
	}
	if len(failedIDs) > 0 {
		message := failure
		if message == "" {
			message = "dual_pool_backfill_failed"
		}
		if len(message) > 500 {
			message = message[:500]
		}
		update(failedIDs, "failed", &message)
	}
}

func (r *Runtime) rebuildTargetCounts(ctx context.Context) map[string]int {
	counts := map[string]int{"paragraphs": 0, "entities": 0, "relations": 0}
	if r.metadataStore == nil {
		return counts
	}
	count := func(table, column, where string) int {
		rows, err := r.metadataStore.Query("SELECT COUNT(*) AS count FROM " + table + " WHERE " + where + " AND " + column + " IS NOT NULL AND TRIM(" + column + ") <> ''")
		if err != nil || len(rows) == 0 {
			return 0
		}
		return intFromAny(rows[0]["count"], 0)
	}
	counts["paragraphs"] = count("paragraphs", "content", r.kernel.ActiveRowFilterSQL("paragraphs"))
	counts["entities"] = count("entities", "name", r.kernel.ActiveRowFilterSQL("entities"))
	counts["relations"] = count("relations", "hash", r.kernel.ActiveRowFilterSQL("relations"))
	return counts
}

func (r *Runtime) vectorRebuildStatusSnapshot(ctx context.Context) map[string]any {
	result, err := r.kernel.VectorRebuildStatus()
	if err != nil || result == nil {
		return map[string]any{}
	}
	return result
}

// BackfillMissingDualVectorPoolEntriesNative 对齐 _backfill_missing_dual_vector_pool_entries
func (r *Runtime) BackfillMissingDualVectorPoolEntriesNative(ctx context.Context, batchSize int) (map[string]any, error) {
	if r.metadataStore == nil || r.vectorStore == nil || r.paragraphVectorStore == nil || r.graphVectorStore == nil || !r.kernel.DualVectorPoolsEnabled() {
		return map[string]any{"success": false, "error": "dual_pool_not_ready"}, nil
	}
	if batchSize < 1 {
		batchSize = cfgInt(r.kernel.Config(), "embedding.batch_size", 32)
	}
	if batchSize < 1 {
		batchSize = 32
	}
	stats := map[string]rebuildStats{"paragraphs": {}, "entities": {}, "relations": {}}
	migration := map[string]map[string]int{
		"paragraphs": {"copied": 0, "encoded": 0, "missing": 0},
		"entities":   {"copied": 0, "encoded": 0, "missing": 0},
		"relations":  {"copied": 0, "encoded": 0, "missing": 0},
	}
	errors := make([]string, 0)
	source := r.vectorStore
	if source != nil && !r.kernel.StoredVectorsCompatibleWithCurrentEmbedding(source) {
		source = nil
	}
	if source != nil && source.HasData() {
		if err := source.Load("", nil, nil, ""); err == nil {
			source.WarmupIndex(false)
		}
	}

	paragraphItems := make([]rebuildItem, 0)
	for _, item := range r.rebuildParagraphItems(ctx) {
		if !r.paragraphVectorStore.Contains(item.ID) {
			paragraphItems = append(paragraphItems, item)
		}
	}
	result, err := r.CopyOrEncodeDualRebuildVectorsNative(ctx, map[string]any{
		"items": paragraphItems, "batch_size": batchSize, "target_store": r.paragraphVectorStore, "source_store": source,
	})
	if err != nil {
		return nil, err
	}
	stats["paragraphs"] = rebuildStats{Done: intFromAny(result["done"], 0), Failed: intFromAny(result["failed"], 0)}
	if copyStats, ok := result["copy_stats"].(map[string]int); ok {
		migration["paragraphs"] = copyStats
	}
	if text := strings.TrimSpace(fmt.Sprint(result["error"])); text != "" && text != "<nil>" {
		errors = append(errors, "paragraph_pool_backfill:"+text)
	}

	entityItems := make([]rebuildItem, 0)
	for _, item := range r.rebuildEntityItems(ctx) {
		if !r.graphVectorStore.Contains("entity:" + item.ID) {
			entityItems = append(entityItems, item)
		}
	}
	result, err = r.CopyOrEncodeDualRebuildVectorsNative(ctx, map[string]any{
		"items": entityItems, "batch_size": batchSize, "target_store": r.graphVectorStore,
		"target_id_prefix": "entity", "source_store": source,
	})
	if err != nil {
		return nil, err
	}
	stats["entities"] = rebuildStats{Done: intFromAny(result["done"], 0), Failed: intFromAny(result["failed"], 0)}
	if copyStats, ok := result["copy_stats"].(map[string]int); ok {
		migration["entities"] = copyStats
	}
	if text := strings.TrimSpace(fmt.Sprint(result["error"])); text != "" && text != "<nil>" {
		errors = append(errors, "entity_graph_pool_backfill:"+text)
	}

	var relationDoneIDs, relationFailedIDs []string
	relationError := ""
	if r.kernel.RelationVectorsEnabled() {
		relationItems := make([]rebuildItem, 0)
		for _, item := range r.rebuildRelationItems(ctx) {
			if !r.graphVectorStore.Contains("relation:" + item.ID) {
				relationItems = append(relationItems, item)
			}
		}
		result, err = r.CopyOrEncodeDualRebuildVectorsNative(ctx, map[string]any{
			"items": relationItems, "batch_size": batchSize, "target_store": r.graphVectorStore,
			"target_id_prefix": "relation", "source_store": source,
		})
		if err != nil {
			return nil, err
		}
		stats["relations"] = rebuildStats{Done: intFromAny(result["done"], 0), Failed: intFromAny(result["failed"], 0)}
		if copyStats, ok := result["copy_stats"].(map[string]int); ok {
			migration["relations"] = copyStats
		}
		relationDoneIDs = toStrings(result["done_ids"])
		relationFailedIDs = toStrings(result["failed_ids"])
		relationError = strings.TrimSpace(fmt.Sprint(result["error"]))
		if relationError == "<nil>" {
			relationError = ""
		}
		if relationError != "" {
			errors = append(errors, "relation_graph_pool_backfill:"+relationError)
		}
		if len(relationDoneIDs) > 0 || len(relationFailedIDs) > 0 {
			r.updateRelationVectorStates(relationDoneIDs, relationFailedIDs, relationError)
		}
	}

	failedTotal := 0
	for _, item := range stats {
		failedTotal += item.Failed
	}
	if failedTotal > 0 {
		reason := strings.Join(errors, "; ")
		if reason == "" {
			reason = "dual_pool_backfill_failed"
		}
		checkedAt := nowSeconds()
		r.kernel.SetEmbeddingDegraded(true, truncateRebuildError(reason), &checkedAt)
	}
	if r.paragraphVectorStore != nil {
		_ = r.paragraphVectorStore.Save("", nil)
	}
	if r.graphVectorStore != nil {
		_ = r.graphVectorStore.Save("", nil)
	}
	r.kernel.RefreshDualVectorReadyManifestFromStores()
	errorStrings := make([]string, 0, 5)
	for index, item := range errors {
		if index >= 5 {
			break
		}
		errorStrings = append(errorStrings, item)
	}
	return map[string]any{
		"success": failedTotal == 0, "stats": rebuildStatsMap(stats),
		"migration": migration, "failed": failedTotal, "errors": errorStrings,
	}, nil
}

func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func toStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
