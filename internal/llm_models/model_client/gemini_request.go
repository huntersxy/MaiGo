package model_client

// Gemini 请求构造
// 对齐 MaiBot gemini_client.py 的 _convert_messages / _build_tools / _build_generation_config /
// _filter_generate_content_extra_params / clamp_thinking_budget

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	// geminiExtraProviderKey 工具调用额外信息中的 Provider 键，对齐 MaiBot GEMINI_EXTRA_CONTENT_PROVIDER_KEY
	geminiExtraProviderKey = "google"
	// geminiExtraThoughtSignatureKey thought_signature 字段名，对齐 MaiBot GEMINI_EXTRA_CONTENT_THOUGHT_SIGNATURE_KEY
	geminiExtraThoughtSignatureKey = "thought_signature"
	// geminiExtraThoughtSignaturesKey 消息级 thought_signature 索引：call_id -> base64
	geminiExtraThoughtSignaturesKey = "thought_signatures"

	geminiThinkingBudgetAuto     = -1
	geminiThinkingBudgetDisabled = 0
)

// geminiFallbackThoughtSignature 历史工具调用缺少签名时的官方占位签名
// 对齐 MaiBot GEMINI_FALLBACK_THOUGHT_SIGNATURE（REST 的 bytes 字段按 base64 传输）
var geminiFallbackThoughtSignature = base64.StdEncoding.EncodeToString([]byte("skip_thought_signature_validator"))

