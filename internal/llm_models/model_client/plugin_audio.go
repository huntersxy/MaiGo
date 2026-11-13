package model_client

// 插件 Provider 语音转写
// 对齐 MaiBot PluginLLMClient.get_audio_transcriptions：
//   - 通过 audio_transcription 操作调用插件 Provider
//   - 请求载荷包含 audio_base64、extra_params、max_tokens、model_info、request_kind、api_provider
//   - 响应优先取 content/response；兼容 output_items 文本块
//   - 插件 RPC 错误由 pluginModel.invoke 统一转换为 ParseError

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type pluginAudioTranscriber struct {
	model *pluginModel
}

func newPluginAudioTranscriber(cfg ModelConfig) (AudioTranscriber, error) {
	pluginID, invoker, err := resolvePluginProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &pluginAudioTranscriber{model: &pluginModel{cfg: cfg, pluginID: pluginID, invoker: invoker}}, nil
}

func (t *pluginAudioTranscriber) Transcribe(ctx context.Context, req AudioTranscriptionRequest) (AudioTranscriptionResult, error) {
	if t == nil || t.model == nil {
		return AudioTranscriptionResult{}, fmt.Errorf("插件语音转写客户端未初始化")
	}
	audioBase64 := strings.TrimSpace(req.AudioBase64)
	if audioBase64 == "" {
		return AudioTranscriptionResult{}, &ParseError{Message: "音频转写请求缺少音频数据"}
	}
	extra := mergeAnyMaps(t.model.cfg.ExtraParams, req.ExtraParams)
	if mimeType := strings.TrimSpace(req.MIMEType); mimeType != "" {
		extra["audio_mime_type"] = mimeType
	}
	request := map[string]any{
		"audio_base64": audioBase64,
		"extra_params": cloneAnyMap(extra),
		"model_info":   t.model.modelInfoSnapshot(),
		"request_kind": "audio_transcription",
		"api_provider": t.model.apiProviderSnapshot(true),
	}
	if req.MaxTokens > 0 {
		request["max_tokens"] = req.MaxTokens
	} else {
		request["max_tokens"] = nil
	}
	result, err := t.model.invoke(ctx, pluginOperationAudioTranscription, request)
	if err != nil {
		return AudioTranscriptionResult{}, err
	}
	text := strings.TrimSpace(pluginFirstString(result, "content", "response"))
	if text == "" {
		text = pluginAudioOutputText(result)
	}
	if text == "" {
		return AudioTranscriptionResult{}, &ParseError{Message: "插件 LLM Provider 音频转写响应缺少文本内容"}
	}
	return AudioTranscriptionResult{
		Text:  text,
		Usage: pluginUsage(result["usage"]),
	}, nil
}

// pluginAudioOutputText 兼容插件返回 output_items 的 Item 协议
func pluginAudioOutputText(result map[string]any) string {
	rawItems, ok := result["output_items"]
	if !ok || rawItems == nil {
		return ""
	}
	items, err := pluginOutputItems(rawItems, result["item_schema_version"], NewContextUUID())
	if err != nil {
		return ""
	}
	message, err := AgenticMessageFromContextItems(items)
	if err != nil || message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block != nil && block.Type == schema.ContentBlockTypeAssistantGenText && block.AssistantGenText != nil {
			builder.WriteString(block.AssistantGenText.Text)
		}
	}
	return strings.TrimSpace(builder.String())
}

var _ AudioTranscriber = (*pluginAudioTranscriber)(nil)
