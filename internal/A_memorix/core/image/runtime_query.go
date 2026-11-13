package imagememory

import (
	"context"
	"sort"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
)

// SearchRequest 对齐 Python search 的关键字参数
type SearchRequest struct {
	ImageBytes          []byte
	ContentHash         string
	CurrentOccurrenceID string
	ChatIDs             []string
	CandidateLimit      int
	SimilarityThreshold float64
	SessionID           string
}

// Search 对齐 Python search，按可见范围检索相似图片
func (r *ImageMemoryRuntime) Search(ctx context.Context, request SearchRequest) (map[string]any, error) {
	started := time.Now()
	timings := map[string]float64{}
	visible, err := r.metadataStore.ListVisibleImageOccurrences(request.ChatIDs, request.CurrentOccurrenceID)
	if err != nil {
		return nil, err
	}
	occurrencesByAsset := map[string][]map[string]any{}
	assetOrder := make([]string, 0, len(visible))
	for _, occurrence := range visible {
		assetID := rowString(occurrence, "asset_id")
		if _, ok := occurrencesByAsset[assetID]; !ok {
			assetOrder = append(assetOrder, assetID)
		}
		occurrencesByAsset[assetID] = append(occurrencesByAsset[assetID], occurrence)
	}
	if len(occurrencesByAsset) == 0 {
		status, err := r.EnsureEmbeddingSpace(ctx)
		if err != nil {
			return nil, err
		}
		elapsed := round2(float64(time.Since(started).Nanoseconds()) / 1e6)
		statusText := rowString(status, "status")
		errorText := ""
		if statusText != "ready" {
			errorText = stringOr(status["message"], "")
		}
		return map[string]any{
			"status":               statusText,
			"query_asset_id":       "",
			"hits":                 []map[string]any{},
			"related_memory_count": 0,
			"success":              statusText == "ready",
			"error":                errorText,
			"timings_ms": map[string]float64{
				"scope":         elapsed,
				"embedding":     0,
				"vector_search": 0,
				"expansion":     0,
				"total":         elapsed,
			},
		}, nil
	}
	var queryAsset map[string]any
	if request.ContentHash != "" {
		queryAsset, err = r.metadataStore.GetImageAssetByHash(request.ContentHash)
		if err != nil {
			return nil, err
		}
	}
	if len(request.ImageBytes) > 0 {
		queryAsset, err = r.metadataStore.GetImageAssetByHash(ContentHash(request.ImageBytes))
		if err != nil {
			return nil, err
		}
	}
	queryAssetID := rowString(queryAsset, "asset_id")
	hits := map[string]map[string]any{}
	hitOrder := make([]string, 0, len(assetOrder)+1)
	if queryAssetID != "" {
		if _, ok := occurrencesByAsset[queryAssetID]; ok {
			hits[queryAssetID] = map[string]any{"asset_id": queryAssetID, "match_kind": "exact_hash", "similarity": 1.0}
			hitOrder = append(hitOrder, queryAssetID)
		}
	}
	timings["scope"] = round2(float64(time.Since(started).Nanoseconds()) / 1e6)
	stageStarted := time.Now()
	status, err := r.EnsureEmbeddingSpace(ctx)
	if err != nil {
		return nil, err
	}
	store := r.VectorStore()
	statusText := rowString(status, "status")
	var queryVector []float32
	hasQueryVector := false
	if statusText == "ready" && store != nil {
		if len(request.ImageBytes) > 0 {
			inspected, inspectErr := r.assetStore.Inspect(request.ImageBytes)
			if inspectErr != nil {
				return nil, inspectErr
			}
			encoded, embedErr := r.embedder.Embed(ctx, request.ImageBytes, inspected.MIMEType, request.SessionID)
			if embedErr != nil {
				return nil, embedErr
			}
			queryVector = embeddingVector(encoded)
			hasQueryVector = queryVector != nil
		} else if queryAssetID != "" {
			vectors, vectorErr := store.GetVectors([]string{queryAssetID})
			if vectorErr != nil {
				return nil, vectorErr
			}
			queryVector = vectors[queryAssetID]
			if queryVector != nil {
				hasQueryVector = true
			}
			if !hasQueryVector && queryAsset != nil {
				payload, readErr := r.assetStore.ReadBytes(rowString(queryAsset, "storage_key"))
				if readErr != nil {
					return nil, readErr
				}
				encoded, embedErr := r.embedder.Embed(ctx, payload, rowString(queryAsset, "mime_type"), request.SessionID)
				if embedErr != nil {
					return nil, embedErr
				}
				queryVector = embeddingVector(encoded)
				hasQueryVector = queryVector != nil
			}
		}
		if hasQueryVector {
			timings["embedding"] = round2(float64(time.Since(stageStarted).Nanoseconds()) / 1e6)
			stageStarted = time.Now()
			allowedIDs := append([]string{}, assetOrder...)
			ids, scores, searchErr := store.Search(queryVector, maxInt(1, request.CandidateLimit), true, allowedIDs)
			if searchErr != nil {
				return nil, searchErr
			}
			for index, assetID := range ids {
				if index >= len(scores) {
					break
				}
				similarity := scores[index]
				if similarity < request.SimilarityThreshold {
					continue
				}
				if assetID == queryAssetID && hits[assetID] != nil {
					continue
				}
				if _, exists := hits[assetID]; !exists {
					hitOrder = append(hitOrder, assetID)
				}
				hits[assetID] = map[string]any{
					"asset_id":   assetID,
					"match_kind": "visual_similar",
					"similarity": similarity,
				}
			}
		}
	}
	if _, ok := timings["embedding"]; !ok {
		timings["embedding"] = round2(float64(time.Since(stageStarted).Nanoseconds()) / 1e6)
	}
	if hasQueryVector {
		timings["vector_search"] = round2(float64(time.Since(stageStarted).Nanoseconds()) / 1e6)
	} else {
		timings["vector_search"] = 0
	}
	stageStarted = time.Now()
	ordered := make([]map[string]any, 0, len(hitOrder))
	for _, assetID := range hitOrder {
		ordered = append(ordered, hits[assetID])
	}
	sort.SliceStable(ordered, func(left int, right int) bool {
		return ordered[left]["similarity"].(float64) > ordered[right]["similarity"].(float64)
	})
	limit := maxInt(1, request.CandidateLimit)
	if len(ordered) < limit {
		limit = len(ordered)
	}
	expanded := make([]map[string]any, 0, limit)
	relatedCount := 0
	for _, hit := range ordered[:limit] {
		assetID := rowString(hit, "asset_id")
		asset, assetErr := r.metadataStore.GetImageAsset(assetID)
		if assetErr != nil {
			return nil, assetErr
		}
		if asset == nil {
			asset = map[string]any{}
		}
		occurrences := occurrencesByAsset[assetID]
		occurrenceIDs := make([]string, 0, len(occurrences))
		for _, occurrence := range occurrences {
			occurrenceIDs = append(occurrenceIDs, rowString(occurrence, "occurrence_id"))
		}
		links, linkErr := r.metadataStore.ListImageMemoryLinks(occurrenceIDs)
		if linkErr != nil {
			return nil, linkErr
		}
		related := make([]map[string]any, 0, len(links))
		for _, link := range links {
			content, contentErr := r.targetContent(rowString(link, "target_type"), rowString(link, "target_id"))
			if contentErr != nil {
				return nil, contentErr
			}
			if content != nil {
				item := map[string]any{}
				mergeMaps(item, content)
				item["link_kind"] = rowString(link, "link_kind")
				related = append(related, item)
			}
		}
		observations, observationErr := r.metadataStore.ListImageObservations(occurrenceIDs)
		if observationErr != nil {
			return nil, observationErr
		}
		item := map[string]any{}
		mergeMaps(item, hit)
		item["content_hash"] = rowString(asset, "content_hash")
		item["width"] = int(rowInt64(asset, "width"))
		item["height"] = int(rowInt64(asset, "height"))
		item["occurrences"] = occurrences
		item["observations"] = observations
		item["related_memories"] = related
		expanded = append(expanded, item)
		relatedCount += len(related)
	}
	timings["expansion"] = round2(float64(time.Since(stageStarted).Nanoseconds()) / 1e6)
	timings["total"] = round2(float64(time.Since(started).Nanoseconds()) / 1e6)
	errorText := ""
	if statusText != "ready" {
		errorText = stringOr(status["message"], "")
	}
	return map[string]any{
		"status":               statusText,
		"success":              statusText == "ready" || len(expanded) > 0,
		"error":                errorText,
		"query_asset_id":       queryAssetID,
		"hits":                 expanded,
		"timings_ms":           timings,
		"related_memory_count": relatedCount,
	}, nil
}

