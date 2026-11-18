package model_client

// Chat Completions 请求构造：消息转换、工具定义、extra_params 拆分、图片 data URI、
// max_tokens 回退。
//
// 对等 openai_client.py 的 _convert_messages / _convert_tool_options /
// split_openai_request_overrides。图片规范化对齐 _normalize_image_part_for_openai：
// jpeg/png/webp 原样回传 base64，gif/bmp/tiff 解码首帧转 PNG，无法解码时由调用方
// 补 "[图片内容不可用]"。Python 用 PIL 可识别任意可解码格式，Go 限于 image 注册表。
import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/url"
	"strings"
	"sync"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	chatReasoningModeAuto     = "auto"
	chatReasoningModeNative   = "native"
	chatReasoningModeThinkTag = "think_tag"
	chatReasoningModeNone     = "none"

	chatToolArgModeAuto         = "auto"
	chatToolArgModeStrict       = "strict"
	chatToolArgModeRepair       = "repair"
	chatToolArgModeDoubleDecode = "double_decode"

	// chatImageUnavailableText 图片不可用时的占位文本，对齐 MaiBot "[图片内容不可用]"
	chatImageUnavailableText = "[图片内容不可用]"
)

// chatCompletionsReservedExtraBodyKeys 与 MaiBot CHAT_COMPLETIONS_RESERVED_EXTRA_BODY_KEYS 一致
// 只用于过滤 extra_params 顶层的散落键，body 映射内不生效
var chatCompletionsReservedExtraBodyKeys = map[string]bool{
	"max_tokens":      true,
	"messages":        true,
	"model":           true,
	"response_format": true,
	"stream":          true,
	"temperature":     true,
	"tools":           true,
}

// chatMaxCompletionTokensModels 记录需要 max_completion_tokens 的 (base_url, model)
// 对齐 MaiBot 模块级集合 _MODELS_REQUIRING_MAX_COMPLETION_TOKENS
var chatMaxCompletionTokensModels sync.Map

// chatCompletionsOverrides extra_params 的拆分结果
type chatCompletionsOverrides struct {
	headers map[string]string
	query   map[string]string
	body    map[string]any
}

func normalizeChatReasoningMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", chatReasoningModeAuto:
		return chatReasoningModeAuto
	case chatReasoningModeNative:
		return chatReasoningModeNative
	case chatReasoningModeThinkTag:
		return chatReasoningModeThinkTag
	case chatReasoningModeNone:
		return chatReasoningModeNone
	default:
		// MaiBot 的枚举校验会拒绝非法值；Go 侧降级为 auto 并告警
		zap.L().Warn("reasoning_parse_mode 无效，已回退为 auto", zap.String("value", raw))
		return chatReasoningModeAuto
	}
}

func normalizeChatToolArgumentMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", chatToolArgModeAuto:
		return chatToolArgModeAuto
	case chatToolArgModeStrict:
		return chatToolArgModeStrict
	case chatToolArgModeRepair:
		return chatToolArgModeRepair
	case chatToolArgModeDoubleDecode:
		return chatToolArgModeDoubleDecode
	default:
		zap.L().Warn("tool_argument_parse_mode 无效，已回退为 auto", zap.String("value", raw))
		return chatToolArgModeAuto
	}
}

// chatReasoningKey 对齐 _build_reasoning_key：Groq/OpenRouter 用 reasoning，其余用 reasoning_content
func chatReasoningKey(baseURL string) string {
	host := ""
	if parsed, err := url.Parse(baseURL); err == nil {
		host = parsed.Hostname()
	}
	if host == "" && !strings.Contains(baseURL, "://") {
		if parsed, err := url.Parse("//" + baseURL); err == nil {
			host = parsed.Hostname()
		}
	}
	switch strings.ToLower(host) {
	case "api.groq.com", "openrouter.ai":
		return "reasoning"
	default:
		return "reasoning_content"
	}
}

