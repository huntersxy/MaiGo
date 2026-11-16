package model_client

// Chat Completions 响应解析：非流式整体解析、SSE 增量累积、reasoning/think 解析、工具调用参数
//
// 对等 MaiBot src/llm_models/model_client/openai_client.py 的
// _default_normal_response_parser / _OpenAIStreamAccumulator / _extract_content_blocks /
// _extract_reasoning_and_content / _parse_tool_arguments。
//
// 差异（诚实边界）：
//   - 工具调用参数修复使用 json-repair-go，并在其前后补齐 Python json_repair 的缺失开括号与注释处理
//   - 流式模式在最终响应构建时恢复文本工具调用；流式文本改为结束前统一构建，避免重复下发
//   - finish_reason=length 只记录日志，eino 的 ResponseMeta 无通用 finish_reason 字段

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"MaiGo/internal/common/llmjson"
)

// 工具调用来源标记，对齐 Python TOOL_CALL_SOURCE_EXTRA_KEY 及取值
const (
	toolCallSourceExtraKey  = "tool_call_source"
	toolCallSourceReasoning = "reasoning"
	toolCallSourceResponse  = "response"
)

// chatCompletionResponse 非流式响应
type chatCompletionResponse struct {
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   *chatUsage             `json:"usage"`
}

// chatCompletionChunk 流式事件
type chatCompletionChunk struct {
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   *chatUsage             `json:"usage"`
	// Error 流内 error 事件；HTTP 200 但 data 携带 error 时必须上抛，不能静默忽略
	Error *chatCompletionAPIError `json:"error"`
	// decodeErr 记录本行 JSON 解析失败
	decodeErr error
}

// chatCompletionAPIError 流内 error 事件体
// OpenAI 兼容 Provider 常见形态：{"error":{"message":...,"type":...,"code":...}}，
// code/status 可能是数字、数字字符串或 null
type chatCompletionAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
	Status  any    `json:"status"`
}

// chatCompletionInBandError 把流内 error 事件映射为异常分类
// 对齐 MaiBot openai_client.py 流式解析（error 事件不能当作正常结束）与
// utils_model.py:1186-1249（429/5xx 重试、4xx 硬错误）/ :1251-1274（解析失败重试）：
//   - error.code/status 为数字且 >=400 时按 HTTPStatusError 分类，429/5xx 重试、4xx 硬错误
//   - 无状态码时按可重试的 ParseError 处理（Python SDK 抛 APIError，MaiBot orchestrator 归为
//     未知硬错误；Go 侧沿用 MaiBot 的解析失败重试类别，重试耗尽后仍真实上抛）
func chatCompletionInBandError(apiErr chatCompletionAPIError) error {
	message := strings.TrimSpace(apiErr.Message)
	if message == "" {
		message = "Chat Completions 流式响应返回错误"
	} else {
		message = "Chat Completions 流式响应返回错误: " + message
	}
	if status, ok := chatErrorStatusCode(apiErr.Code); ok && status >= 400 {
		return &HTTPStatusError{StatusCode: status, Message: message, Op: "chat_completions"}
	}
	if status, ok := chatErrorStatusCode(apiErr.Status); ok && status >= 400 {
		return &HTTPStatusError{StatusCode: status, Message: message, Op: "chat_completions"}
	}
	return &ParseError{Message: message}
}

