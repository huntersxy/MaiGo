package imagememory

import (
	"context"
	"encoding/json"

	"MaiGo/internal/A_memorix/core/storage"
)

// AssetLoader 对齐 Python asset_loader 回调
type AssetLoader func(asset map[string]any) ([]byte, error)

// ImportRecordsRequest 对齐 Python import_records 的关键字参数
type ImportRecordsRequest struct {
	Records        map[string]any
	AssetBytes     map[string][]byte
	AssetLoader    AssetLoader
	InstallationID string
	ScopeType      string
	ChatID         string
	TargetIDMap    map[string]map[string]string
}

// ImportRecords 对齐 Python import_records，导入记忆包图片资产、出现记录、认知与关联
func (r *ImageMemoryRuntime) ImportRecords(ctx context.Context, request ImportRecordsRequest) (map[string]any, error) {
	_ = ctx
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	assetMap := map[string]string{}
	assetMapOrder := []string{}
	occurrenceMap := map[string]string{}
	publishedAssets := map[string]string{}
	publishedOrder := []string{}
	importErr := r.importRecordsLocked(request, assetMap, &assetMapOrder, occurrenceMap, publishedAssets, &publishedOrder)
	if importErr != nil {
		released := []map[string]any{}
		cleanupErr := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
			for _, assetID := range publishedOrder {
				aborted, abortErr := r.metadataStore.AbortImageJobsIfUnreferenced(assetID, tx.Conn())
				if abortErr != nil {
					return abortErr
				}
				if aborted {
					released = append(released, map[string]any{
						"asset_id":    assetID,
						"storage_key": publishedAssets[assetID],
					})
				}
			}
			return nil
		})
		if cleanupErr != nil {
			return nil, cleanupErr
		}
		if finalizeErr := r.FinalizeReleasedAssets(released); finalizeErr != nil {
			return nil, finalizeErr
		}
		return nil, importErr
	}
	return map[string]any{
		"assets":      len(assetMap),
		"occurrences": len(occurrenceMap),
		"asset_map":   assetMap,
	}, nil
}

