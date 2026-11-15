package imagememory

import (
	"context"
	"math"

	"MaiGo/internal/A_memorix/core/storage"
)

// ExportVectors 对齐 Python export_vectors，导出指定资产的图片向量 npz
func (r *ImageMemoryRuntime) ExportVectors(assetIDs []string) ([]byte, error) {
	store := r.VectorStore()
	if store == nil {
		return nil, nil
	}
	vectors, err := store.GetVectors(assetIDs)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		if _, ok := vectors[assetID]; ok {
			ids = append(ids, assetID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows := make([][]float32, 0, len(ids))
	for _, assetID := range ids {
		rows = append(rows, vectors[assetID])
	}
	return encodeImageVectorsNPZ(ids, rows)
}

// ImportVectors 对齐 Python import_vectors，导入记忆包图片向量
func (r *ImageMemoryRuntime) ImportVectors(ctx context.Context, payload []byte, assetMap map[string]string, fingerprint map[string]any) (int, error) {
	status, err := r.EnsureEmbeddingSpace(ctx)
	if err != nil {
		return 0, err
	}
	store := r.VectorStore()
	currentFingerprint := r.Fingerprint()
	if rowString(status, "status") != "ready" || fingerprint == nil || !fingerprintEqual(fingerprint, currentFingerprint) || store == nil {
		return 0, nil
	}
	arrays, err := decodeImageVectorsNPZ(payload)
	if err != nil {
		return 0, err
	}
	if hasDuplicateStrings(arrays.IDs) || !arrays.TwoDimensional || arrays.Rows != len(arrays.IDs) || arrays.Cols != store.Dimension() {
		return 0, validationErrorf("记忆包图片向量的 ID、数量或维度无效")
	}
	if !allFiniteVectors(arrays.Vectors) {
		return 0, validationErrorf("记忆包图片向量包含非有限数值")
	}
	imported := 0
	addedIDs := []string{}
	fpHash := FingerprintHash(currentFingerprint)
	generation := r.Generation()
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	err = r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		for index, sourceID := range arrays.IDs {
			vector := arrays.Vectors[index]
			assetID := assetMap[sourceID]
			if assetID == "" {
				return validationErrorf("记忆包图片向量引用了未知资产")
			}
			asset, err := r.metadataStore.GetImageAsset(assetID)
			if err != nil {
				return err
			}
			if asset == nil || rowString(asset, "status") != "active" {
				return validationErrorf("记忆包图片向量引用的资产已失效")
			}
			wasActive := store.Contains(assetID)
			if _, err := store.Restore([]string{assetID}); err != nil {
				return err
			}
			if _, err := store.Add([][]float32{vector}, []string{assetID}); err != nil {
				return err
			}
			if !wasActive {
				addedIDs = append(addedIDs, assetID)
			}
			if err := r.metadataStore.RecordImportedImageEmbedding(
				assetID,
				fpHash,
				assetID,
				generation,
				int64(store.Dimension()),
				tx.Conn(),
			); err != nil {
				return err
			}
			imported++
		}
		return nil
	})
	if err != nil {
		if len(addedIDs) > 0 {
			if _, deleteErr := store.Delete(addedIDs); deleteErr != nil {
				return 0, deleteErr
			}
		}
		return 0, err
	}
	if imported > 0 {
		if err := r.persist(store, currentFingerprint); err != nil {
			return 0, err
		}
	}
	return imported, nil
}

// hasDuplicateStrings 判断字符串列表是否有重复
func hasDuplicateStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

// allFiniteVectors 判断二维向量是否全部有限
func allFiniteVectors(vectors [][]float32) bool {
	for _, vector := range vectors {
		for _, value := range vector {
			if !isFiniteValue(float64(value)) {
				return false
			}
		}
	}
	return true
}

// isFiniteValue 判断浮点是否有限
func isFiniteValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
