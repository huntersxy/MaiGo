package imagememory

import (
	"context"
	"strings"

	"maibot-go/internal/A_memorix/core/storage"
)

// IngestRequest 对齐 Python ingest 的关键字参数
type IngestRequest struct {
	ImageBytes     []byte
	SourceKind     string
	ExternalRef    string
	ScopeType      string
	ChatID         string
	MessageID      string
	ComponentPath  string
	OccurredAt     *float64
	UserStatement  string
	InstallationID string
}

// Ingest 对齐 Python ingest，发布图片并登记出现记录
func (r *ImageMemoryRuntime) Ingest(ctx context.Context, request IngestRequest) (map[string]any, error) {
	_ = ctx
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	asset, _, err := r.publishAssetOnly(request.ImageBytes)
	if err != nil {
		return nil, err
	}
	assetID := rowString(asset, "asset_id")
	assetStorageKey := rowString(asset, "storage_key")
	assetContentHash := rowString(asset, "content_hash")
	previous, err := r.metadataStore.GetImageOccurrenceByRef(request.ExternalRef)
	if err != nil {
		return nil, err
	}
	previousAssetID := rowString(previous, "asset_id")
	released := []map[string]any{}
	var occurrence map[string]any
	var job map[string]any
	snapshot := r.snapshot()
	err = r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		created, upsertErr := r.metadataStore.UpsertImageOccurrence(storage.ImageOccurrenceInput{
			AssetID:        assetID,
			ExternalRef:    request.ExternalRef,
			SourceKind:     request.SourceKind,
			ScopeType:      request.ScopeType,
			ChatID:         request.ChatID,
			MessageID:      request.MessageID,
			ComponentPath:  request.ComponentPath,
			OccurredAt:     request.OccurredAt,
			InstallationID: request.InstallationID,
			Conn:           tx.Conn(),
		})
		if upsertErr != nil {
			return upsertErr
		}
		occurrence = created
		if strings.TrimSpace(request.UserStatement) != "" {
			if _, observationErr := r.metadataStore.AddImageObservation(storage.ImageObservationInput{
				OccurrenceID:  rowString(created, "occurrence_id"),
				Text:          request.UserStatement,
				SourceKind:    "user_statement",
				ConfirmStatus: "stated",
				Evidence:      map[string]any{"message_id": request.MessageID},
				Conn:          tx.Conn(),
			}); observationErr != nil {
				return observationErr
			}
		}
		if snapshot.status == "ready" {
			fpHash := FingerprintHash(snapshot.fingerprint)
			ready, embeddingErr := r.metadataStore.GetImageEmbedding(assetID, fpHash)
			if embeddingErr != nil {
				return embeddingErr
			}
			if ready == nil || rowString(ready, "status") != "ready" {
				job, embeddingErr = r.metadataStore.EnqueueImageEmbeddingJob(assetID, fpHash, snapshot.generation, tx.Conn())
				if embeddingErr != nil {
					return embeddingErr
				}
			}
		}
		if previousAssetID != "" && previousAssetID != assetID {
			previousAsset, assetErr := r.metadataStore.GetImageAsset(previousAssetID)
			if assetErr != nil {
				return assetErr
			}
			if previousAsset != nil {
				aborted, abortErr := r.metadataStore.AbortImageJobsIfUnreferenced(previousAssetID, tx.Conn())
				if abortErr != nil {
					return abortErr
				}
				if aborted {
					released = append(released, map[string]any{
						"asset_id":    previousAssetID,
						"storage_key": rowString(previousAsset, "storage_key"),
					})
				}
			}
		}
		return nil
	})
	if err != nil {
		cleanupErr := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
			aborted, abortErr := r.metadataStore.AbortImageJobsIfUnreferenced(assetID, tx.Conn())
			if abortErr != nil {
				return abortErr
			}
			if aborted {
				released = append(released, map[string]any{"asset_id": assetID, "storage_key": assetStorageKey})
			}
			return nil
		})
		if cleanupErr != nil {
			return nil, cleanupErr
		}
		if finalizeErr := r.FinalizeReleasedAssets(released); finalizeErr != nil {
			return nil, finalizeErr
		}
		return nil, err
	}
	if finalizeErr := r.FinalizeReleasedAssets(released); finalizeErr != nil {
		return nil, finalizeErr
	}
	pendingDescriptions, err := r.metadataStore.ListPendingImageDescriptions(assetContentHash)
	if err != nil {
		return nil, err
	}
	for _, pending := range pendingDescriptions {
		if _, applyErr := r.applyModelDescriptionLocked(assetContentHash, rowString(pending, "text")); applyErr != nil {
			return nil, applyErr
		}
		err := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
			_, deleteErr := r.metadataStore.DeletePendingImageDescription(
				assetContentHash,
				rowString(pending, "description_hash"),
				tx.Conn(),
			)
			return deleteErr
		})
		if err != nil {
			return nil, err
		}
	}
	finalSnapshot := r.snapshot()
	embeddingStatus := rowString(job, "status")
	if embeddingStatus == "" {
		if finalSnapshot.status == "ready" {
			embeddingStatus = "ready"
		} else {
			embeddingStatus = finalSnapshot.status
		}
	}
	return map[string]any{
		"success":          true,
		"asset_id":         assetID,
		"occurrence_id":    rowString(occurrence, "occurrence_id"),
		"content_hash":     assetContentHash,
		"embedding_status": embeddingStatus,
	}, nil
}