// splitChatCompletionsOverrides 对齐 split_openai_request_overrides
func splitChatCompletionsOverrides(extraParams map[string]any) chatCompletionsOverrides {
	overrides := chatCompletionsOverrides{
		headers: map[string]string{},
		query:   map[string]string{},
		body:    map[string]any{},
	}
	if len(extraParams) == 0 {
		return overrides
	}
	if raw, ok := extraParams["headers"]; ok {
		overrides.headers = chatStringMap(raw)
	}
	if raw, ok := extraParams["query"]; ok {
		overrides.query = chatStringMap(raw)
	}
	if raw, ok := extraParams["body"]; ok {
		if body, ok := raw.(map[string]any); ok {
			for key, value := range body {
				overrides.body[key] = value
			}
		}
	}
	for key, value := range extraParams {
		switch key {
		case "headers", "query", "body":
			continue
		}
		if chatCompletionsReservedExtraBodyKeys[key] {
			continue
		}
		overrides.body[key] = value
	}
	return overrides
}

func chatStringMap(raw any) map[string]string {
	result := map[string]string{}
	switch source := raw.(type) {
	case map[string]string:
		for key, value := range source {
			result[key] = value
		}
	case map[string]any:
		for key, value := range source {
			result[key] = chatScalarString(value)
		}
	}
	return result
}

// ChatToolChoiceValue 把 eino 工具选择映射为 Chat Completions 值
// 对齐 MaiBot：request.tool_choice 目前只使用 "none"
func ChatToolChoiceValue(options *model.Options) string {
	if options == nil {
		return ""
	}
	if options.AgenticToolChoice != nil {
		return chatToolChoiceFromSchema(options.AgenticToolChoice.Type)
	}
	if options.ToolChoice != nil {
		return chatToolChoiceFromSchema(*options.ToolChoice)
	}
	return ""
}

func chatToolChoiceFromSchema(value schema.ToolChoice) string {
	switch value {
	case schema.ToolChoiceForbidden:
		return "none"
	case schema.ToolChoiceForced:
		return "required"
	case schema.ToolChoiceAllowed:
		return "auto"
	default:
		return ""
	}
}

// buildRequestBody 构造 Chat Completions 请求体（测试与兼容入口）
// 返回是否发送了旧式 max_tokens，供 400 回退判断
func (c *chatCompletionsClient) buildRequestBody(input []*schema.AgenticMessage, options *model.Options, stream bool, useMaxCompletionTokens bool) (map[string]any, bool, error) {
	items, err := contextItemsForConfig(input, c.cfg)
	if err != nil {
		return nil, false, err
	}
	return c.buildRequestBodyFromContextItems(items, options, stream, useMaxCompletionTokens)
}