// geminiSafetySettings 对齐 MaiBot GEMINI_SAFE_SETTINGS
var geminiSafetySettings = []map[string]string{
	{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_NONE"},
	{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_NONE"},
	{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"},
	{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_NONE"},
	{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
}

// geminiReservedExtraParams 由客户端自行处理、不透传 generationConfig 的额外参数
var geminiReservedExtraParams = map[string]bool{
	"thinking_budget":      true,
	"include_thoughts":     true,
	"enable_google_search": true,
	"transcription_prompt": true,
	"audio_mime_type":      true,
}

// geminiExtraConfigKeys extra_params 中可透传到 generationConfig 的字段（snake_case → REST 字段名）
var geminiExtraConfigKeys = map[string]string{
	"temperature":                "temperature",
	"max_output_tokens":          "maxOutputTokens",
	"top_p":                      "topP",
	"top_k":                      "topK",
	"candidate_count":            "candidateCount",
	"stop_sequences":             "stopSequences",
	"presence_penalty":           "presencePenalty",
	"frequency_penalty":          "frequencyPenalty",
	"seed":                       "seed",
	"response_logprobs":          "responseLogprobs",
	"logprobs":                   "logprobs",
	"response_mime_type":         "responseMimeType",
	"response_json_schema":       "responseJsonSchema",
	"response_schema":            "responseSchema",
	"response_modalities":        "responseModalities",
	"media_resolution":           "mediaResolution",
	"image_config":               "imageConfig",
	"speech_config":              "speechConfig",
	"audio_timestamp":            "audioTimestamp",
	"audio_transcription_config": "audioTranscriptionConfig",
	"thinking_config":            "thinkingConfig",
	"safety_settings":            "safetySettings",
	"tools":                      "tools",
	"tool_config":                "toolConfig",
}

// geminiThinkingBudgetLimit 单模型的思考预算范围
type geminiThinkingBudgetLimit struct {
	min        int
	max        int
	canDisable bool
}

// geminiThinkingBudgetLimits 对齐 MaiBot THINKING_BUDGET_LIMITS
var geminiThinkingBudgetLimits = map[string]geminiThinkingBudgetLimit{
	"gemini-2.5-flash":      {min: 1, max: 24576, canDisable: true},
	"gemini-2.5-flash-lite": {min: 512, max: 24576, canDisable: true},
	"gemini-2.5-pro":        {min: 128, max: 32768, canDisable: false},
}

// buildRequestBody 组装 generateContent 请求体（测试与兼容入口）
func (c *geminiClient) buildRequestBody(
	ctx context.Context,
	input []*schema.AgenticMessage,
	options *model.Options,
	enableGoogleSearch bool,
) (map[string]any, error) {
	items, err := ContextItemsFromMessages(input)
	if err != nil {
		return nil, err
	}
	return c.buildRequestBodyFromContextItems(ctx, items, options, enableGoogleSearch)
}

// buildRequestBodyFromContextItems 以 Context Item 为唯一请求表示组装 generateContent 请求体
func (c *geminiClient) buildRequestBodyFromContextItems(
	ctx context.Context,
	items []ContextItem,
	options *model.Options,
	enableGoogleSearch bool,
) (map[string]any, error) {
	formatSource := c.cfg.ResponseFormat
	if raw, ok := responseFormatFromContext(ctx); ok {
		formatSource = raw
	}
	contents, systemText, err := BuildGeminiContentsFromContextItems(items)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"contents":       contents,
		"safetySettings": geminiSafetySettings,
	}
	if systemText != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": systemText}},
			"role":  "user",
		}
	}
	generationConfig := c.buildGenerationConfig(options.MaxTokens, options.Temperature, options.TopP, options.Stop, options.Tools, enableGoogleSearch, c.extra)
	formatSpec, err := parseResponseFormat(formatSource)
	if err != nil {
		return nil, err
	}
	applyGeminiResponseFormat(generationConfig, formatSpec)
	if len(generationConfig) > 0 {
		body["generationConfig"] = generationConfig
	}
	if tools := generationConfig["tools"]; tools != nil {
		body["tools"] = tools
		delete(generationConfig, "tools")
	}
	if toolConfig := generationConfig["toolConfig"]; toolConfig != nil {
		body["toolConfig"] = toolConfig
		delete(generationConfig, "toolConfig")
	}
	if mode := geminiToolChoiceMode(options); mode != "" {
		body["toolConfig"] = map[string]any{
			"functionCallingConfig": map[string]any{"mode": mode},
		}
	}
	return body, nil
}

// geminiToolChoiceMode 把 eino 工具选择映射为 Gemini functionCallingConfig.mode
// 对齐 MaiBot：request.tool_choice 目前只使用 "none"（gemini_client.py:1034-1047）
// AgenticToolChoice 是 eino 的推荐字段，旧 ToolChoice 继续兼容
func geminiToolChoiceMode(options *model.Options) string {
	if options == nil {
		return ""
	}
	if options.AgenticToolChoice != nil {
		switch options.AgenticToolChoice.Type {
		case schema.ToolChoiceForbidden:
			return "NONE"
		case schema.ToolChoiceForced:
			return "ANY"
		case schema.ToolChoiceAllowed:
			return "AUTO"
		}
	}
	if options.ToolChoice != nil && *options.ToolChoice == schema.ToolChoiceForbidden {
		return "NONE"
	}
	return ""
}

// buildGenerationConfig 构造 generationConfig（含 tools/toolConfig 暂存，由上层提升到顶层字段）
func (c *geminiClient) buildGenerationConfig(
	maxTokens *int,
	temperature *float32,
	topP *float32,
	stop []string,
	tools []*schema.ToolInfo,
	enableGoogleSearch bool,
	extra map[string]any,
) map[string]any {
		config := filterGeminiExtraParams(extra, c.model)
		// 对齐 Python gemini_client.py:913-914：extra_params 已有 temperature 时不覆盖；
		// send_temperature=false 只让上游把请求温度置空，不剥 extra_params。
		if maxTokens != nil {
			config["maxOutputTokens"] = *maxTokens
		}
		if temperature != nil {
			if _, exists := config["temperature"]; !exists {
				config["temperature"] = *temperature
			}
		}
	if topP != nil {
		config["topP"] = *topP
	}
	if len(stop) > 0 && config["stopSequences"] == nil {
		config["stopSequences"] = stop
	}
	if config["responseModalities"] == nil {
		config["responseModalities"] = []string{"TEXT"}
	}
	if config["thinkingConfig"] == nil {
		config["thinkingConfig"] = map[string]any{
			"includeThoughts": includeThoughtsFromExtra(extra),
			"thinkingBudget":  clampGeminiThinkingBudget(extra, c.model),
		}
	}

	toolPayloads := geminiTools(tools, enableGoogleSearch)
	if len(toolPayloads) > 0 {
		config["tools"] = mergeGeminiTools(config["tools"], toolPayloads)
	}
	return config
}

// filterGeminiExtraParams 对齐 MaiBot _filter_generate_content_extra_params
func filterGeminiExtraParams(extra map[string]any, modelID string) map[string]any {
	filtered := make(map[string]any)
	for key, value := range extra {
		if geminiReservedExtraParams[key] {
			continue
		}
		restKey, ok := geminiExtraConfigKeys[key]
		if !ok {
			continue
		}
		switch restKey {
		case "thinkingConfig", "safetySettings", "toolConfig", "imageConfig", "speechConfig", "audioTranscriptionConfig":
			filtered[restKey] = snakeToCamelValue(value)
		default:
			filtered[restKey] = value
		}
	}
	return filtered
}

// includeThoughtsFromExtra 对齐 MaiBot：include_thoughts 默认开启
func includeThoughtsFromExtra(extra map[string]any) bool {
	value, ok := extra["include_thoughts"]
	if !ok {
		return true
	}
	return boolFromAny(value)
}

// clampGeminiThinkingBudget 对齐 MaiBot GeminiClient.clamp_thinking_budget
func clampGeminiThinkingBudget(extra map[string]any, modelID string) int {
	budget := geminiThinkingBudgetAuto
	if extra != nil {
		if raw, ok := extra["thinking_budget"]; ok {
			if parsed, ok := IntFromAny(raw); ok {
				budget = parsed
			} else {
				zap.L().Warn("Gemini thinking_budget 无效，已回退为自动模式",
					zap.Any("thinking_budget", raw))
			}
		}
	}
	limits, ok := geminiThinkingLimits(modelID)

	if budget == geminiThinkingBudgetAuto {
		return geminiThinkingBudgetAuto
	}
	if budget == geminiThinkingBudgetDisabled {
		if ok && limits.canDisable {
			return geminiThinkingBudgetDisabled
		}
		if ok {
			zap.L().Warn("Gemini 模型不支持禁用思考预算，已回退为最小值",
				zap.String("model", modelID), zap.Int("min", limits.min))
			return limits.min
		}
		return geminiThinkingBudgetAuto
	}
	if !ok {
		zap.L().Warn("Gemini 模型未配置思考预算范围，已回退为自动模式", zap.String("model", modelID))
		return geminiThinkingBudgetAuto
	}
	if budget < limits.min {
		zap.L().Warn("Gemini thinking_budget 过小，已调整为最小值",
			zap.String("model", modelID), zap.Int("thinking_budget", budget), zap.Int("min", limits.min))
		return limits.min
	}
	if budget > limits.max {
		zap.L().Warn("Gemini thinking_budget 过大，已调整为最大值",
			zap.String("model", modelID), zap.Int("thinking_budget", budget), zap.Int("max", limits.max))
		return limits.max
	}
	return budget
}

// geminiThinkingLimits 按「精确匹配 → 最长前缀匹配」查找模型预算范围
func geminiThinkingLimits(modelID string) (geminiThinkingBudgetLimit, bool) {
	if limits, ok := geminiThinkingBudgetLimits[modelID]; ok {
		return limits, true
	}
	prefixes := make([]string, 0, len(geminiThinkingBudgetLimits))
	for prefix := range geminiThinkingBudgetLimits {
		prefixes = append(prefixes, prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	for _, prefix := range prefixes {
		if modelID == prefix || strings.HasPrefix(modelID, prefix+"-") {
			return geminiThinkingBudgetLimits[prefix], true
		}
	}
	return geminiThinkingBudgetLimit{}, false
}

// mergeGeminiTools 合并 extra_params 中已有的 tools 与内置工具
func mergeGeminiTools(existing any, tools []map[string]any) []any {
	merged := make([]any, 0, len(tools)+1)
	switch value := existing.(type) {
	case nil:
	case []any:
		merged = append(merged, value...)
	case []map[string]any:
		for _, item := range value {
			merged = append(merged, item)
		}
	default:
		merged = append(merged, value)
	}
	for _, tool := range tools {
		merged = append(merged, tool)
	}
	return merged
}

// geminiTools 构造 functionDeclarations 与 Google Search 工具
func geminiTools(tools []*schema.ToolInfo, enableGoogleSearch bool) []map[string]any {
	result := make([]map[string]any, 0, 2)
	if declarations, err := toolInfosToGeminiDeclarations(tools); err == nil && len(declarations) > 0 {
		result = append(result, map[string]any{"functionDeclarations": declarations})
	} else if err != nil {
		zap.L().Warn("Gemini 工具定义解析失败，已跳过该批工具", zap.Error(err))
	}
	if enableGoogleSearch {
		result = append(result, map[string]any{"googleSearch": map[string]any{}})
	}
	return result
}

// toolInfosToGeminiDeclarations 对齐 MaiBot _build_tools：参数使用 JSON Schema 字段
func toolInfosToGeminiDeclarations(tools []*schema.ToolInfo) ([]map[string]any, error) {
	declarations := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			continue
		}
		declaration := map[string]any{
			"name":        tool.Name,
			"description": tool.Desc,
		}
		if tool.ParamsOneOf != nil {
			jsonSchema, err := tool.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, fmt.Errorf("工具 %q 参数 schema 无效: %w", tool.Name, err)
			}
			if jsonSchema != nil {
				encoded, err := sonic.Marshal(jsonSchema)
				if err != nil {
					return nil, err
				}
				var params map[string]any
				if err := sonic.Unmarshal(encoded, &params); err != nil {
					return nil, err
				}
				declaration["parametersJsonSchema"] = params
			}
		}
		declarations = append(declarations, declaration)
	}
	return declarations, nil
}

// buildGeminiContents 对齐 MaiBot _convert_messages
// 返回 contents、systemInstruction 文本；连续 assistant 消息合并为一个 model content
func buildGeminiContents(messages []*schema.AgenticMessage) ([]map[string]any, string, error) {
	signatureIndex := geminiSignatureIndex(messages)
	toolNameByCallID := make(map[string]string)
	var systemChunks []string
	contents := make([]map[string]any, 0, len(messages))
	var modelTextParts []map[string]any
	var modelCallParts []map[string]any
	groupHasCall := false

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

	for _, message := range messages {
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.AgenticRoleTypeSystem:
			text := strings.TrimSpace(collectMessagePlainText(message))
			if text == "" {
				return nil, "", fmt.Errorf("Gemini 的 system 消息必须为非空文本")
			}
			systemChunks = append(systemChunks, text)
			flushModel()
		case schema.AgenticRoleTypeUser:
			flushModel()
			if hasFunctionToolResults(message) {
				parts, err := geminiFunctionResultParts(message, toolNameByCallID)
				if err != nil {
					return nil, "", err
				}
				if len(parts) > 0 {
					contents = append(contents, map[string]any{"role": "tool", "parts": parts})
				}
				continue
			}
			parts, err := geminiUserParts(message)
			if err != nil {
				return nil, "", err
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		case schema.AgenticRoleTypeAssistant:
			assistantParts, err := geminiAssistantParts(message)
			if err != nil {
				return nil, "", err
			}
			modelTextParts = append(modelTextParts, assistantParts...)
			for _, block := range message.ContentBlocks {
				if block == nil || block.Type != schema.ContentBlockTypeFunctionToolCall || block.FunctionToolCall == nil {
					continue
				}
				call := block.FunctionToolCall
				name := strings.TrimSpace(call.Name)
				if name == "" {
					continue
				}
				signature := geminiBlockSignature(block)
				if signature == "" && call.CallID != "" {
					signature = signatureIndex[call.CallID]
				}
				if signature == "" && !groupHasCall && !hasModelFunctionCall(modelCallParts) {
					// 组内首个工具调用注入占位签名，避免历史 functionCall 校验失败
					signature = geminiFallbackThoughtSignature
				}
				groupHasCall = true
				part := map[string]any{
					"functionCall": geminiFunctionCallPayload(call, name),
				}
				if signature != "" {
					part["thoughtSignature"] = signature
				}
				modelCallParts = append(modelCallParts, part)
				if call.CallID != "" {
					toolNameByCallID[call.CallID] = name
				}
			}
		default:
			flushModel()
		}
	}
	flushModel()

	systemText := strings.Join(nonEmptyStrings(systemChunks), "\n\n")
	return contents, systemText, nil
}

// hasModelFunctionCall 判断本组是否已经出现工具调用（占位签名只注入首个）
func hasModelFunctionCall(parts []map[string]any) bool {
	return len(parts) > 0
}

// geminiUserParts 用户消息的文本与图片片段
func geminiUserParts(message *schema.AgenticMessage) ([]map[string]any, error) {
	var parts []map[string]any
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil && block.UserInputText.Text != "" {
				parts = append(parts, map[string]any{"text": block.UserInputText.Text})
			}
		case schema.ContentBlockTypeUserInputImage:
			part, err := geminiImagePart(block.UserInputImage)
			if err != nil {
				return nil, err
			}
			if part != nil {
				parts = append(parts, part)
			}
		}
	}
	return parts, nil
}