// Get 对齐 Python get，返回资产、出现记录、认知与关联
func (r *ImageMemoryRuntime) Get(assetID string, chatIDs []string) (map[string]any, error) {
	asset, err := r.metadataStore.GetImageAsset(assetID)
	if err != nil {
		return nil, err
	}
	if asset == nil || rowString(asset, "status") != "active" {
		return map[string]any{"success": false, "error": "图片不存在"}, nil
	}
	visible, err := r.metadataStore.ListVisibleImageOccurrences(chatIDs, "")
	if err != nil {
		return nil, err
	}
	occurrences := make([]map[string]any, 0, len(visible))
	for _, occurrence := range visible {
		if rowString(occurrence, "asset_id") == assetID {
			occurrences = append(occurrences, occurrence)
		}
	}
	if chatIDs != nil && len(occurrences) == 0 {
		return map[string]any{"success": false, "error": "当前范围不可见该图片"}, nil
	}
	occurrenceIDs := make([]string, 0, len(occurrences))
	for _, occurrence := range occurrences {
		occurrenceIDs = append(occurrenceIDs, rowString(occurrence, "occurrence_id"))
	}
	links, err := r.metadataStore.ListImageMemoryLinks(occurrenceIDs)
	if err != nil {
		return nil, err
	}
	observations, err := r.metadataStore.ListImageObservations(occurrenceIDs)
	if err != nil {
		return nil, err
	}
	enrichedLinks := make([]map[string]any, 0, len(links))
	for _, link := range links {
		content, contentErr := r.targetContent(rowString(link, "target_type"), rowString(link, "target_id"))
		if contentErr != nil {
			return nil, contentErr
		}
		item := map[string]any{}
		mergeMaps(item, link)
		item["memory"] = content
		enrichedLinks = append(enrichedLinks, item)
	}
	return map[string]any{
		"success":      true,
		"asset":        asset,
		"occurrences":  occurrences,
		"observations": observations,
		"links":        enrichedLinks,
	}, nil
}

