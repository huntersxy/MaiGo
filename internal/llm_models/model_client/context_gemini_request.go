package model_client

// Context Item 到 Gemini contents 的投影
// 对齐 MaiBot _convert_messages：system 合并为 systemInstruction；连续 function output 合并为 tool content；
// 连续 assistant/function call 合并为 model content；Reasoning 与 Provider Activity 不写回历史

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
)

func BuildGeminiContentsFromContextItems(items []ContextItem) ([]map[string]any, string, error) {
	var systemChunks []string
	contents := make([]map[string]any, 0, len(items))
	toolNameByCallID := make(map[string]string)

	var modelTextParts []map[string]any
	var modelCallParts []map[string]any
	groupHasCall := false

	var toolParts []map[string]any

	flushTool := func() {
		if len(toolParts) == 0 {
			return
		}
		contents = append(contents, map[string]any{"role": "tool", "parts": toolParts})
		toolParts = nil
	}
	flushModel := func() {
		if len(modelTextParts) == 0 && len(modelCallParts) == 0 {
			return
		}
		parts := make([]map[string]any, 0, len(modelTextParts)+len(modelCallParts))
		parts = append(parts, modelTextParts...)
		parts = append(parts, modelCallParts...)
		contents = append(contents, map[string]any{"role": "model", "parts": parts})
		modelTextParts = nil
		modelCallParts = nil
		groupHasCall = false
	}

	for _, item := range items {
		switch item.Type {
		case ContextItemSystemMessage:
			flushTool()
			flushModel()
			text := strings.TrimSpace(geminiContextTextOnly(item.Parts))
			if text == "" {
				return nil, "", fmt.Errorf("Gemini 的 system 消息必须为非空文本")
			}
			systemChunks = append(systemChunks, text)
		case ContextItemUserMessage:
			flushTool()
			flushModel()
			parts, err := geminiContextUserParts(item.Parts)
			if err != nil {
				return nil, "", err
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		case ContextItemFunctionCallOutput:
			flushModel()
			name := strings.TrimSpace(item.ToolName)
			if name == "" && item.CallID != "" {
				name = toolNameByCallID[item.CallID]
			}
			if name == "" {
				return nil, "", fmt.Errorf("Gemini 无法根据 tool_call_id=%s 找到对应的工具名称，且消息中未携带 tool_name", item.CallID)
			}
			if item.CallID != "" {
				toolNameByCallID[item.CallID] = name
			}
			payload := map[string]any{
				"name":     name,
				"response": geminiFunctionResponsePayloadFromText(item.Output),
			}
			if strings.TrimSpace(item.CallID) != "" {
				payload["id"] = item.CallID
			}
			toolParts = append(toolParts, map[string]any{"functionResponse": payload})
		case ContextItemAssistantMessage:
			flushTool()
			parts, err := geminiContextAssistantParts(item.Parts)
			if err != nil {
				return nil, "", err
			}
			modelTextParts = append(modelTextParts, parts...)
		case ContextItemFunctionCall:
			flushTool()
			if item.ToolCall == nil {
				continue
			}
			call := item.ToolCall
			name := strings.TrimSpace(call.FuncName)
			if name == "" {
				continue
			}
			signature := ""
			if item.ProviderData != nil {
				signature, _ = item.ProviderData[geminiExtraThoughtSignatureKey].(string)
			}
			signature = strings.TrimSpace(signature)
			if signature == "" && !groupHasCall && len(modelCallParts) == 0 {
				// 组内首个工具调用注入占位签名，避免历史 functionCall 校验失败
				signature = geminiFallbackThoughtSignature
			}
			groupHasCall = true
			part := map[string]any{"functionCall": geminiContextFunctionCallPayload(call)}
			if signature != "" {
				part["thoughtSignature"] = signature
			}
			modelCallParts = append(modelCallParts, part)
			if call.CallID != "" {
				toolNameByCallID[call.CallID] = name
			}
		case ContextItemReasoning, ContextItemProviderActivity, ContextItemProviderOpaque:
			continue
		default:
			continue
		}
	}
	flushTool()
	flushModel()

	return contents, strings.Join(nonEmptyStrings(systemChunks), "\n\n"), nil
}

// geminiContextTextOnly 提取可见文本，对齐 Python get_item_text 对 text/refusal 的拼接
func geminiContextTextOnly(parts []ContextContentPart) string {
	var builder strings.Builder
	for _, part := range parts {
		switch part.Type {
		case ContextPartText:
			builder.WriteString(part.Text)
		case ContextPartRefusal:
			builder.WriteString(part.Refusal)
		}
	}
	return builder.String()
}

func geminiContextUserParts(parts []ContextContentPart) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.Media != nil {
			mediaPart, err := geminiContextMediaPart(*part.Media)
			if err != nil {
				return nil, err
			}
			out = append(out, mediaPart)
			continue
		}
		switch part.Type {
		case ContextPartText:
			if part.Text != "" {
				out = append(out, map[string]any{"text": part.Text})
			}
		case ContextPartRefusal:
			if part.Refusal != "" {
				out = append(out, map[string]any{"text": part.Refusal})
			}
		case ContextPartImage:
			imagePart, err := geminiContextImagePart(part)
			if err != nil {
				return nil, err
			}
			if imagePart != nil {
				out = append(out, imagePart)
			}
		}
	}
	return out, nil
}

