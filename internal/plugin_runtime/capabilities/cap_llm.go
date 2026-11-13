package capabilities

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

func normalizePromptArg(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case []any:
		var builder strings.Builder
		for _, item := range value {
			if part, ok := item.(string); ok {
				builder.WriteString(part)
			}
		}
		return builder.String()
	default:
		return stringValue(raw)
	}
}

func normalizeEmbeddingTextArg(args map[string]any) (string, error) {
	raw, ok := args["text"]
	if !ok || raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("text 必须是字符串")
	}
	return text, nil
}

func normalizeEmbeddingTextsArg(args map[string]any) ([]string, error) {
	raw, ok := args["texts"]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		if texts, ok := raw.([]string); ok {
			return texts, nil
		}
		return nil, fmt.Errorf("texts 必须是字符串列表")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("texts 中的每一项必须是非空字符串")
		}
		out = append(out, text)
	}
	return out, nil
}

func normalizeAudioBase64Arg(args map[string]any) (string, error) {
	raw := sendStringArg(args, "audio_base64", "voice_base64", "base64")
	if raw == "" {
		return "", nil
	}
	if index := strings.Index(raw, ","); strings.HasPrefix(raw, "data:") && index >= 0 {
		raw = raw[index+1:]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
		if _, rawErr := base64.RawStdEncoding.DecodeString(raw); rawErr != nil {
			return "", fmt.Errorf("音频 Base64 数据不合法")
		}
	}
	return raw, nil
}

func buildLLMRequest(pluginID string, args map[string]any, withTools bool) (PluginLLMRequest, error) {
	_, taskNameProvided := args["task_name"]
	req := PluginLLMRequest{
		TaskName:         strings.TrimSpace(sendStringArg(args, "task_name")),
		TaskNameProvided: taskNameProvided,
		RequestType:      "plugin." + pluginID,
		Prompt:           normalizePromptArg(args["prompt"]),
		Model:            strings.TrimSpace(sendStringArg(args, "model")),
		ModelName:        strings.TrimSpace(sendStringArg(args, "model_name")),
	}
	if req.Model != "" && req.ModelName != "" && req.Model != req.ModelName {
		return req, fmt.Errorf("model 与 model_name 不能指定不同的模型")
	}
	if raw, ok := args["temperature"]; ok && raw != nil {
		value, err := coerceFloat64(raw)
		if err != nil {
			return req, fmt.Errorf("temperature 必须是数字")
		}
		req.Temperature = &value
	}
	if raw, ok := args["max_tokens"]; ok && raw != nil {
		value, err := coerceInt64(raw)
		if err != nil {
			return req, fmt.Errorf("max_tokens 必须是整数")
		}
		tokens := int(value)
		req.MaxTokens = &tokens
	}
	if withTools {
		raw, ok := args["tools"]
		if !ok || raw == nil {
			raw = args["tool_options"]
		}
		if raw != nil {
			list, ok := raw.([]any)
			if !ok {
				return req, fmt.Errorf("tools 必须为列表")
			}
			req.Tools = list
		}
	}
	return req, nil
}

// makeLLMCapabilityHandler 构造 llm.* 能力
func makeLLMCapabilityHandler(port PluginLLMPort, capability string) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		if port == nil {
			return pluginPortNotWired(capability), nil
		}
		switch capability {
		case CapLLMGenerate:
			req, err := buildLLMRequest(pluginID, args, false)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			payload, err := port.Generate(ctx, pluginID, req)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			return payload, nil
		case CapLLMGenerateWithTools:
			req, err := buildLLMRequest(pluginID, args, true)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			payload, err := port.Generate(ctx, pluginID, req)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			return payload, nil
		case CapLLMEmbed:
			text, err := normalizeEmbeddingTextArg(args)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			texts, err := normalizeEmbeddingTextsArg(args)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			if text == "" && len(texts) == 0 {
				return capabilityFailure("缺少必要参数 text 或 texts"), nil
			}
			if text != "" && len(texts) > 0 {
				return capabilityFailure("text 与 texts 只能提供一个"), nil
			}
			req := PluginEmbedRequest{Texts: texts}
			if text != "" {
				req.Texts = []string{text}
			}
			if raw, ok := args["max_concurrent"]; ok && raw != nil {
				value, err := coerceInt64(raw)
				if err != nil {
					return capabilityFailure("max_concurrent 必须是整数"), nil
				}
				req.MaxConcurrent = int(value)
			}
			payload, err := port.Embed(ctx, pluginID, req)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			return payload, nil
		case CapLLMTranscribeAudio:
			audio, err := normalizeAudioBase64Arg(args)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			if audio == "" {
				return capabilityFailure("缺少必要参数 audio_base64 或 voice_base64"), nil
			}
			payload, err := port.Transcribe(ctx, pluginID, audio)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			return payload, nil
		case CapLLMGetAvailableModels:
			models, err := port.AvailableModels(ctx)
			if err != nil {
				return capabilityFailure("%s", err.Error()), nil
			}
			return map[string]any{"success": true, "models": models}, nil
		default:
			return nil, &CapabilityError{Code: "E_METHOD_NOT_ALLOWED", Message: "未注册的能力: " + capability}
		}
	}
}
