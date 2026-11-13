package model_client

// Gemini 语音转写
// 对齐 MaiBot GeminiClient._execute_audio_transcription_request：
//   - 请求体复用 generateContent，contents = [转写提示文本, inline 音频]
//   - 生成配置复用 _build_generation_config（含 max_tokens、extra_params 透传、thinking/safety 默认值）
//   - 响应复用非流式解析器，取可见正文与 reasoning

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/cloudwego/eino/schema"
)

const (
	// defaultTranscriptionPrompt 对齐 MaiBot transcription_prompt 默认值
	defaultTranscriptionPrompt = "Generate a transcript of the speech. The language of the transcript should match the speech."
	// defaultAudioMimeType 对齐 MaiBot audio_mime_type 默认值
	defaultAudioMimeType = "audio/wav"
)

// AudioTranscriptionRequest 音频转写请求（对齐 MaiBot AudioTranscriptionRequest）
type AudioTranscriptionRequest struct {
	// AudioBase64 音频数据的 base64 编码
	AudioBase64 string
	// MIMEType 音频 MIME 类型；为空时由客户端按 audio_mime_type 或 audio/wav 兜底
	MIMEType    string
	MaxTokens   int
	ExtraParams map[string]any
}

// AudioTranscriptionResult 转写结果
type AudioTranscriptionResult struct {
	Text      string
	Reasoning string
	Usage     *schema.TokenUsage
	Raw       []byte
	// 以下字段由任务级 failover 包装器回填，供 LLMService 等上层报告实际命中的模型
	ModelName       string
	ModelIdentifier string
	APIProvider     string
}

// AudioTranscriber 支持语音转写的模型客户端
// MaiBot 内置 Gemini / OpenAI 兼容 / 插件 Provider 均实现
type AudioTranscriber interface {
	Transcribe(ctx context.Context, req AudioTranscriptionRequest) (AudioTranscriptionResult, error)
}

// NewAudioTranscriber 按 client_type 创建转写客户端
func NewAudioTranscriber(cfg ModelConfig) (AudioTranscriber, error) {
	client, err := newAudioTranscriber(cfg)
	if err != nil {
		return nil, err
	}
	return newUsageRecordingAudioTranscriber(client, cfg), nil
}

func newAudioTranscriber(cfg ModelConfig) (AudioTranscriber, error) {
	if isGeminiClientType(cfg.ClientType) {
		client, err := newGeminiClient(cfg)
		if err != nil {
			return nil, err
		}
		return &geminiAudioTranscriber{client: client}, nil
	}
	// openai_responses 与 openai 共用 OpenAI 兼容 /audio/transcriptions 实现
	// 对齐 MaiBot openai_responses_client.py:667：OpenAIResponsesClient 直接继承 OpenaiClient 的转写方法，
	// 因此 Responses 档案也必须能走同一条 base_url + "/audio/transcriptions" 路径
	if isChatCompletionsClientType(cfg.ClientType) || isOpenAIResponsesClientType(cfg.ClientType) {
		client, err := newChatCompletionsClient(cfg)
		if err != nil {
			return nil, err
		}
		return &openAIAudioTranscriber{client: client}, nil
	}
	if !isOpenAICompatClientType(cfg.ClientType) {
		// 非内置 client_type 归插件 Provider
		return newPluginAudioTranscriber(cfg)
	}
	return nil, fmt.Errorf("client_type %q 尚未实现语音转写", cfg.ClientType)
}

type geminiAudioTranscriber struct {
	client *geminiClient
}