func geminiContextAssistantParts(parts []ContextContentPart) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.Media != nil {
			mediaPart, err := geminiContextMediaPart(*part.Media)
			if err != nil {
				return nil, err
			}
			out = append(out, mediaPart)
			continue
		}
		switch part.Type {
		case ContextPartText:
			if part.Text != "" {
				out = append(out, map[string]any{"text": part.Text})
			}
		case ContextPartRefusal:
			if part.Refusal != "" {
				out = append(out, map[string]any{"text": part.Refusal})
			}
		case ContextPartImage:
			imagePart, err := geminiContextImagePart(part)
			if err != nil {
				return nil, err
			}
			if imagePart != nil {
				out = append(out, imagePart)
			}
		}
	}
	return out, nil
}

// geminiContextMediaPart 把音频/视频/文件映射为 Gemini inlineData 或 fileData。
// 直接 HTTP URL 无法被 generateContent 自动抓取，必须明确报错而不是静默丢弃。
func geminiContextMediaPart(media ContextMedia) (map[string]any, error) {
	kind := strings.TrimSpace(media.Kind)
	if kind == "" {
		kind = "媒体"
	}
	mimeType := strings.TrimSpace(media.MIMEType)
	if mimeType == "" {
		switch kind {
		case "audio":
			mimeType = "audio/wav"
		case "video":
			mimeType = "video/mp4"
		default:
			mimeType = "application/octet-stream"
		}
	}
	if media.Base64Data != "" {
		if _, err := base64.StdEncoding.DecodeString(media.Base64Data); err != nil {
			return nil, fmt.Errorf("Gemini %s 媒体 base64 解码失败: %w", kind, err)
		}
		return map[string]any{"inlineData": map[string]any{"mimeType": mimeType, "data": media.Base64Data}}, nil
	}
	if media.FileID != "" {
		return map[string]any{"fileData": map[string]any{"mimeType": mimeType, "fileUri": media.FileID}}, nil
	}
	if media.URL != "" {
		return nil, fmt.Errorf("Gemini 不支持直接 URL 媒体输入（%s），请提供 base64 或 Files API file_uri", kind)
	}
	return nil, fmt.Errorf("Gemini %s 媒体缺少 base64 数据或 file_uri", kind)
}

func geminiContextImagePart(part ContextContentPart) (map[string]any, error) {
	if strings.TrimSpace(part.ImageBase64) == "" {
		return nil, nil
	}
	if _, err := base64.StdEncoding.DecodeString(part.ImageBase64); err != nil {
		// Python gemini_client.py:169-193 直接 base64.b64decode，异常经 :1134-1156 归为 NetworkConnectionError
		return nil, &NetworkError{Message: fmt.Sprintf("Gemini 图片 base64 解码失败: %v", err), Err: err}
	}
	return map[string]any{
		"inlineData": map[string]any{
			"mimeType": geminiNormalizeImageMIME(ContextImageMIME(part.ImageFormat)),
			"data":     part.ImageBase64,
		},
	}, nil
}

func geminiContextFunctionCallPayload(call *ContextToolCall) map[string]any {
	args := map[string]any{}
	if call != nil && len(call.Args) > 0 {
		args = call.Args
	}
	payload := map[string]any{"name": call.FuncName, "args": args}
	if strings.TrimSpace(call.CallID) != "" {
		payload["id"] = call.CallID
	}
	return payload
}

// geminiFunctionResponsePayloadFromText 对齐 _normalize_function_response_payload
func geminiFunctionResponsePayloadFromText(output string) map[string]any {
	text := strings.TrimSpace(output)
	if text == "" {
		return map[string]any{}
	}
	var parsed any
	if err := sonic.UnmarshalString(text, &parsed); err == nil {
		if object, ok := parsed.(map[string]any); ok {
			return object
		}
		return map[string]any{"result": parsed}
	}
	return map[string]any{"result": text}
}
