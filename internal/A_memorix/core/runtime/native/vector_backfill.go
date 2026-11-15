package native

// 段落向量回填执行器
// Python 侧由 MemoryVectorRuntimeService._run_paragraph_backfill_once 实现，
// 这里复用 metadata 的待回填队列与 embedding/向量库端口，避免在 service 层反向依赖内核

import (
	"context"
	"strings"

	"MaiGo/internal/A_memorix/core/utils"
)

// runParagraphBackfillOnce 执行一次段落向量回填
func (r *Runtime) runParagraphBackfillOnce(ctx context.Context, limit *int, maxRetry *int, trigger string) (map[string]any, error) {
	result := map[string]any{"success": false, "processed": 0, "done": 0, "failed": 0, "trigger": trigger}
	target := r.paragraphPoolStore()
	if r.metadataStore == nil || target == nil || r.embeddingManager == nil {
		return result, nil
	}
	if r.kernel.IsEmbeddingDegraded() {
		result["detail"] = "embedding_degraded"
		return result, nil
	}
	safeLimit := 0
	if limit != nil {
		safeLimit = *limit
	}
	if safeLimit <= 0 {
		safeLimit = r.kernel.ParagraphVectorBackfillBatchSize()
	}
	if safeLimit <= 0 {
		safeLimit = 32
	}
	safeRetry := 0
	if maxRetry != nil {
		safeRetry = *maxRetry
	}
	if safeRetry <= 0 {
		safeRetry = r.kernel.ParagraphVectorBackfillMaxRetry()
	}
	if safeRetry <= 0 {
		safeRetry = 1
	}
	rows, err := r.metadataStore.FetchParagraphVectorBackfillBatch(safeLimit, safeRetry, nil)
	if err != nil {
		return result, err
	}
	if len(rows) == 0 {
		result["success"] = true
		return result, nil
	}
	pending := make([]string, 0, len(rows))
	for _, row := range rows {
		hashValue := strings.TrimSpace(utils.PyStr(row["paragraph_hash"]))
		if hashValue != "" {
			pending = append(pending, hashValue)
		}
	}
	if len(pending) == 0 {
		result["success"] = true
		return result, nil
	}
	if err := r.metadataStore.MarkParagraphVectorBackfillRunning(pending, nil); err != nil {
		r.logger.Warning("[native] 标记段落回填运行中失败: " + err.Error())
	}
	paragraphs, err := r.metadataStore.GetParagraphsByHashes(pending)
	if err != nil {
		return result, err
	}
	done := make([]string, 0, len(pending))
	encodeHashes := make([]string, 0, len(pending))
	encodeTexts := make([]string, 0, len(pending))
	for _, hashValue := range pending {
		if target.Contains(hashValue) {
			done = append(done, hashValue)
			continue
		}
		paragraph := paragraphs[hashValue]
		if paragraph == nil {
			done = append(done, hashValue)
			continue
		}
		content := strings.TrimSpace(utils.PyStr(paragraph["content"]))
		if content == "" {
			done = append(done, hashValue)
			continue
		}
		encodeHashes = append(encodeHashes, hashValue)
		encodeTexts = append(encodeTexts, content)
	}
	failed := 0
	lastError := ""
	if len(encodeHashes) > 0 {
		vectors, encodeErr := r.embeddingManager.Encode(ctx, encodeTexts, nil)
		if encodeErr != nil {
			failed = len(encodeHashes)
			lastError = encodeErr.Error()
			for _, hashValue := range encodeHashes {
				_ = r.metadataStore.MarkParagraphVectorBackfillFailed(hashValue, lastError, nil)
			}
		} else {
			for index, hashValue := range encodeHashes {
				if index >= len(vectors) || len(vectors[index]) == 0 {
					failed++
					lastError = "embedding 返回空向量"
					_ = r.metadataStore.MarkParagraphVectorBackfillFailed(hashValue, lastError, nil)
					continue
				}
				if _, addErr := target.Add([][]float32{vectors[index]}, []string{hashValue}); addErr != nil {
					failed++
					lastError = addErr.Error()
					_ = r.metadataStore.MarkParagraphVectorBackfillFailed(hashValue, lastError, nil)
					continue
				}
				if r.vectorSpace != nil {
					r.kernel.RecordVectorSpaceInput("paragraph", hashValue, encodeTexts[index])
				}
				done = append(done, hashValue)
			}
		}
	}
	if len(done) > 0 {
		if err := r.metadataStore.MarkParagraphVectorBackfillDone(done, nil); err != nil {
			r.logger.Warning("[native] 标记段落回填完成失败: " + err.Error())
		}
		r.persistKernel()
	}
	if failed > 0 && r.kernel.EmbeddingFallbackEnabled() {
		r.kernel.SetEmbeddingDegraded(true, truncateText(lastError, 500), nil)
	}
	result["success"] = failed == 0
	result["processed"] = len(done) + failed
	result["done"] = len(done)
	result["failed"] = failed
	return result, nil
}

func truncateText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}