// chatErrorStatusCode 从任意 JSON 值解析 HTTP 状态码
func chatErrorStatusCode(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed < 100 || typed > 599 {
			return 0, false
		}
		return int(typed), true
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed); err != nil {
			return 0, false
		}
		if parsed < 100 || parsed > 599 {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

type chatCompletionChoice struct {
	Index        int                    `json:"index"`
	Message      *chatCompletionMessage `json:"message"`
	Delta        *chatCompletionDelta   `json:"delta"`
	FinishReason string                 `json:"finish_reason"`
}

type chatCompletionMessage struct {
	Content          json.RawMessage `json:"content"`
	ReasoningContent json.RawMessage `json:"reasoning_content"`
	Reasoning        json.RawMessage `json:"reasoning"`
	ToolCalls        []chatToolCall  `json:"tool_calls"`
}

type chatCompletionDelta struct {
	Content          json.RawMessage `json:"content"`
	ReasoningContent json.RawMessage `json:"reasoning_content"`
	Reasoning        json.RawMessage `json:"reasoning"`
	ToolCalls        []chatToolCall  `json:"tool_calls"`
}

type chatToolCall struct {
	Index    *int              `json:"index"`
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function *chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// chatUsage OpenAI 兼容 usage，含缓存命中的可选字段
type chatUsage struct {
	PromptTokens            int                          `json:"prompt_tokens"`
	CompletionTokens        int                          `json:"completion_tokens"`
	TotalTokens             int                          `json:"total_tokens"`
	PromptCacheHitTokens    *int                         `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens   *int                         `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails     *chatPromptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails *chatCompletionTokensDetails `json:"completion_tokens_details"`
}

type chatPromptTokensDetails struct {
	CachedTokens *int `json:"cached_tokens"`
}

type chatCompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// chatContentBlock 响应中的内容块
type chatContentBlock struct {
	Type       string             `json:"type"`
	Text       json.RawMessage    `json:"text"`
	Content    json.RawMessage    `json:"content"`
	OutputText json.RawMessage    `json:"output_text"`
	Reasoning  json.RawMessage    `json:"reasoning"`
	Summary    json.RawMessage    `json:"summary"`
	Function   *chatBlockFunction `json:"function"`
	Name       string             `json:"name"`
	FuncName   string             `json:"func_name"`
	ID         string             `json:"id"`
	CallID     string             `json:"call_id"`
	Input      json.RawMessage    `json:"input"`
	Arguments  json.RawMessage    `json:"arguments"`
	Args       json.RawMessage    `json:"args"`
}

type chatBlockFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

var chatTrailingCommaPattern = regexp.MustCompile(`,(\s*[}\]])`)

// parseResponse 非流式解析，对齐 _default_normal_response_parser
func (c *chatCompletionsClient) parseResponse(raw []byte, maxTokens *int) (*schema.AgenticMessage, error) {
	var response chatCompletionResponse
	if err := sonic.Unmarshal(raw, &response); err != nil {
		return nil, &ParseError{Message: "解析 OpenAI 兼容响应失败", Err: err}
	}
	if len(response.Choices) == 0 {
		return nil, &EmptyResponseError{Message: "响应解析失败，choices 为空或缺失"}
	}
	choice := response.Choices[0]
	if choice.Message == nil {
		return nil, &EmptyResponseError{Message: "响应解析失败，choices 为空或缺失"}
	}
	message := choice.Message

	messageContent, hasMessageContent := chatRawString(message.Content)
	var blockReasoning, blockContent string
	var blockCalls []*schema.FunctionToolCall
	if !hasMessageContent {
		if blocks, ok := chatRawContentBlocks(message.Content); ok {
			parsedReasoning, parsedContent, parsedCalls, err := c.parseContentBlocks(blocks)
			if err != nil {
				return nil, err
			}
			blockReasoning, blockContent, blockCalls = parsedReasoning, parsedContent, parsedCalls
		}
	}

	nativeReasoning := c.nativeReasoning(message.ReasoningContent, message.Reasoning)
	reasoning := ""
	content := ""
	switch {
	case nativeReasoning != "" && c.reasoningParseMode != chatReasoningModeNone:
		reasoning = nativeReasoning
		if hasMessageContent && messageContent != "" {
			content = messageContent
		} else {
			content = blockContent
		}
	case hasMessageContent && messageContent != "":
		reasoning, content = chatExtractReasoningAndContent(messageContent, c.reasoningParseMode)
	default:
		reasoning, content = blockReasoning, blockContent
	}

	calls, err := c.parseStandardToolCalls(message.ToolCalls)
	if err != nil {
		return nil, err
	}
	// 标准 tool_calls 的来源只看是否带原生推理；content block 的来源看推理是否独占
	source := chatStandardToolCallSource(reasoning)
	if len(calls) == 0 {
		calls = blockCalls
		if len(blockCalls) > 0 {
			source = chatBlockToolCallSource(blockReasoning, blockContent)
		}
	}
	chatWarnMaxTokens(c.model, choice.FinishReason, content, len(calls), maxTokens)

	hasReplay := c.reasoningScope != nil && nativeReasoning != ""
	if content == "" && reasoning == "" && len(calls) == 0 && !hasReplay {
		return nil, &EmptyResponseError{}
	}
	blocks := make([]*schema.ContentBlock, 0, 2+len(calls))
	blocks = append(blocks, chatReasoningBlocks(reasoning)...)
	blocks = append(blocks, chatTextBlocks(content)...)
	for _, call := range calls {
		blocks = append(blocks, chatToolCallBlock(call, source))
	}
	blocks = attachChatReasoningReplay(blocks, c.reasoningScope, nativeReasoning)
	return &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeAssistant,
		ContentBlocks: blocks,
		ResponseMeta:  chatResponseMeta(response.Usage),
	}, nil
}

// nativeReasoning 对齐 _extract_reasoning_content：先读 reasoning_key，再回退 reasoning
func (c *chatCompletionsClient) nativeReasoning(reasoningContent, reasoning json.RawMessage) string {
	first := reasoningContent
	if c.reasoningKey == "reasoning" {
		first = reasoning
	}
	if value, ok := chatRawString(first); ok && value != "" {
		return value
	}
	if value, ok := chatRawString(reasoning); ok {
		return value
	}
	return ""
}

// parseContentBlocks 对齐 _extract_content_blocks
func (c *chatCompletionsClient) parseContentBlocks(blocks []chatContentBlock) (string, string, []*schema.FunctionToolCall, error) {
	reasoningParts := make([]string, 0, len(blocks))
	contentParts := make([]string, 0, len(blocks))
	calls := make([]*schema.FunctionToolCall, 0)
	for _, block := range blocks {
		blockType := strings.ToLower(strings.TrimSpace(block.Type))
		switch blockType {
		case "reasoning", "reasoning_content", "thinking", "thought":
			if text := chatFirstBlockText(block.Text, block.Content, block.Reasoning, block.Summary); text != "" {
				reasoningParts = append(reasoningParts, text)
			}
		case "tool_use", "tool_call", "function_call":
			name := block.callName()
			if name == "" {
				return "", "", nil, &ParseError{Message: "响应解析失败，content block 工具调用缺少 name 字段。"}
			}
			parsed, err := parseChatToolArguments(block.callArguments(), c.toolArgumentParseMode)
			if err != nil {
				return "", "", nil, err
			}
			callID := strings.TrimSpace(block.ID)
			if callID == "" {
				callID = strings.TrimSpace(block.CallID)
			}
			if callID == "" {
				callID = chatGeneratedToolCallID(len(calls))
			}
			calls = append(calls, &schema.FunctionToolCall{
				CallID:    callID,
				Name:      name,
				Arguments: chatMarshalArguments(parsed),
			})
		default:
			if text := chatFirstBlockText(block.Text, block.Content, block.OutputText); text != "" {
				contentParts = append(contentParts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(reasoningParts, "\n")),
		strings.TrimSpace(strings.Join(contentParts, "\n")),
		calls, nil
}

func (block chatContentBlock) callName() string {
	if block.Function != nil && strings.TrimSpace(block.Function.Name) != "" {
		return strings.TrimSpace(block.Function.Name)
	}
	if strings.TrimSpace(block.Name) != "" {
		return strings.TrimSpace(block.Name)
	}
	return strings.TrimSpace(block.FuncName)
}

func (block chatContentBlock) callArguments() string {
	if block.Function != nil && len(bytes.TrimSpace(block.Function.Arguments)) > 0 {
		return chatRawArgumentsText(block.Function.Arguments)
	}
	for _, candidate := range []json.RawMessage{block.Input, block.Arguments, block.Args} {
		if len(bytes.TrimSpace(candidate)) > 0 {
			return chatRawArgumentsText(candidate)
		}
	}
	return ""
}

// chatFirstBlockText 取多个候选字段中第一个非空文本，并按 MaiBot 规则 strip
func chatFirstBlockText(candidates ...json.RawMessage) string {
	for _, candidate := range candidates {
		if text := strings.TrimSpace(chatRawText(candidate)); text != "" {
			return text
		}
	}
	return ""
}

// chatRawText 把字符串或嵌套对象里的文本字段取出
func chatRawText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err == nil {
			return text
		}
		return ""
	}
	if trimmed[0] == '{' {
		var nested struct {
			Text      json.RawMessage `json:"text"`
			Content   json.RawMessage `json:"content"`
			Reasoning json.RawMessage `json:"reasoning"`
			Summary   json.RawMessage `json:"summary"`
		}
		if err := json.Unmarshal(trimmed, &nested); err != nil {
			return ""
		}
		for _, candidate := range []json.RawMessage{nested.Text, nested.Content, nested.Reasoning, nested.Summary} {
			if text := chatRawText(candidate); text != "" {
				return text
			}
		}
	}
	return ""
}

func chatRawString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return "", false
	}
	return text, true
}

