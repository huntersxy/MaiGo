package llm

// 任务级 hard_timeout 的可诊断快照来源
// 对齐 MaiBot utils_model.py:1322-1333：hard_timeout 快照仍保存原始请求体，而不是只有超时字段；
// request_kind 与 request_snapshot.py:396-447 的 response/embedding/image_embedding/audio_transcription 一致

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	requestKindResponse           = "response"
	requestKindEmbedding          = "embedding"
	requestKindImageEmbedding     = "image_embedding"
	requestKindAudioTranscription = "audio_transcription"
)

// attemptSnapshotSource 由各请求路径在进入重试前构造，供 hard_timeout 快照复用
type attemptSnapshotSource struct {
	Config          ModelConfig
	RequestKind     string
	ContextItems    []ContextItem
	Input           []*schema.AgenticMessage
	Options         *model.Options
	InternalRequest map[string]any
	ProviderRequest map[string]any
}

// responseAttemptSnapshot 对齐 serialize_response_request_snapshot 的可诊断字段
func responseAttemptSnapshot(cfg ModelConfig, input []*schema.AgenticMessage, opts []model.Option) *attemptSnapshotSource {
	options := model.GetCommonOptions(&model.Options{}, opts...)
	// eino 客户端会在真正发包前把五档/四档解析进 options；快照先按同样规则解析一份副本
	resolved := *options
	resolveEffectiveParams(cfg, &resolved)
	items, err := ContextItemsFromMessages(input)
	if err != nil {
		items = nil
	}
	internal := map[string]any{
		"item_schema_version": ContextItemSchemaVersion,
		"request_kind":        requestKindResponse,
		"extra_params":        cloneAnyMap(cfg.ExtraParams),
		"model_info":          modelInfoSnapshotForConfig(cfg),
		"response_format":     attemptResponseFormatSnapshotValue(cfg.ResponseFormat),
	}
	if resolved.MaxTokens != nil {
		internal["max_tokens"] = *resolved.MaxTokens
	} else {
		internal["max_tokens"] = nil
	}
	if resolved.Temperature != nil {
		internal["temperature"] = *resolved.Temperature
	} else {
		internal["temperature"] = nil
	}
	if choice := chatToolChoiceValue(&resolved); choice != "" {
		internal["tool_choice"] = choice
	} else {
		internal["tool_choice"] = nil
	}
	return &attemptSnapshotSource{
		Config:          cfg,
		RequestKind:     requestKindResponse,
		ContextItems:    items,
		Input:           input,
		Options:         &resolved,
		InternalRequest: internal,
	}
}

// embeddingAttemptSnapshot 对齐 serialize_embedding_request_snapshot
func embeddingAttemptSnapshot(cfg ModelConfig, text string) *attemptSnapshotSource {
	return &attemptSnapshotSource{
		Config:      cfg,
		RequestKind: requestKindEmbedding,
		InternalRequest: map[string]any{
			"embedding_input": text,
			"extra_params":    cloneAnyMap(cfg.ExtraParams),
			"model_info":      modelInfoSnapshotForConfig(cfg),
			"request_kind":    requestKindEmbedding,
		},
	}
}

// imageEmbeddingAttemptSnapshot 对齐 serialize_image_embedding_request_snapshot
func imageEmbeddingAttemptSnapshot(cfg ModelConfig, input ImageEmbeddingInput) *attemptSnapshotSource {
	digest := sha256.Sum256(input.Bytes)
	return &attemptSnapshotSource{
		Config:      cfg,
		RequestKind: requestKindImageEmbedding,
		InternalRequest: map[string]any{
			"byte_size":          len(input.Bytes),
			"content_sha256":     hex.EncodeToString(digest[:]),
			"extra_params":       cloneAnyMap(cfg.ExtraParams),
			"mime_type":          strings.TrimSpace(input.MIMEType),
			"model_info":         modelInfoSnapshotForConfig(cfg),
			"preprocess_version": input.PreprocessVersion,
			"request_kind":       requestKindImageEmbedding,
		},
	}
}

// audioAttemptSnapshot 对齐 serialize_audio_request_snapshot
func audioAttemptSnapshot(cfg ModelConfig, req AudioTranscriptionRequest) *attemptSnapshotSource {
	extra := req.ExtraParams
	if extra == nil {
		extra = cfg.ExtraParams
	}
	return &attemptSnapshotSource{
		Config:      cfg,
		RequestKind: requestKindAudioTranscription,
		InternalRequest: map[string]any{
			"audio_base64": req.AudioBase64,
			"extra_params": cloneAnyMap(extra),
			"max_tokens":   req.MaxTokens,
			"model_info":   modelInfoSnapshotForConfig(cfg),
			"request_kind": requestKindAudioTranscription,
		},
	}
}

// attemptResponseFormatSnapshotValue 把模型配置里的 RespFormat 归一成快照形状
func attemptResponseFormatSnapshotValue(raw any) any {
	if raw == nil {
		return nil
	}
	spec, err := parseResponseFormat(raw)
	if err != nil || spec.Kind == "" {
		return nil
	}
	return responseFormatSnapshot(spec)
}
