package imagememory

import (
	"MaiGo/internal/A_memorix/core/storage"
)

// ApplyModelDescription 对齐 Python apply_model_description，写入模型描述或暂存待绑定描述
func (r *ImageMemoryRuntime) ApplyModelDescription(contentHash string, text string) ([]map[string]any, error) {
	description := pythonStrip(stringOr(text, ""))
	if contentHash == "" || description == "" {
		return []map[string]any{}, nil
	}
	descriptionHash := ContentHash([]byte(description))
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	asset, err := r.metadataStore.GetImageAssetByHash(contentHash)
	if err != nil {
		return nil, err
	}
	if asset == nil {
		err := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
			return r.metadataStore.UpsertPendingImageDescription(contentHash, descriptionHash, description, tx.Conn())
		})
		if err != nil {
			return nil, err
		}
		return []map[string]any{}, nil
	}
	return r.applyModelDescriptionLocked(contentHash, description)
}

// applyModelDescriptionLocked 对齐 Python _apply_model_description_locked，调用方需持发布锁
func (r *ImageMemoryRuntime) applyModelDescriptionLocked(contentHash string, text string) ([]map[string]any, error) {
	asset, err := r.metadataStore.GetImageAssetByHash(contentHash)
	if err != nil {
		return nil, err
	}
	description := pythonStrip(stringOr(text, ""))
	if asset == nil || description == "" {
		return []map[string]any{}, nil
	}
	descriptionHash := ContentHash([]byte(description))
	updated := []map[string]any{}
	err = r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		occurrences, listErr := r.metadataStore.ListImageOccurrencesForAsset(rowString(asset, "asset_id"), false)
		if listErr != nil {
			return listErr
		}
		for _, occurrence := range occurrences {
			occurrenceID := rowString(occurrence, "occurrence_id")
			active, observationErr := r.metadataStore.ListImageObservations([]string{occurrenceID})
			if observationErr != nil {
				return observationErr
			}
			alreadyRecorded := false
			for _, item := range active {
				if rowString(item, "source_kind") == "model_description" && rowString(item, "text") == description {
					alreadyRecorded = true
					break
				}
			}
			if !alreadyRecorded {
				if _, addErr := r.metadataStore.AddImageObservation(storage.ImageObservationInput{
					OccurrenceID:  occurrenceID,
					Text:          description,
					SourceKind:    "model_description",
					ConfirmStatus: "unconfirmed",
					Evidence:      map[string]any{"content_hash": contentHash},
					Conn:          tx.Conn(),
				}); addErr != nil {
					return addErr
				}
			}
			if rowString(occurrence, "scope_type") == "chat" && rowString(occurrence, "chat_id") != "" &&
				rowString(occurrence, "message_id") != "" && occurrence["occurred_at"] != nil {
				if err := r.metadataStore.EnqueueImageDescriptionCompensation(occurrenceID, descriptionHash, tx.Conn()); err != nil {
					return err
				}
				updated = append(updated, map[string]any{
					"chat_id":     rowString(occurrence, "chat_id"),
					"message_id":  rowString(occurrence, "message_id"),
					"occurred_at": occurrence["occurred_at"],
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteOccurrence 对齐 Python delete_occurrence，停用出现记录并在独占时释放资产
func (r *ImageMemoryRuntime) DeleteOccurrence(occurrenceID string) (map[string]any, error) {
	occurrence, err := r.metadataStore.GetImageOccurrence(occurrenceID)
	if err != nil {
		return nil, err
	}
	if occurrence == nil {
		return map[string]any{"success": false, "error": "图片出现记录不存在"}, nil
	}
	assetID := rowString(occurrence, "asset_id")
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	released := false
	err = r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		if _, deactivateErr := r.metadataStore.DeactivateImageOccurrence(occurrenceID, tx.Conn()); deactivateErr != nil {
			return deactivateErr
		}
		aborted, abortErr := r.metadataStore.AbortImageJobsIfUnreferenced(assetID, tx.Conn())
		if abortErr != nil {
			return abortErr
		}
		released = aborted
		return nil
	})
	if err != nil {
		return nil, err
	}
	if released {
		asset, assetErr := r.metadataStore.GetImageAsset(assetID)
		if assetErr != nil {
			return nil, assetErr
		}
		if store := r.VectorStore(); store != nil {
			if _, deleteErr := store.Delete([]string{assetID}); deleteErr != nil {
				return nil, deleteErr
			}
			if persistErr := r.persist(store, r.Fingerprint()); persistErr != nil {
				return nil, persistErr
			}
		}
		if asset != nil {
			if _, deleteErr := r.assetStore.Delete(rowString(asset, "storage_key")); deleteErr != nil {
				return nil, deleteErr
			}
		}
	}
	return map[string]any{"success": true, "asset_released": released}, nil
}

// ReleaseInstallation 对齐 Python release_installation，在调用方事务内停用安装包图片
func (r *ImageMemoryRuntime) ReleaseInstallation(installationID string, conn *storage.Conn) ([]map[string]any, error) {
	released := []map[string]any{}
	assetIDs, err := r.metadataStore.DeactivateInstallationImageOccurrences(installationID, conn)
	if err != nil {
		return nil, err
	}
	for _, assetID := range assetIDs {
		aborted, abortErr := r.metadataStore.AbortImageJobsIfUnreferenced(assetID, conn)
		if abortErr != nil {
			return nil, abortErr
		}
		if !aborted {
			continue
		}
		asset, assetErr := r.metadataStore.GetImageAsset(assetID)
		if assetErr != nil {
			return nil, assetErr
		}
		if asset != nil {
			released = append(released, map[string]any{
				"asset_id":    assetID,
				"storage_key": rowString(asset, "storage_key"),
			})
		}
	}
	return released, nil
}

// ReleaseInstallationAtomic 对齐 Python release_installation_atomic，在发布锁内提交卸载
func (r *ImageMemoryRuntime) ReleaseInstallationAtomic(installationID string) ([]map[string]any, error) {
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	var released []map[string]any
	err := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		result, releaseErr := r.ReleaseInstallation(installationID, tx.Conn())
		released = result
		return releaseErr
	})
	if err != nil {
		return nil, err
	}
	return released, nil
}

// FinalizeReleasedAssets 对齐 Python finalize_released_assets，提交后清理向量与资产文件
func (r *ImageMemoryRuntime) FinalizeReleasedAssets(assets []map[string]any) error {
	store := r.VectorStore()
	for _, asset := range assets {
		assetID := rowString(asset, "asset_id")
		if store != nil {
			if _, err := store.Delete([]string{assetID}); err != nil {
				return err
			}
		}
		if _, err := r.assetStore.Delete(rowString(asset, "storage_key")); err != nil {
			return err
		}
	}
	if len(assets) > 0 && store != nil {
		if err := r.persist(store, r.Fingerprint()); err != nil {
			return err
		}
	}
	return nil
}

// ListAssets 对齐 Python list_assets，返回分页资产与运行时状态
func (r *ImageMemoryRuntime) ListAssets(limit int, offset int, chatID string) (map[string]any, error) {
	items, err := r.metadataStore.ListImageAssets(limit, offset, chatID)
	if err != nil {
		return nil, err
	}
	status, err := r.Status()
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	mergeMaps(result, status)
	result["success"] = true
	result["items"] = items
	return result, nil
}

// ListChatStats 对齐 Python list_chat_stats
func (r *ImageMemoryRuntime) ListChatStats() ([]map[string]any, error) {
	return r.metadataStore.ListImageChatStats()
}

// ExportRecords 对齐 Python export_records，导出图片记录与指纹
func (r *ImageMemoryRuntime) ExportRecords(occurrenceIDs []string) (map[string]any, error) {
	records, err := r.metadataStore.ExportImageRecords(occurrenceIDs)
	if err != nil {
		return nil, err
	}
	assets := records.Assets
	if assets == nil {
		assets = []map[string]any{}
	}
	occurrences := records.Occurrences
	if occurrences == nil {
		occurrences = []map[string]any{}
	}
	observations := records.Observations
	if observations == nil {
		observations = []map[string]any{}
	}
	links := records.Links
	if links == nil {
		links = []map[string]any{}
	}
	return map[string]any{
		"assets":       assets,
		"occurrences":  occurrences,
		"observations": observations,
		"links":        links,
		"fingerprint":  r.Fingerprint(),
	}, nil
}