func chatRawContentBlocks(raw json.RawMessage) ([]chatContentBlock, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var blocks []chatContentBlock
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}

func chatRawArgumentsText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err == nil {
			return text
		}
	}
	return string(trimmed)
}

// chatExtractReasoningAndContent 对齐 _extract_reasoning_and_content（re.match 语义）
func chatExtractReasoningAndContent(content, mode string) (string, string) {
	if mode == chatReasoningModeNative || mode == chatReasoningModeNone {
		return "", content
	}
	openTag := "<think>"
	closeTag := "</think>"
	if strings.HasPrefix(content, openTag) {
		if index := strings.Index(content, closeTag); index >= 0 {
			reasoning := strings.TrimSpace(content[len(openTag):index])
			remaining := strings.TrimSpace(content[index+len(closeTag):])
			return reasoning, remaining
		}
		return strings.TrimSpace(content[len(openTag):]), ""
	}
	return "", strings.TrimSpace(content)
}

// parseStandardToolCalls 解析非流式 tool_calls
func (c *chatCompletionsClient) parseStandardToolCalls(toolCalls []chatToolCall) ([]*schema.FunctionToolCall, error) {
	calls := make([]*schema.FunctionToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		callType := strings.TrimSpace(toolCall.Type)
		if callType != "" && callType != "function" {
			return nil, &ParseError{Message: fmt.Sprintf("响应解析失败，暂不支持工具调用类型 %s。", callType)}
		}
		name := ""
		if toolCall.Function != nil {
			name = strings.TrimSpace(toolCall.Function.Name)
		}
		if name == "" {
			return nil, &ParseError{Message: "响应解析失败，工具调用缺少函数名。"}
		}
		arguments := ""
		if toolCall.Function != nil {
			arguments = chatRawArgumentsText(toolCall.Function.Arguments)
		}
		parsed, err := parseChatToolArguments(arguments, c.toolArgumentParseMode)
		if err != nil {
			return nil, err
		}
		callID := strings.TrimSpace(toolCall.ID)
		if callID == "" {
			callID = chatGeneratedToolCallID(len(calls))
		}
		calls = append(calls, &schema.FunctionToolCall{
			CallID:    callID,
			Name:      name,
			Arguments: chatMarshalArguments(parsed),
		})
	}
	return calls, nil
}