// ProcessJobsOnce 对齐 Python process_jobs_once，领取并发布图片嵌入任务
func (r *ImageMemoryRuntime) ProcessJobsOnce(ctx context.Context) (map[string]any, error) {
	status, err := r.EnsureEmbeddingSpace(ctx)
	if err != nil {
		return nil, err
	}
	store := r.VectorStore()
	if rowString(status, "status") != "ready" || store == nil {
		result := map[string]any{"claimed": 0, "processed": 0}
		finalStatus, statusErr := r.Status()
		if statusErr != nil {
			return nil, statusErr
		}
		mergeMaps(result, finalStatus)
		return result, nil
	}
	snapshot := r.snapshot()
	fpHash := FingerprintHash(snapshot.fingerprint)
	enqueueBatch, err := r.cfgInt("job_enqueue_batch_size", 200)
	if err != nil {
		return nil, err
	}
	if _, err := r.metadataStore.EnqueueUnindexedImageAssets(fpHash, snapshot.generation, enqueueBatch); err != nil {
		return nil, err
	}
	leaseToken := randomHex32()
	leaseSeconds, err := r.cfgFloat("job_lease_seconds", 120.0)
	if err != nil {
		return nil, err
	}
	maxRetries, err := r.cfgInt("job_max_retries", 5)
	if err != nil {
		return nil, err
	}
	jobBatch, err := r.cfgInt("job_batch_size", 4)
	if err != nil {
		return nil, err
	}
	jobs, err := r.metadataStore.ClaimImageEmbeddingJobs(leaseToken, leaseSeconds, 1+maxRetries, jobBatch)
	if err != nil {
		return nil, err
	}
	processed := 0
	for _, job := range jobs {
		jobID := rowString(job, "job_id")
		assetID := rowString(job, "asset_id")
		jobErr := r.processEmbeddingJob(ctx, jobID, assetID, leaseToken, fpHash, snapshot, store, &processed)
		if jobErr != nil {
			if _, failErr := r.metadataStore.FailImageEmbeddingJob(jobID, leaseToken, jobErr.Error()); failErr != nil {
				return nil, failErr
			}
		}
	}
	if processed > 0 {
		if err := r.persist(store, snapshot.fingerprint); err != nil {
			return nil, err
		}
	}
	minTrainThreshold, err := r.cfgInt("min_train_threshold", 40)
	if err != nil {
		return nil, err
	}
	if store.NeedsTraining(minTrainThreshold) {
		warmup := store.WarmupIndex(true)
		if !truthy(warmup["ok"]) {
			return nil, runtimeErrorf("图片索引训练失败: %s", pythonStr(warmup["error"]))
		}
		if err := r.persist(store, snapshot.fingerprint); err != nil {
			return nil, err
		}
	}
	result := map[string]any{"claimed": len(jobs), "processed": processed}
	finalStatus, err := r.Status()
	if err != nil {
		return nil, err
	}
	mergeMaps(result, finalStatus)
	return result, nil
}

// processEmbeddingJob 处理单条图片嵌入任务，返回错误由调用方标记失败
func (r *ImageMemoryRuntime) processEmbeddingJob(
	ctx context.Context,
	jobID string,
	assetID string,
	leaseToken string,
	fpHash string,
	snapshot runtimeSnapshot,
	store *storage.VectorStore,
	processed *int,
) error {
	asset, err := r.metadataStore.GetImageAsset(assetID)
	if err != nil {
		return err
	}
	if asset == nil {
		return notFoundErrorf("图片资产元数据不存在")
	}
	payload, err := r.assetStore.ReadBytes(rowString(asset, "storage_key"))
	if err != nil {
		return err
	}
	encoded, err := r.embedder.Embed(ctx, payload, rowString(asset, "mime_type"), "")
	if err != nil {
		return err
	}
	vector := embeddingVector(encoded)
	if FingerprintHash(mapFromAny(encoded["fingerprint"])) != fpHash {
		return runtimeErrorf("图片嵌入指纹与任务不一致")
	}
	if len(vector) != store.Dimension() {
		return runtimeErrorf("图片嵌入维度与图片向量池不一致")
	}
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	publishable, err := r.metadataStore.ImageEmbeddingJobIsPublishable(jobID, leaseToken, snapshot.generation, assetID)
	if err != nil {
		return err
	}
	if !publishable {
		return nil
	}
	if _, err := store.Restore([]string{assetID}); err != nil {
		return err
	}
	if _, err := store.Add([][]float32{vector}, []string{assetID}); err != nil {
		return err
	}
	published, err := r.metadataStore.PublishImageEmbedding(
		jobID,
		leaseToken,
		assetID,
		fpHash,
		assetID,
		snapshot.generation,
		int64(len(vector)),
	)
	if err != nil {
		return err
	}
	if !published {
		if _, deleteErr := store.Delete([]string{assetID}); deleteErr != nil {
			return deleteErr
		}
		return nil
	}
	*processed = *processed + 1
	return nil
}
