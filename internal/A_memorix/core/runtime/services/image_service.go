package services

// 图片记忆服务，对齐 Python core/runtime/services/image_service.py

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/google/uuid"

	imagememory "maibot-go/internal/A_memorix/core/image"
	"maibot-go/internal/A_memorix/core/storage"
)

// ImageServiceDeps 图片记忆服务依赖的运行时与配置端口
type ImageServiceDeps struct {
	// Runtime 返回图片记忆运行时，nil 表示未启用
	Runtime func() *imagememory.ImageMemoryRuntime
	// Config 读取图片记忆分段配置，对齐 runtime._cfg(key, fallback)
	Config func(key string, fallback any) any
}

// ImageService 对齐 MemoryImageService
type ImageService struct {
	*Base
	Deps ImageServiceDeps
}

// NewImageService 构造图片记忆服务
// Config 缺省时按 image_memory.<key> 读取全局配置
func NewImageService(base *Base, deps ImageServiceDeps) *ImageService {
	if deps.Config == nil {
		deps.Config = func(key string, fallback any) any {
			return base.Cfg("image_memory."+key, fallback)
		}
	}
	return &ImageService{Base: base, Deps: deps}
}

// imageRuntime 对齐 _runtime，未启用时返回错误
func (s *ImageService) imageRuntime() (*imagememory.ImageMemoryRuntime, error) {
	if s.Deps.Runtime == nil {
		return nil, fmt.Errorf("图片记忆未启用")
	}
	runtime := s.Deps.Runtime()
	if runtime == nil {
		return nil, fmt.Errorf("图片记忆未启用")
	}
	return runtime, nil
}

// imageCfg 对齐 runtime._cfg
func (s *ImageService) imageCfg(key string, fallback any) any {
	if s.Deps.Config == nil {
		return fallback
	}
	return s.Deps.Config(key, fallback)
}

// imageDecode 对齐 _decode_image，支持 image_bytes 与严格 Base64
func imageDecode(payload map[string]any) ([]byte, error) {
	if raw, ok := payload["image_bytes"]; ok && raw != nil {
		switch typed := raw.(type) {
		case []byte:
			return typed, nil
		case string:
			return nil, fmt.Errorf("image_bytes 必须是字节数据")
		default:
			return nil, fmt.Errorf("image_bytes 必须是字节数据")
		}
	}
	encoded := strings.TrimSpace(asString(payload["image_base64"]))
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("image_base64 不是合法的 Base64")
	}
	return decoded, nil
}

// imageChatIDs 对齐 chat_ids 的字符串列表校验，返回 nil 表示不过滤
func imageChatIDs(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items := asList(value)
	if items == nil {
		return nil, fmt.Errorf("chat_ids 必须是字符串列表")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := asString(item)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, text)
	}
	return out, nil
}