// parseChatToolArguments 对齐 _parse_tool_arguments。
//
// strict 使用 encoding/json（等价 json.loads）；repair 走 json_repair；
// auto/double_decode 在修复结果为字符串时再修复一次，与 Python 的行为一致。
func parseChatToolArguments(raw, mode string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var (
		value any
		err   error
	)
	if mode == chatToolArgModeStrict {
		value, err = chatDecodeToolArgumentValue(trimmed)
		if err != nil {
			return nil, &ParseError{Message: "响应解析失败，无法解析工具调用参数。原始参数：" + raw}
		}
	} else {
		value, err = chatRepairJSON(llmjson.Normalize(trimmed))
		if err != nil {
			return nil, &ParseError{Message: "响应解析失败，无法解析工具调用参数。原始参数：" + raw}
		}
		if mode == chatToolArgModeAuto || mode == chatToolArgModeDoubleDecode {
			if text, ok := value.(string); ok {
				value, err = chatRepairJSON(llmjson.Normalize(text))
				if err != nil {
					return nil, &ParseError{Message: "响应解析失败，无法解析工具调用参数。原始参数：" + raw}
				}
			}
		}
	}
	parsed, ok := value.(map[string]any)
	if !ok {
		return nil, &ParseError{Message: fmt.Sprintf("响应解析失败，工具调用参数必须解析为字典，实际类型为 %s。", chatPythonTypeName(value))}
	}
	return parsed, nil
}