// geminiAssistantParts 助手消息的文本与图片片段
// MaiBot _convert_messages 只投影 AssistantMessageItem 的 parts，ReasoningItem 不写回历史；
// 把思考块作为 thought 文本回放会污染模型上下文，也会让 Gemini 对历史 part 校验失败
func geminiAssistantParts(message *schema.AgenticMessage) ([]map[string]any, error) {
	var parts []map[string]any
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil && block.AssistantGenText.Text != "" {
				parts = append(parts, map[string]any{"text": block.AssistantGenText.Text})
			}
		case schema.ContentBlockTypeUserInputImage:
			part, err := geminiImagePart(block.UserInputImage)
			if err != nil {
				return nil, err
			}
			if part != nil {
				parts = append(parts, part)
			}
		}
	}
	return parts, nil
}

// geminiFunctionCallPayload 构造 functionCall 对象
func geminiFunctionCallPayload(call *schema.FunctionToolCall, name string) map[string]any {
	args := map[string]any{}
	if raw := strings.TrimSpace(call.Arguments); raw != "" {
		_ = sonic.UnmarshalString(raw, &args)
	}
	payload := map[string]any{"name": name, "args": args}
	if id := strings.TrimSpace(call.CallID); id != "" {
		payload["id"] = id
	}
	return payload
}

