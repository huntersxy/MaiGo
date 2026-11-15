package imagememory

import (
	"context"
	"path/filepath"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
	memorixutils "MaiGo/internal/A_memorix/core/utils"
)

// EnsureEmbeddingSpace 对齐 Python ensure_embedding_space，探测并加载图片向量空间
func (r *ImageMemoryRuntime) EnsureEmbeddingSpace(ctx context.Context) (map[string]any, error) {
	snapshot := r.snapshot()
	if snapshot.vectorStore != nil && snapshot.status == "ready" {
		return r.Status()
	}
	r.initializationMu.Lock()
	defer r.initializationMu.Unlock()
	snapshot = r.snapshot()
	if snapshot.vectorStore != nil && snapshot.status == "ready" {
		return r.Status()
	}
	report, err := r.embedder.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if !truthy(report["available"]) {
		r.setStatus("unavailable", stringOr(report["message"], "图片嵌入模型不可用"))
		return r.Status()
	}
	// Python 用 dict(report.get("fingerprint") or {}) 处理缺失指纹，非空但不可转映射才失败
	fingerprint := map[string]any{}
	if fingerprintValue := report["fingerprint"]; fingerprintValue != nil {
		parsed, ok := asStringMap(fingerprintValue)
		if !ok {
			return nil, runtimeErrorf("图片嵌入探测返回的指纹格式无效")
		}
		fingerprint = fingerprintCopy(parsed)
	}
	fpHash := FingerprintHash(fingerprint)
	dimensionValue := report["dimension"]
	if !truthy(dimensionValue) {
		dimensionValue = fingerprint["dimension"]
	}
	if !truthy(dimensionValue) {
		dimensionValue = 0
	}
	dimension, err := valueToInt(dimensionValue)
	if err != nil {
		return nil, err
	}
	if fpHash == "" || dimension <= 0 {
		r.setStatus("failed", "图片嵌入探测没有返回可验证的指纹和维度")
		return r.Status()
	}
	state, err := r.metadataStore.GetImageRuntimeState()
	if err != nil {
		return nil, err
	}
	previousHash := stringOr(state["fingerprint_hash"], "")
	generation := 0
	if rawGeneration, exists := state["generation"]; exists && rawGeneration != nil {
		generation, err = valueToInt(rawGeneration)
		if err != nil {
			return nil, err
		}
	}
	if previousHash != fpHash {
		generation++
	}
	if generation < 1 {
		generation = 1
	}
	directoryKey := strings.TrimPrefix(fpHash, "sha256:")
	if len(directoryKey) > 24 {
		directoryKey = directoryKey[:24]
	}
	directory := filepath.Join(r.vectorRoot, directoryKey)
	store, err := storage.NewVectorStore(storage.VectorStoreConfig{
		Dimension:        dimension,
		QuantizationType: memorixutils.QuantizationInt8,
		DataDir:          directory,
	})
	if err != nil {
		return nil, err
	}
	minTrainThreshold, err := r.cfgInt("min_train_threshold", 40)
	if err != nil {
		return nil, err
	}
	store.SetMinTrainThreshold(minTrainThreshold)
	if store.HasData() {
		if err := store.Load(directory, fingerprint, nil, ""); err != nil {
			return nil, err
		}
		warmup := store.WarmupIndex(true)
		if !truthy(warmup["ok"]) {
			return nil, runtimeErrorf("图片索引预热失败: %s", pythonStr(warmup["error"]))
		}
	}
	embeddings, err := r.metadataStore.Query(
		"SELECT e.asset_id, a.status AS asset_status FROM image_embeddings e "+
			"JOIN image_assets a ON a.asset_id=e.asset_id WHERE e.fingerprint_hash=? AND e.status='ready'",
		fpHash,
	)
	if err != nil {
		return nil, err
	}
	for _, embedding := range embeddings {
		assetID := rowString(embedding, "asset_id")
		if rowString(embedding, "asset_status") == "active" && !store.Contains(assetID) {
			err := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
				if _, execErr := tx.Exec(
					"UPDATE image_embeddings SET status='pending', updated_at=? WHERE asset_id=? AND fingerprint_hash=?",
					nowTimestamp(), assetID, fpHash,
				); execErr != nil {
					return execErr
				}
				_, enqueueErr := r.metadataStore.EnqueueImageEmbeddingJob(assetID, fpHash, int64(generation), tx.Conn())
				return enqueueErr
			})
			if err != nil {
				return nil, err
			}
		}
	}
	deletedRows, err := r.metadataStore.Query("SELECT asset_id FROM image_assets WHERE status='deleted'")
	if err != nil {
		return nil, err
	}
	deletedIDs := make([]string, 0, len(deletedRows))
	for _, row := range deletedRows {
		deletedIDs = append(deletedIDs, rowString(row, "asset_id"))
	}
	if len(deletedIDs) > 0 {
		deletedCount, err := store.Delete(deletedIDs)
		if err != nil {
			return nil, err
		}
		if deletedCount > 0 {
			if err := r.persist(store, fingerprint); err != nil {
				return nil, err
			}
		}
	}
	err = r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		return r.metadataStore.SetImageRuntimeState(int64(generation), fpHash, int64(dimension), "ready", tx.Conn())
	})
	if err != nil {
		return nil, err
	}
	r.setReady(store, fingerprint, int64(generation), stringOr(report["message"], "图片嵌入空间已就绪"))
	return r.Status()
}