// importRecordsLocked 执行导入主体，调用方需持发布锁
func (r *ImageMemoryRuntime) importRecordsLocked(
	request ImportRecordsRequest,
	assetMap map[string]string,
	assetMapOrder *[]string,
	occurrenceMap map[string]string,
	publishedAssets map[string]string,
	publishedOrder *[]string,
) error {
	for _, asset := range recordList(request.Records, "assets") {
		declaredHash := rowString(asset, "content_hash")
		var payload []byte
		if request.AssetLoader != nil {
			loaded, err := request.AssetLoader(asset)
			if err != nil {
				return err
			}
			payload = loaded
		} else {
			if request.AssetBytes != nil {
				payload = request.AssetBytes[declaredHash]
			}
		}
		if len(payload) == 0 || ContentHash(payload) != declaredHash {
			return validationErrorf("记忆包图片资产与声明哈希不一致: %s", declaredHash)
		}
		published, _, err := r.publishAssetOnly(payload)
		if err != nil {
			return err
		}
		assetID := rowString(published, "asset_id")
		key := rowString(asset, "asset_id")
		if key == "" {
			key = declaredHash
		}
		if _, exists := assetMap[key]; !exists {
			*assetMapOrder = append(*assetMapOrder, key)
		}
		assetMap[key] = assetID
		if _, exists := publishedAssets[assetID]; !exists {
			*publishedOrder = append(*publishedOrder, assetID)
		}
		publishedAssets[assetID] = rowString(published, "storage_key")
	}
	snapshot := r.snapshot()
	err := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		for _, occurrence := range recordList(request.Records, "occurrences") {
			oldOccurrenceID := rowString(occurrence, "occurrence_id")
			assetID := assetMap[rowString(occurrence, "asset_id")]
			if oldOccurrenceID == "" || assetID == "" {
				return validationErrorf("记忆包图片出现记录引用了未知资产")
			}
			created, err := r.metadataStore.UpsertImageOccurrence(storage.ImageOccurrenceInput{
				AssetID:        assetID,
				ExternalRef:    BuildPackageExternalRef(request.InstallationID, oldOccurrenceID),
				SourceKind:     "imported_knowledge",
				ScopeType:      request.ScopeType,
				ChatID:         request.ChatID,
				ComponentPath:  rowString(occurrence, "component_path"),
				OccurredAt:     rowFloatPtr(occurrence, "occurred_at"),
				InstallationID: request.InstallationID,
				Conn:           tx.Conn(),
			})
			if err != nil {
				return err
			}
			occurrenceMap[oldOccurrenceID] = rowString(created, "occurrence_id")
		}
		for _, observation := range recordList(request.Records, "observations") {
			occurrenceID, ok := occurrenceMap[rowString(observation, "occurrence_id")]
			if !ok {
				return validationErrorf("记忆包图片认知引用了未知出现记录")
			}
			evidence, err := decodeEvidenceObject(rowString(observation, "evidence_json"), "记忆包图片认知证据不是有效对象")
			if err != nil {
				return err
			}
			evidence["imported"] = true
			if _, err := r.metadataStore.AddImageObservation(storage.ImageObservationInput{
				OccurrenceID:  occurrenceID,
				Text:          stringOr(observation["text"], ""),
				SourceKind:    stringOr(observation["source_kind"], "imported_knowledge"),
				ConfirmStatus: stringOr(observation["confirm_status"], "imported"),
				Evidence:      evidence,
				Conn:          tx.Conn(),
			}); err != nil {
				return err
			}
		}
		for _, link := range recordList(request.Records, "links") {
			occurrenceID, ok := occurrenceMap[rowString(link, "occurrence_id")]
			targetType := rowString(link, "target_type")
			originalTarget := rowString(link, "target_id")
			targetID := originalTarget
			if mapping, exists := request.TargetIDMap[targetType]; exists {
				if mapped, found := mapping[originalTarget]; found {
					targetID = mapped
				}
			}
			if !ok {
				return validationErrorf("记忆包图片关联无法映射到有效目标")
			}
			content, err := r.targetContent(targetType, targetID)
			if err != nil {
				return err
			}
			if content == nil {
				return validationErrorf("记忆包图片关联无法映射到有效目标")
			}
			evidence, err := decodeEvidenceObject(rowString(link, "evidence_json"), "记忆包图片关联证据不是有效对象")
			if err != nil {
				return err
			}
			evidence["imported"] = true
			evidence["source_link_id"] = rowString(link, "link_id")
			if _, err := r.metadataStore.UpsertImageMemoryLink(storage.ImageMemoryLinkInput{
				OccurrenceID: occurrenceID,
				TargetType:   targetType,
				TargetID:     targetID,
				LinkKind:     stringOr(link["link_kind"], "imported"),
				Evidence:     evidence,
				Conn:         tx.Conn(),
			}); err != nil {
				return err
			}
		}
		if snapshot.status == "ready" {
			fpHash := FingerprintHash(snapshot.fingerprint)
			for _, key := range *assetMapOrder {
				assetID := assetMap[key]
				existing, err := r.metadataStore.GetImageEmbedding(assetID, fpHash)
				if err != nil {
					return err
				}
				if existing == nil {
					if _, err := r.metadataStore.EnqueueImageEmbeddingJob(assetID, fpHash, snapshot.generation, tx.Conn()); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	return err
}

// recordList 读取导入记录中的列表字段
func recordList(records map[string]any, key string) []map[string]any {
	if records == nil {
		return nil
	}
	value, ok := records[key]
	if !ok || value == nil {
		return nil
	}
	switch items := value.(type) {
	case []map[string]any:
		return items
	case []any:
		converted := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if row, ok := item.(map[string]any); ok {
				converted = append(converted, row)
			}
		}
		return converted
	}
	return nil
}

// decodeEvidenceObject 对齐 json.loads(str(... or "{}")) 的对象校验
func decodeEvidenceObject(raw string, message string) (map[string]any, error) {
	text := raw
	if text == "" {
		text = "{}"
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, validationErrorf("%s", message)
	}
	object, ok := parsed.(map[string]any)
	if !ok || object == nil {
		return nil, validationErrorf("%s", message)
	}
	return object, nil
}