// geminiFunctionResultParts 工具结果片段
func geminiFunctionResultParts(message *schema.AgenticMessage, toolNameByCallID map[string]string) ([]map[string]any, error) {
	var parts []map[string]any
	for _, block := range message.ContentBlocks {
		if block == nil || block.Type != schema.ContentBlockTypeFunctionToolResult || block.FunctionToolResult == nil {
			continue
		}
		result := block.FunctionToolResult
		name := strings.TrimSpace(result.Name)
		if name == "" && result.CallID != "" {
			name = toolNameByCallID[result.CallID]
		}
		if name == "" {
			return nil, fmt.Errorf("Gemini 无法根据 tool_call_id=%s 找到对应的工具名称，且消息中未携带 tool_name", result.CallID)
		}
		if result.CallID != "" {
			toolNameByCallID[result.CallID] = name
		}
		payload := map[string]any{
			"name":     name,
			"response": geminiFunctionResponsePayload(result),
		}
		if id := strings.TrimSpace(result.CallID); id != "" {
			payload["id"] = id
		}
		parts = append(parts, map[string]any{"functionResponse": payload})
	}
	return parts, nil
}

// geminiFunctionResponsePayload 对齐 MaiBot _normalize_function_response_payload
// json.loads 成功时：dict 原样返回；其他类型（包括 null）包成 {"result": ...}；解析失败回退原始文本
func geminiFunctionResponsePayload(result *schema.FunctionToolResult) map[string]any {
	text := strings.TrimSpace(functionToolResultText(result))
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

// geminiImagePart 图片片段：inline base64 图片
func geminiImagePart(image *schema.UserInputImage) (map[string]any, error) {
	if image == nil {
		return nil, nil
	}
	mimeType := geminiNormalizeImageMIME(image.MIMEType)
	if data := strings.TrimSpace(image.Base64Data); data != "" {
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			// Python gemini_client.py:169-193 直接 base64.b64decode，异常经 :1134-1156 归为 NetworkConnectionError
			return nil, &NetworkError{Message: fmt.Sprintf("Gemini 图片 base64 解码失败: %v", err), Err: err}
		}
		return map[string]any{
			"inlineData": map[string]any{"mimeType": mimeType, "data": data},
		}, nil
	}
	if image.URL != "" {
		// MaiBot 1.4.0 无 URL 图片投影路径；保留占位文本而不是伪造 inlineData
		return map[string]any{"text": fmt.Sprintf("[Gemini 客户端暂不支持 URL 图片: %s]", image.URL)}, nil
	}
	return nil, nil
}