// buildRequestBodyFromContextItems 以 Context Item 为唯一请求表示构造请求体
func (c *chatCompletionsClient) buildRequestBodyFromContextItems(items []ContextItem, options *model.Options, stream bool, useMaxCompletionTokens bool) (map[string]any, bool, error) {
	includeTools := options != nil && len(options.Tools) > 0
	var reasoningScope *ContextProviderScope
	if includeTools {
		reasoningScope = c.reasoningScope
	}
	messages, err := BuildChatMessagesFromContextItemsWithScope(items, includeTools, reasoningScope)
	if err != nil {
		return nil, false, err
	}
	modelName := c.model
	if options != nil && options.Model != nil && strings.TrimSpace(*options.Model) != "" {
		modelName = strings.TrimSpace(*options.Model)
	}
	body := map[string]any{
		"model":    modelName,
		"messages": messages,
		"stream":   stream,
	}
	if includeTools {
		tools, err := BuildChatToolDefinitions(options.Tools)
		if err != nil {
			return nil, false, err
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
	}

	extraBody := cloneAnyMap(c.extra.body)
	if extraBody == nil {
		extraBody = map[string]any{}
	}
	toolChoice := ChatToolChoiceValue(options)
	if toolChoice != "" {
		extraBody["tool_choice"] = toolChoice
	}
	// 对齐 Python openai_client.py:1590-1618：
	// extra_body 已含 temperature/max_tokens 时省略原生参数，由最终 extra_body 合并覆盖；
	// send_temperature=false 时 utils_model 把请求温度置空，但仍不剥掉 extra_body 里的温度。
	_, hasExtraTemperature := extraBody["temperature"]
	if options != nil && options.Temperature != nil && !hasExtraTemperature {
		body["temperature"] = *options.Temperature
	}

	sentLegacyMaxTokens := false
	_, hasExtraMaxTokens := extraBody["max_tokens"]
	_, hasExtraMaxCompletion := extraBody["max_completion_tokens"]
	if useMaxCompletionTokens {
		delete(extraBody, "max_tokens")
		if !hasExtraMaxCompletion && options != nil && options.MaxTokens != nil {
			extraBody["max_completion_tokens"] = *options.MaxTokens
		}
	} else if options != nil && options.MaxTokens != nil && !hasExtraMaxTokens && !hasExtraMaxCompletion {
		body["max_tokens"] = *options.MaxTokens
		sentLegacyMaxTokens = true
	} else {
		sentLegacyMaxTokens = hasExtraMaxTokens
	}

	formatSpec, err := parseResponseFormat(c.cfg.ResponseFormat)
	if err != nil {
		return nil, false, err
	}
	if format := chatResponseFormat(formatSpec); format != nil {
		body["response_format"] = format
	}

	// extra_body 最后合并，与 OpenAI SDK 行为一致（extra_body 覆盖同名顶层参数）
	for key, value := range extraBody {
		body[key] = value
	}
	return body, sentLegacyMaxTokens, nil
}

func (c *chatCompletionsClient) maxCompletionTokensKey() string {
	return c.baseURL + "\x00" + c.model
}

func (c *chatCompletionsClient) requiresMaxCompletionTokens() bool {
	_, ok := chatMaxCompletionTokensModels.Load(c.maxCompletionTokensKey())
	return ok
}

func (c *chatCompletionsClient) rememberMaxCompletionTokensRequired() {
	chatMaxCompletionTokensModels.Store(c.maxCompletionTokensKey(), struct{}{})
}

// maxCompletionTokensRetryPayload 对齐 MaiBot 的 400 回退：命中特征后改用 max_completion_tokens 重试一次
func (c *chatCompletionsClient) maxCompletionTokensRetryPayload(err error, items []ContextItem, options *model.Options, stream bool, sentLegacyMaxTokens bool) (map[string]any, bool) {
	if !sentLegacyMaxTokens {
		return nil, false
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != 400 {
		return nil, false
	}
	if !strings.Contains(strings.ToLower(statusErr.Message), "max_completion_tokens") {
		return nil, false
	}
	c.rememberMaxCompletionTokensRequired()
	payload, _, buildErr := c.buildRequestBodyFromContextItems(items, options, stream, true)
	if buildErr != nil {
		return nil, false
	}
	return payload, true
}

// buildChatMessages 对齐 _convert_messages：连续 assistant 记录合并为一条，
// 保证工具结果前紧邻带 tool_calls 的 assistant 消息。
func buildChatMessages(input []*schema.AgenticMessage, includeTools bool) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, len(input))
	assistantBlocks := make([]*schema.ContentBlock, 0)
	flushAssistant := func() error {
		if len(assistantBlocks) == 0 {
			return nil
		}
		converted, err := buildChatAssistantMessage(assistantBlocks, includeTools)
		assistantBlocks = assistantBlocks[:0]
		if err != nil {
			return err
		}
		if converted != nil {
			messages = append(messages, converted)
		}
		return nil
	}
	for _, message := range input {
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.AgenticRoleTypeSystem:
			if err := flushAssistant(); err != nil {
				return nil, err
			}
			text, err := chatTextOnlyContent(message.ContentBlocks)
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": "system", "content": text})
		case schema.AgenticRoleTypeAssistant:
			assistantBlocks = append(assistantBlocks, message.ContentBlocks...)
		default:
			if err := flushAssistant(); err != nil {
				return nil, err
			}
			converted, err := buildChatUserMessages(message.ContentBlocks, includeTools)
			if err != nil {
				return nil, err
			}
			messages = append(messages, converted...)
		}
	}
	if err := flushAssistant(); err != nil {
		return nil, err
	}
	return messages, nil
}