func chatDecodeToolArgumentValue(raw string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func chatPythonTypeName(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if strings.ContainsAny(string(typed), ".eE") {
			return "float"
		}
		return "int"
	case float64:
		return "float"
	case []any:
		return "list"
	case int64:
		return "int"
	case int:
		return "int"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func chatMarshalArguments(arguments map[string]any) string {
	encoded, err := sonic.Marshal(arguments)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func chatGeneratedToolCallID(index int) string {
	return fmt.Sprintf("tool_call_%d_%s", index, strings.ReplaceAll(uuid.NewString(), "-", ""))
}

// chatStandardToolCallSource 标准 tool_calls 的来源：带原生推理即视为推理中调用
func chatStandardToolCallSource(reasoning string) string {
	if strings.TrimSpace(reasoning) != "" {
		return toolCallSourceReasoning
	}
	return toolCallSourceResponse
}

// chatBlockToolCallSource content block 工具调用的来源：推理独占且无正文时才算推理中调用
func chatBlockToolCallSource(reasoning, content string) string {
	if strings.TrimSpace(reasoning) != "" && strings.TrimSpace(content) == "" {
		return toolCallSourceReasoning
	}
	return toolCallSourceResponse
}

// chatToolCallBlock 构造带来源标记的工具调用块，对齐 Python extra_content
func chatToolCallBlock(call *schema.FunctionToolCall, source string) *schema.ContentBlock {
	block := schema.NewContentBlock(call)
	if source == "" {
		return block
	}
	block.Extra = map[string]any{toolCallSourceExtraKey: source}
	return block
}

// chatTokenUsage 对齐 _extract_usage_record 的缓存命中计算
func chatTokenUsage(usage *chatUsage) *schema.TokenUsage {
	if usage == nil {
		return nil
	}
	hit := 0
	reportedHit := false
	if usage.PromptCacheHitTokens != nil {
		hit = *usage.PromptCacheHitTokens
		reportedHit = true
	}
	if !reportedHit && usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens != nil {
		hit = *usage.PromptTokensDetails.CachedTokens
	}
	return &schema.TokenUsage{
		PromptTokens:       usage.PromptTokens,
		CompletionTokens:   usage.CompletionTokens,
		TotalTokens:        usage.TotalTokens,
		PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: hit},
	}
}

func chatResponseMeta(usage *chatUsage) *schema.AgenticResponseMeta {
	tokenUsage := chatTokenUsage(usage)
	if tokenUsage == nil {
		return nil
	}
	return &schema.AgenticResponseMeta{TokenUsage: tokenUsage}
}

func chatWarnMaxTokens(modelName, finishReason, content string, toolCallCount int, maxTokens *int) {
	if finishReason != "length" {
		return
	}
	fields := []zap.Field{zap.String("model", modelName)}
	if maxTokens != nil {
		fields = append(fields, zap.Int("max_tokens", *maxTokens))
	}
	fields = append(fields, zap.Int("tool_calls", toolCallCount), zap.Bool("has_content", strings.TrimSpace(content) != ""))
	zap.L().Info("模型响应因达到最大输出 token 限制被截断，可能仅输出部分内容", fields...)
}

func chatTextBlocks(text string) []*schema.ContentBlock {
	if text == "" {
		return nil
	}
	return []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: text})}
}

func chatReasoningBlocks(text string) []*schema.ContentBlock {
	if text == "" {
		return nil
	}
	return []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Text: text})}
}

const chatReasoningReplayExtraKey = "provider_reasoning_replay"

// chatReasoningReplayExtra 记录原样的 Provider 推理字段，供同一范围后续请求回放
func chatReasoningReplayExtra(scope *ContextProviderScope, nativeReasoning string) map[string]any {
	if scope == nil || nativeReasoning == "" {
		return nil
	}
	return map[string]any{
		"scope": map[string]any{
			"schema_version":       scope.SchemaVersion,
			"client_type":          scope.ClientType,
			"provider_name":        scope.ProviderName,
			"endpoint_fingerprint": scope.EndpointFingerprint,
			"model_identifier":     scope.ModelIdentifier,
		},
		"payload": map[string]any{"reasoning_content": nativeReasoning},
	}
}

// attachChatReasoningReplay 把回放片段附着到 reasoning block；隐藏推理时插入不透明项
func attachChatReasoningReplay(blocks []*schema.ContentBlock, scope *ContextProviderScope, nativeReasoning string) []*schema.ContentBlock {
	extra := chatReasoningReplayExtra(scope, nativeReasoning)
	if extra == nil {
		return blocks
	}
	for _, block := range blocks {
		if block == nil || block.Reasoning == nil {
			continue
		}
		if block.Extra == nil {
			block.Extra = map[string]any{}
		}
		block.Extra[chatReasoningReplayExtraKey] = extra
		return blocks
	}
	opaque := schema.NewContentBlock(&schema.Reasoning{})
	opaque.Extra = map[string]any{chatReasoningReplayExtraKey: extra}
	return append([]*schema.ContentBlock{opaque}, blocks...)
}