// geminiNormalizeImageMIME 对齐 _normalize_image_mime_type：jpg/jpeg 统一为 image/jpeg
func geminiNormalizeImageMIME(mimeType string) string {
	normalized := strings.ToLower(strings.TrimSpace(mimeType))
	switch normalized {
	case "", "image/jpg", "image/jpeg":
		return "image/jpeg"
	}
	if strings.HasPrefix(normalized, "image/") {
		return normalized
	}
	// 非图片 MIME 无法作为 inlineData 的图片类型，按 JPEG 处理
	return "image/jpeg"
}

// hasFunctionToolResults 判断消息是否携带工具结果
func hasFunctionToolResults(message *schema.AgenticMessage) bool {
	for _, block := range message.ContentBlocks {
		if block != nil && block.Type == schema.ContentBlockTypeFunctionToolResult {
			return true
		}
	}
	return false
}

// collectMessagePlainText 收集文本块内容（system/user 共用）
func collectMessagePlainText(message *schema.AgenticMessage) string {
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.Type == schema.ContentBlockTypeUserInputText && block.UserInputText != nil {
			builder.WriteString(block.UserInputText.Text)
		}
	}
	return builder.String()
}

// functionToolResultText 拼接工具结果文本
func functionToolResultText(result *schema.FunctionToolResult) string {
	var builder strings.Builder
	for _, block := range result.Content {
		if block == nil || block.Type != schema.FunctionToolResultContentBlockTypeText || block.Text == nil {
			continue
		}
		builder.WriteString(block.Text.Text)
	}
	return builder.String()
}