// Execute 对齐 execute，按操作分发图片记忆接口
func (s *ImageService) Execute(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	runtime, err := s.imageRuntime()
	if err != nil {
		return nil, err
	}
	operation := strings.ToLower(strings.TrimSpace(action))
	switch operation {
	case "status":
		status, err := runtime.Status()
		if err != nil {
			return nil, err
		}
		result := map[string]any{"success": true}
		for key, value := range status {
			result[key] = value
		}
		return result, nil
	case "jobs":
		limit := clampIntRange(intOr(payload["limit"], 50, 1), 1, 200)
		offset := intOr(payload["offset"], 0, 0)
		list, err := runtime.MetadataStore().ListImageJobs(limit, offset, asString(payload["status"]))
		if err != nil {
			return nil, err
		}
		items := list.Items
		if items == nil {
			items = []map[string]any{}
		}
		return map[string]any{"success": list.Success, "items": items, "total": list.Total}, nil
	case "retry_failed_jobs":
		counts, err := runtime.MetadataStore().RetryFailedImageJobs()
		if err != nil {
			return nil, err
		}
		total := 0
		for _, count := range counts {
			total += count
		}
		return map[string]any{"success": true, "counts": counts, "count": total}, nil
	case "ingest":
		imageBytes, err := imageDecode(payload)
		if err != nil {
			return nil, err
		}
		if imageBytes == nil {
			return nil, fmt.Errorf("图片写入缺少 image_bytes 或 image_base64")
		}
		return runtime.Ingest(ctx, imagememory.IngestRequest{
			ImageBytes:     imageBytes,
			SourceKind:     imageDefaultText(payload, "source_kind", "chat"),
			ExternalRef:    imageDefaultText(payload, "external_ref", ""),
			ScopeType:      imageDefaultText(payload, "scope_type", "chat"),
			ChatID:         imageDefaultText(payload, "chat_id", ""),
			MessageID:      imageDefaultText(payload, "message_id", ""),
			ComponentPath:  imageDefaultText(payload, "component_path", ""),
			OccurredAt:     optionalFloat(payload["occurred_at"]),
			UserStatement:  imageDefaultText(payload, "user_statement", ""),
			InstallationID: imageDefaultText(payload, "installation_id", ""),
		})
	case "search":
		imageBytes, err := imageDecode(payload)
		if err != nil {
			return nil, err
		}
		currentOccurrenceID := imageDefaultText(payload, "current_occurrence_id", "")
		currentExternalRef := imageDefaultText(payload, "current_external_ref", "")
		if currentOccurrenceID == "" && currentExternalRef != "" {
			occurrence, err := runtime.MetadataStore().GetImageOccurrenceByRef(currentExternalRef)
			if err != nil {
				return nil, err
			}
			if occurrence != nil {
				currentOccurrenceID = asString(occurrence["occurrence_id"])
			}
		}
		chatIDs, err := imageChatIDs(payload["chat_ids"])
		if err != nil {
			return nil, err
		}
		candidateLimit := payload["candidate_limit"]
		if !pyTruthy(candidateLimit) {
			candidateLimit = s.imageCfg("candidate_limit", float64(8))
		}
		thresholdRaw := payload["similarity_threshold"]
		if thresholdRaw == nil {
			thresholdRaw = s.imageCfg("similarity_threshold", 0.72)
		}
		threshold, ok := asFloat(thresholdRaw)
		if !ok {
			return nil, fmt.Errorf("similarity_threshold 必须是数字")
		}
		return runtime.Search(ctx, imagememory.SearchRequest{
			ImageBytes:          imageBytes,
			ContentHash:         imageDefaultText(payload, "content_hash", ""),
			CurrentOccurrenceID: currentOccurrenceID,
			ChatIDs:             chatIDs,
			CandidateLimit:      intOr(candidateLimit, 8, 0),
			SimilarityThreshold: threshold,
			SessionID:           imageDefaultText(payload, "session_id", ""),
		})
	case "get":
		chatIDs, err := imageChatIDs(payload["chat_ids"])
		if err != nil {
			return nil, err
		}
		return runtime.Get(imageDefaultText(payload, "asset_id", ""), chatIDs)
	case "list":
		limit := clampIntRange(pyIntOr(payload["limit"], 50, 1), 1, 500)
		offset := intOr(payload["offset"], 0, 0)
		return runtime.ListAssets(limit, offset, strings.TrimSpace(asString(payload["chat_id"])))
	case "list_chats":
		items, err := runtime.ListChatStats()
		if err != nil {
			return nil, err
		}
		if items == nil {
			items = []map[string]any{}
		}
		return map[string]any{"success": true, "items": items}, nil
	case "delete_occurrence":
		return runtime.DeleteOccurrence(imageDefaultText(payload, "occurrence_id", ""))
	case "observe":
		return runtime.AddObservation(storage.ImageObservationInput{
			OccurrenceID:  imageDefaultText(payload, "occurrence_id", ""),
			Text:          imageDefaultText(payload, "text", ""),
			SourceKind:    imageDefaultText(payload, "source_kind", "manual"),
			ConfirmStatus: imageDefaultText(payload, "confirm_status", "confirmed"),
			Evidence:      imageEvidence(payload),
			SupersedesID:  imageDefaultText(payload, "supersedes_id", ""),
		})
	case "link":
		return runtime.Link(imagememory.LinkRequest{
			OccurrenceID: imageDefaultText(payload, "occurrence_id", ""),
			TargetType:   imageDefaultText(payload, "target_type", ""),
			TargetID:     imageDefaultText(payload, "target_id", ""),
			LinkKind:     imageDefaultText(payload, "link_kind", "evidence"),
			Evidence:     imageEvidence(payload),
		})
	case "unlink":
		return runtime.Unlink(imageDefaultText(payload, "link_id", ""))
	case "describe":
		targets, err := runtime.ApplyModelDescription(
			imageDefaultText(payload, "content_hash", ""),
			imageDefaultText(payload, "text", ""),
		)
		if err != nil {
			return nil, err
		}
		if targets == nil {
			targets = []map[string]any{}
		}
		return map[string]any{"success": true, "updated_occurrences": len(targets), "compensation_targets": targets}, nil
	case "process_jobs":
		return runtime.ProcessJobsOnce(ctx)
	case "claim_description_compensations":
		leaseToken := strings.ReplaceAll(uuid.NewString(), "-", "")
		leaseSeconds, ok := asFloat(s.imageCfg("job_lease_seconds", 120.0))
		if !ok {
			return nil, fmt.Errorf("job_lease_seconds 必须是数字")
		}
		maxRetries := intOr(s.imageCfg("job_max_retries", float64(5)), 5, 0)
		limit := clampIntRange(pyIntOr(payload["limit"], pyIntOr(s.imageCfg("job_batch_size", float64(4)), 4, 1), 1), 1, 100)
		items, err := runtime.MetadataStore().ClaimImageDescriptionCompensations(leaseToken, leaseSeconds, 1+maxRetries, limit)
		if err != nil {
			return nil, err
		}
		if items == nil {
			items = []map[string]any{}
		}
		return map[string]any{"success": true, "lease_token": leaseToken, "items": items}, nil
	case "complete_description_compensation":
		updated, err := runtime.MetadataStore().CompleteImageDescriptionCompensation(
			imageDefaultText(payload, "occurrence_id", ""),
			imageDefaultText(payload, "description_hash", ""),
			imageDefaultText(payload, "lease_token", ""),
			pyTruthy(payload["success"]),
			imageDefaultText(payload, "error", ""),
		)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": updated}, nil
	}
	return nil, fmt.Errorf("不支持的图片记忆操作: %s", operation)
}

// imageDefaultText 对齐 str(kwargs.get(key) or default)
func imageDefaultText(payload map[string]any, key string, fallback string) string {
	value := payload[key]
	if !pyTruthy(value) {
		return fallback
	}
	return asString(value)
}

// imageEvidence 对齐 evidence 仅在字典时透传
func imageEvidence(payload map[string]any) map[string]any {
	if evidence := asMap(payload["evidence"]); evidence != nil {
		return evidence
	}
	return map[string]any{}
}