func (t *geminiAudioTranscriber) Transcribe(ctx context.Context, req AudioTranscriptionRequest) (AudioTranscriptionResult, error) {
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	audioBase64 := strings.TrimSpace(req.AudioBase64)
	if audioBase64 == "" {
		return AudioTranscriptionResult{}, &ParseError{Message: "音频转写请求缺少音频数据"}
	}
	extra := t.client.extra
	if req.ExtraParams != nil {
		extra = mergeAnyMaps(extra, req.ExtraParams)
	}
	prompt := defaultTranscriptionPrompt
	if value, ok := extra["transcription_prompt"]; ok && strings.TrimSpace(fmt.Sprint(value)) != "" {
		prompt = fmt.Sprint(value)
	}
	// MIME 只取 extra_params.audio_mime_type，忽略调用方传入的 req.MIMEType
	// 对齐 MaiBot gemini_client.py:1295：AudioTranscriptionRequest 没有 mime 字段，
	// 固定使用 str(extra_params.get("audio_mime_type", "audio/wav"))
	audioMimeType := defaultAudioMimeType
	if value, ok := extra["audio_mime_type"]; ok && strings.TrimSpace(fmt.Sprint(value)) != "" {
		audioMimeType = strings.TrimSpace(fmt.Sprint(value))
	}
	// 先解码校验再重新编码，非法 base64 直接按 NetworkConnectionError 失败且不发请求
	// 对齐 MaiBot gemini_client.py:1298 base64.b64decode -> Part.from_bytes
	normalizedAudio, decodeErr := normalizeGeminiAudioBase64(audioBase64)
	if decodeErr != nil {
		networkErr := &NetworkError{Message: fmt.Sprintf("音频 base64 解码失败: %v", decodeErr), Err: decodeErr}
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, prompt, audioMimeType, len(audioBase64), nil, networkErr)
	}
	audioBase64 = normalizedAudio

	modelName := strings.TrimSuffix(t.client.model, "-search")
	var maxTokens *int
	if req.MaxTokens > 0 {
		value := req.MaxTokens
		maxTokens = &value
	}
	config := t.client.buildGenerationConfig(maxTokens, nil, nil, nil, nil, false, extra)
	body := map[string]any{
		"contents": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"text": prompt},
				{"inlineData": map[string]any{"mimeType": audioMimeType, "data": audioBase64}},
			},
		}},
		"safetySettings": geminiSafetySettings,
	}
	if len(config) > 0 {
		body["generationConfig"] = config
	}

	reqCtx, cancel := t.client.requestContext(ctx)
	defer cancel()
	raw, err := t.client.post(ctx, reqCtx, modelName, body, false)
	if err != nil {
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, prompt, audioMimeType, len(audioBase64), body, err)
	}
	parsed, err := parseGeminiResponse(raw)
	if err != nil {
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, prompt, audioMimeType, len(audioBase64), body, err)
	}
	message, err := parsed.toAgenticMessage()
	if err != nil {
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, prompt, audioMimeType, len(audioBase64), body, err)
	}
	result := AudioTranscriptionResult{Raw: raw}
	if message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
		result.Usage = message.ResponseMeta.TokenUsage
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				result.Text += block.AssistantGenText.Text
			}
		case schema.ContentBlockTypeReasoning:
			if block.Reasoning != nil {
				result.Reasoning += block.Reasoning.Text
			}
		}
	}
	result.Text = strings.TrimSpace(result.Text)
	result.Reasoning = strings.TrimSpace(result.Reasoning)
	return result, nil
}

// normalizeGeminiAudioBase64 校验并规范化音频 base64
// Python base64.b64decode 忽略空白字符，这里同样先剔除空白再解码，
// 兼容缺失 padding 的输入，最终统一用标准 base64 重新编码
func normalizeGeminiAudioBase64(raw string) (string, error) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(cleaned)
	}
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(decoded), nil
}

// mergeAnyMaps 复制并合并两个参数表，后者覆盖前者
func mergeAnyMaps(base, override map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

// snapshotFailure 保存音频转写失败快照；音频只保留引用摘要，不写原始字节
func (t *geminiAudioTranscriber) snapshotFailure(recorder *snapshotAttemptRecorder, started time.Time, prompt, mimeType string, encodedLength int, body map[string]any, err error) error {
	if err == nil {
		return nil
	}
	items := []ContextItem{NewUserMessageItem(ContextContentPart{Type: ContextPartText, Text: prompt})}
	summary := "音频转写请求"
	if mimeType != "" {
		summary += "（" + mimeType + "）"
	}
	if encodedLength > 0 {
		summary += fmt.Sprintf("，base64 长度 %d", encodedLength)
	}
	items = append(items, NewProviderOpaqueItem("audio", summary))
	internal := map[string]any{
		"operation": "audio_transcription",
		"base_url":  t.client.baseURL,
		"mime_type": mimeType,
	}
	spec := newRequestSnapshotSpec(t.client.cfg, "gemini.audio_transcription", "audio_transcription", nil, nil, body, internal)
	spec.LogicalTurnID = snapshotLogicalTurnID(recorder)
	spec.ContextItems = items
	spec.Error = err
	spec.StartedAt = started
	recordSnapshotAttempt(recorder, &spec, started)
	return captureRequestSnapshot(spec)
}