// geminiSignatureIndex 汇总历史消息中的 thought_signature
func geminiSignatureIndex(messages []*schema.AgenticMessage) map[string]string {
	index := make(map[string]string)
	for _, message := range messages {
		if message == nil {
			continue
		}
		for callID, signature := range geminiMessageSignatures(message) {
			if signature != "" {
				index[callID] = signature
			}
		}
		for _, block := range message.ContentBlocks {
			if block == nil || block.FunctionToolCall == nil {
				continue
			}
			if signature := geminiBlockSignature(block); signature != "" {
				index[block.FunctionToolCall.CallID] = signature
			}
		}
	}
	return index
}

// geminiBlockSignature 读取工具调用块上的 thought_signature
func geminiBlockSignature(block *schema.ContentBlock) string {
	if block == nil || len(block.Extra) == 0 {
		return ""
	}
	providerPayload, ok := block.Extra[geminiExtraProviderKey].(map[string]any)
	if !ok {
		return ""
	}
	signature, _ := providerPayload[geminiExtraThoughtSignatureKey].(string)
	return decodeThoughtSignature(signature)
}

// geminiMessageSignatures 读取消息级 thought_signature 索引
func geminiMessageSignatures(message *schema.AgenticMessage) map[string]string {
	if message == nil || len(message.Extra) == 0 {
		return nil
	}
	providerPayload, ok := message.Extra[geminiExtraProviderKey].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := providerPayload[geminiExtraThoughtSignaturesKey]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case map[string]string:
		return value
	case map[string]any:
		index := make(map[string]string, len(value))
		for callID, signature := range value {
			if text, ok := signature.(string); ok {
				index[callID] = text
			}
		}
		return index
	default:
		return nil
	}
}

// geminiMessageSignatureExtra 构造消息级 thought_signature 索引
func geminiMessageSignatureExtra(signatures map[string]string) map[string]any {
	if len(signatures) == 0 {
		return nil
	}
	index := make(map[string]any, len(signatures))
	for callID, signature := range signatures {
		index[callID] = signature
	}
	return map[string]any{
		geminiExtraProviderKey: map[string]any{geminiExtraThoughtSignaturesKey: index},
	}
}

// snakeToCamelValue 递归把 map 的 snake_case 键转成 lowerCamel，供 thinkingConfig 等对象使用
func snakeToCamelValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[snakeToCamelKey(key)] = snakeToCamelValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = snakeToCamelValue(item)
		}
		return out
	default:
		return value
	}
}

// snakeToCamelKey a_b → aB
func snakeToCamelKey(key string) string {
	if !strings.Contains(key, "_") {
		return key
	}
	segments := strings.Split(key, "_")
	var builder strings.Builder
	builder.WriteString(segments[0])
	for _, segment := range segments[1:] {
		if segment == "" {
			continue
		}
		builder.WriteString(strings.ToUpper(segment[:1]))
		builder.WriteString(segment[1:])
	}
	return builder.String()
}