// LinkRequest 对齐 Python link 的关键字参数
type LinkRequest struct {
	OccurrenceID string
	TargetType   string
	TargetID     string
	LinkKind     string
	Evidence     map[string]any
}

// Link 对齐 Python link，校验目标后写入图片记忆关联
func (r *ImageMemoryRuntime) Link(request LinkRequest) (map[string]any, error) {
	occurrence, err := r.metadataStore.GetImageOccurrence(request.OccurrenceID)
	if err != nil {
		return nil, err
	}
	if occurrence == nil {
		return nil, validationErrorf("图片出现记录不存在")
	}
	content, err := r.targetContent(request.TargetType, request.TargetID)
	if err != nil {
		return nil, err
	}
	if content == nil {
		return nil, validationErrorf("关联目标不存在或已失效")
	}
	var link map[string]any
	err = r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		created, upsertErr := r.metadataStore.UpsertImageMemoryLink(storage.ImageMemoryLinkInput{
			OccurrenceID: request.OccurrenceID,
			TargetType:   request.TargetType,
			TargetID:     request.TargetID,
			LinkKind:     request.LinkKind,
			Evidence:     request.Evidence,
			Conn:         tx.Conn(),
		})
		link = created
		return upsertErr
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "link": link}, nil
}

// AddObservation 对齐 Python add_observation，在事务内写入图片认知
func (r *ImageMemoryRuntime) AddObservation(input storage.ImageObservationInput) (map[string]any, error) {
	var observation map[string]any
	err := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		input.Conn = tx.Conn()
		created, addErr := r.metadataStore.AddImageObservation(input)
		observation = created
		return addErr
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "observation": observation}, nil
}

// Unlink 对齐 Python unlink，失效单条图片记忆关联
func (r *ImageMemoryRuntime) Unlink(linkID string) (map[string]any, error) {
	invalidated := 0
	err := r.metadataStore.Transaction(true, func(tx *storage.Tx) error {
		count, invalidateErr := r.metadataStore.InvalidateImageMemoryLink(linkID, tx.Conn())
		invalidated = count
		return invalidateErr
	})
	if err != nil {
		return nil, err
	}
	errorText := ""
	if invalidated != 1 {
		errorText = "图片记忆关联不存在或已失效"
	}
	return map[string]any{
		"success":     invalidated == 1,
		"invalidated": invalidated,
		"error":       errorText,
	}, nil
}