// chatTextOnlyContent 对齐 _convert_text_only_message_content：只接受文本块并按顺序拼接
func chatTextOnlyContent(blocks []*schema.ContentBlock) (string, error) {
	var builder strings.Builder
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if block.UserInputText != nil {
			builder.WriteString(block.UserInputText.Text)
			continue
		}
		return "", &ParseError{Message: "Chat Completions 的 system 消息仅支持文本内容"}
	}
	return builder.String(), nil
}

// buildChatUserMessages 转换 user 消息：工具结果转 tool 消息，其余内容转 user 消息
func buildChatUserMessages(blocks []*schema.ContentBlock, includeTools bool) ([]map[string]any, error) {
	results := make([]*schema.FunctionToolResult, 0, len(blocks))
	hasContent := false
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if block.FunctionToolResult != nil {
			results = append(results, block.FunctionToolResult)
			continue
		}
		hasContent = true
	}
	messages := make([]map[string]any, 0, len(results)+1)
	if len(results) > 0 && includeTools {
		for _, result := range results {
			callID := ""
			if result != nil {
				callID = result.CallID
			}
			messages = append(messages, map[string]any{
				"role":         "tool",
				"content":      ChatToolResultText(result),
				"tool_call_id": callID,
			})
		}
	}
	// 无工具请求时丢弃工具调用/结果，对齐 _sanitize_messages_for_toolless_request
	if hasContent {
		content, err := buildChatUserContent(blocks)
		if err != nil {
			return nil, err
		}
		messages = append(messages, map[string]any{"role": "user", "content": content})
	}
	return messages, nil
}

// ChatToolResultText 把工具结果文本块拼接为字符串；纯多媒体结果给出清单式占位
func ChatToolResultText(result *schema.FunctionToolResult) string {
	if result == nil {
		return ""
	}
	texts := make([]string, 0, len(result.Content))
	hasMedia := false
	for _, content := range result.Content {
		if content == nil {
			continue
		}
		if content.Text != nil {
			texts = append(texts, content.Text.Text)
			continue
		}
		hasMedia = true
	}
	text := strings.Join(texts, "\n")
	if text == "" && hasMedia {
		return "[工具结果不包含文本内容]"
	}
	return text
}

// buildChatUserContent 对齐 _convert_user_message_content
func buildChatUserContent(blocks []*schema.ContentBlock) (any, error) {
	texts := make([]string, 0, len(blocks))
	onlyText := true
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if block.UserInputText == nil {
			onlyText = false
			break
		}
		texts = append(texts, block.UserInputText.Text)
	}
	if onlyText {
		return strings.Join(texts, ""), nil
	}
	parts := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if block == nil {
			continue
		}
		switch {
		case block.UserInputText != nil:
			if strings.TrimSpace(block.UserInputText.Text) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": block.UserInputText.Text})
			}
		case block.UserInputImage != nil:
			if part, ok := buildChatImagePart(block.UserInputImage); ok {
				parts = append(parts, part)
			} else {
				parts = append(parts, map[string]any{"type": "text", "text": chatImageUnavailableText})
			}
		case block.UserInputAudio != nil:
			parts = append(parts, map[string]any{"type": "text", "text": "[音频内容不可用]"})
		case block.UserInputVideo != nil:
			parts = append(parts, map[string]any{"type": "text", "text": "[视频内容不可用]"})
		case block.UserInputFile != nil:
			parts = append(parts, map[string]any{"type": "text", "text": "[文件内容不可用]"})
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return parts, nil
}

// buildChatAssistantMessage 对齐 assistant 分组：文本拼接，工具调用转 tool_calls
func buildChatAssistantMessage(blocks []*schema.ContentBlock, includeTools bool) (map[string]any, error) {
	texts := make([]string, 0, len(blocks))
	toolCalls := make([]map[string]any, 0)
	for _, block := range blocks {
		if block == nil {
			continue
		}
		switch {
		case block.AssistantGenText != nil:
			texts = append(texts, block.AssistantGenText.Text)
		case block.FunctionToolCall != nil:
			if !includeTools || strings.TrimSpace(block.FunctionToolCall.Name) == "" {
				continue
			}
			callID := block.FunctionToolCall.CallID
			if callID == "" {
				callID = fmt.Sprintf("tool_call_%d", len(toolCalls))
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      block.FunctionToolCall.Name,
					"arguments": chatAssistantArguments(block.FunctionToolCall),
				},
			})
		}
	}
	content := strings.Join(texts, "")
	if content == "" && len(toolCalls) == 0 {
		return nil, nil
	}
	message := map[string]any{"role": "assistant", "content": nil}
	if content != "" {
		message["content"] = content
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	return message, nil
}