// publishAssetOnly 对齐 Python _publish_asset_only，发布资产并写入元数据
func (r *ImageMemoryRuntime) publishAssetOnly(imageBytes []byte) (map[string]any, InspectedImage, error) {
	inspected, err := r.assetStore.Inspect(imageBytes)
	if err != nil {
		return nil, InspectedImage{}, err
	}
	if inspected.FrameCount > 1 {
		return nil, InspectedImage{}, validationErrorf("当前图片记忆只支持静态图片")
	}
	digest, storageKey, err := r.assetStore.Publish(imageBytes, inspected)
	if err != nil {
		return nil, InspectedImage{}, err
	}
	var asset map[string]any
	err = r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		created, upsertErr := r.metadataStore.UpsertImageAsset(
			digest,
			storageKey,
			inspected.MIMEType,
			int64(inspected.ByteSize),
			int64(inspected.Width),
			int64(inspected.Height),
			tx.Conn(),
		)
		asset = created
		return upsertErr
	})
	if err != nil {
		return nil, InspectedImage{}, err
	}
	return asset, inspected, nil
}

// targetContent 对齐 Python _target_content，解析关联目标正文
func (r *ImageMemoryRuntime) targetContent(targetType string, targetID string) (map[string]any, error) {
	query := ""
	switch targetType {
	case "paragraph":
		query = "SELECT hash AS id, content FROM paragraphs WHERE hash=? AND is_deleted=0"
	case "entity":
		query = "SELECT hash AS id, name AS content FROM entities WHERE hash=? AND is_deleted=0"
	case "relation":
		query = "SELECT hash AS id, subject || ' ' || predicate || ' ' || object AS content FROM relations WHERE hash=? AND is_inactive=0"
	case "episode":
		query = "SELECT episode_id AS id, summary AS content FROM episodes WHERE episode_id=?"
	default:
		return nil, nil
	}
	rows, err := r.metadataStore.Query(query, targetID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return map[string]any{
		"target_type": targetType,
		"target_id":   targetID,
		"content":     pythonStr(rows[0]["content"]),
	}, nil
}

// persist 调用宿主注入的向量持久化回调
func (r *ImageMemoryRuntime) persist(store *storage.VectorStore, fingerprint map[string]any) error {
	if r.persistVectorStore == nil {
		return nil
	}
	return r.persistVectorStore(store, fingerprintCopy(fingerprint))
}