// chatAssistantArguments 保证回放的 assistant 工具调用参数是合法 JSON
func chatAssistantArguments(call *schema.FunctionToolCall) string {
	if call == nil {
		return "{}"
	}
	raw := strings.TrimSpace(call.Arguments)
	if raw == "" {
		return "{}"
	}
	var decoded any
	if err := sonic.UnmarshalString(raw, &decoded); err == nil {
		return raw
	}
	if repaired, err := parseChatToolArguments(raw, chatToolArgModeAuto); err == nil {
		if encoded, marshalErr := sonic.Marshal(repaired); marshalErr == nil {
			return string(encoded)
		}
	}
	zap.L().Warn("Chat Completions 历史工具调用参数不是合法 JSON，已回退为空对象",
		zap.String("tool", call.Name))
	return "{}"
}

// BuildChatToolDefinitions 对齐 _convert_tool_options
func BuildChatToolDefinitions(tools []*schema.ToolInfo) ([]map[string]any, error) {
	definitions := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			continue
		}
		parameters := map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []any{},
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
				var parsed map[string]any
				if err := sonic.Unmarshal(encoded, &parsed); err != nil {
					return nil, err
				}
				if len(parsed) > 0 {
					parameters = parsed
				}
			}
		}
		definitions = append(definitions, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Desc,
				"parameters":  parameters,
			},
		})
	}
	return definitions, nil
}

// buildChatImagePart 对齐 _normalize_image_part_for_openai，返回 image_url 内容片段
func buildChatImagePart(imagePart *schema.UserInputImage) (map[string]any, bool) {
	if imagePart == nil {
		return nil, false
	}
	if strings.TrimSpace(imagePart.Base64Data) != "" {
		format, data, ok := normalizeChatImageData(imagePart.Base64Data)
		if !ok {
			return nil, false
		}
		return map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": fmt.Sprintf("data:image/%s;base64,%s", format, data),
			},
		}, true
	}
	// MaiBot 只处理 base64 图片；go 侧额外透传 data URI 与 http(s) URL
	rawURL := strings.TrimSpace(imagePart.URL)
	lower := strings.ToLower(rawURL)
	if strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": rawURL},
		}, true
	}
	return nil, false
}

// normalizeChatImageData 识别图片格式并返回可发送的 (格式, base64)
func normalizeChatImageData(encoded string) (string, string, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		zap.L().Warn("Chat Completions 图片 base64 解码失败，已跳过", zap.Error(err))
		return "", "", false
	}
	format, err := ChatImageFormat(raw)
	if err != nil {
		zap.L().Warn("Chat Completions 图片格式识别失败，已跳过", zap.Error(err))
		return "", "", false
	}
	switch format {
	case "jpeg", "png", "webp":
		return format, encoded, true
	case "gif", "bmp", "tiff":
		decoded, _, decodeErr := image.Decode(bytes.NewReader(raw))
		if decodeErr != nil {
			zap.L().Warn("Chat Completions 图片解码失败，已跳过", zap.String("format", format), zap.Error(decodeErr))
			return "", "", false
		}
		var buffer bytes.Buffer
		if encodeErr := png.Encode(&buffer, decoded); encodeErr != nil {
			zap.L().Warn("Chat Completions 图片转 PNG 失败，已跳过", zap.String("format", format), zap.Error(encodeErr))
			return "", "", false
		}
		return "png", base64.StdEncoding.EncodeToString(buffer.Bytes()), true
	default:
		zap.L().Warn("Chat Completions 暂不支持该图片格式，已跳过", zap.String("format", format))
		return "", "", false
	}
}

func ChatImageFormat(raw []byte) (string, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	return strings.ToLower(format), nil
}
