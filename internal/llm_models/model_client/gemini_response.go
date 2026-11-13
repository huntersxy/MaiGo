package model_client

// Gemini REST 响应与流式分块的解析
// 对齐 MaiBot _default_normal_response_parser / _default_stream_response_handler / _collect_function_calls

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/schema/gemini"
)

// geminiBlob inlineData/文件数据
type geminiBlob struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
	FileURI  string `json:"fileUri"`
}

// geminiFunctionCall 响应中的函数调用
type geminiFunctionCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// geminiPart 响应内容片段
type geminiPart struct {
	Text             string              `json:"text"`
	Thought          bool                `json:"thought"`
	ThoughtSignature string              `json:"thoughtSignature"`
	FunctionCall     *geminiFunctionCall `json:"functionCall"`
	InlineData       *geminiBlob         `json:"inlineData"`
}

// geminiCandidate 单个候选
type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

// geminiContent 候选内容
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

// geminiUsageMetadata 用量元数据
type geminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

// geminiAPIError REST 错误体
type geminiAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// geminiStreamChunk 既表示非流式响应，也表示单个 SSE 分块
type geminiStreamChunk struct {
	Candidates     []geminiCandidate    `json:"candidates"`
	UsageMetadata  *geminiUsageMetadata `json:"usageMetadata"`
	Error          *geminiAPIError      `json:"error"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	// decodeErr SSE 分块无法解析时的错误，供 pump 上抛
	decodeErr error
}

// parseGeminiResponse 解析非流式响应体
func parseGeminiResponse(raw []byte) (geminiStreamChunk, error) {
	var chunk geminiStreamChunk
	if err := sonic.Unmarshal(raw, &chunk); err != nil {
		return chunk, &ParseError{Message: "解析 Gemini 响应失败", Err: err}
	}
	if chunk.Error != nil && chunk.Error.Message != "" {
		return chunk, geminiInBandError(*chunk.Error)
	}
	return chunk, nil
}

// geminiInBandError HTTP 200 但响应体带 error 的兜底分类
func geminiInBandError(apiErr geminiAPIError) error {
	status := apiErr.Code
	if status < 400 {
		status = 500
	}
	return &HTTPStatusError{StatusCode: status, Message: apiErr.Message, Op: "gemini"}
}

// toAgenticMessage 把非流式响应转换成 eino 消息
// 顺序对齐 MaiBot build_portable_output_items：reasoning → content → tool_calls
func (c geminiStreamChunk) toAgenticMessage() (*schema.AgenticMessage, error) {
	visible, reasoning, calls, signatures, finishReason, err := c.collect()
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(visible)
	reasoningText := strings.TrimSpace(reasoning)
	warnGeminiMaxTokens(finishReason, content, len(calls))
	if content == "" && reasoningText == "" && len(calls) == 0 {
		return nil, &EmptyResponseError{Message: "响应中既无文本内容也无工具调用"}
	}

	blocks := make([]*schema.ContentBlock, 0, 2+len(calls))
	if reasoningText != "" {
		blocks = append(blocks, schema.NewContentBlock(&schema.Reasoning{Text: reasoningText}))
	}
	if content != "" {
		blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: content}))
	}
	for _, call := range calls {
		blocks = append(blocks, geminiToolCallBlock(call, signatures[call.CallID]))
	}
	return &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeAssistant,
		ContentBlocks: blocks,
		ResponseMeta:  geminiResponseMeta(c.UsageMetadata, finishReason),
		Extra:         geminiMessageSignatureExtra(signatures),
	}, nil
}

// collect 汇总所有候选的可见文本、思考文本、工具调用与 thoughtSignature 索引
func (c geminiStreamChunk) collect() (string, string, []*schema.FunctionToolCall, map[string]string, string, error) {
	var visible strings.Builder
	var reasoning strings.Builder
	signatures := make(map[string]string)
	var calls []*schema.FunctionToolCall
	finishReason := ""
	for _, candidate := range c.Candidates {
		if finishReason == "" && candidate.FinishReason != "" {
			finishReason = candidate.FinishReason
		}
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				if part.Thought {
					reasoning.WriteString(part.Text)
				} else {
					visible.WriteString(part.Text)
				}
			}
			if part.FunctionCall == nil {
				continue
			}
			call, err := geminiToolCall(part, len(calls)+1)
			if err != nil {
				return "", "", nil, nil, "", err
			}
			calls = append(calls, call)
			if signature := decodeThoughtSignature(part.ThoughtSignature); signature != "" {
				signatures[call.CallID] = signature
			}
		}
	}
	return visible.String(), reasoning.String(), calls, signatures, finishReason, nil
}

// geminiToolCall 由响应片段构造工具调用
func geminiToolCall(part geminiPart, position int) (*schema.FunctionToolCall, error) {
	fc := part.FunctionCall
	name := strings.TrimSpace(fc.Name)
	if name == "" {
		// 对齐 MaiBot：缺少 name 视为响应解析失败
		return nil, &ParseError{Message: "响应解析失败，Gemini 工具调用缺少 name 字段"}
	}
	callID := strings.TrimSpace(fc.ID)
	if callID == "" {
		callID = fmt.Sprintf("gemini-tool-call-%d", position)
	}
	argsJSON := "{}"
	if len(fc.Args) > 0 {
		encoded, err := sonic.MarshalString(fc.Args)
		if err != nil {
			return nil, &ParseError{Message: "响应解析失败，Gemini 工具调用参数无法序列化", Err: err}
		}
		argsJSON = encoded
	}
	return &schema.FunctionToolCall{CallID: callID, Name: name, Arguments: argsJSON}, nil
}

// geminiToolCallBlock 工具调用块，thoughtSignature 存到块 Extra 供回放使用
func geminiToolCallBlock(call *schema.FunctionToolCall, signature string) *schema.ContentBlock {
	block := schema.NewContentBlock(call)
	if signature != "" {
		block.Extra = map[string]any{
			geminiExtraProviderKey: map[string]any{geminiExtraThoughtSignatureKey: signature},
		}
	}
	return block
}

// geminiResponseMeta 构造响应元信息（用量 + finishReason）
func geminiResponseMeta(usage *geminiUsageMetadata, finishReason string) *schema.AgenticResponseMeta {
	tokenUsage := usageFromGemini(usage)
	if tokenUsage == nil && finishReason == "" {
		return nil
	}
	return &schema.AgenticResponseMeta{
		TokenUsage:      tokenUsage,
		GeminiExtension: &gemini.ResponseMetaExtension{FinishReason: finishReason},
	}
}

// usageFromGemini 对齐 MaiBot _extract_usage_record
// completion = candidates_token_count + thoughts_token_count
func usageFromGemini(meta *geminiUsageMetadata) *schema.TokenUsage {
	if meta == nil {
		return nil
	}
	if meta.TotalTokenCount == 0 && meta.PromptTokenCount == 0 && meta.CandidatesTokenCount == 0 && meta.ThoughtsTokenCount == 0 {
		return nil
	}
	return &schema.TokenUsage{
		PromptTokens:     meta.PromptTokenCount,
		CompletionTokens: meta.CandidatesTokenCount + meta.ThoughtsTokenCount,
		TotalTokens:      meta.TotalTokenCount,
	}
}

// geminiStreamBuilder 汇总流式分块，构造增量消息
type geminiStreamBuilder struct {
	content      strings.Builder
	reasoning    strings.Builder
	calls        []*schema.FunctionToolCall
	finishReason string
	usage        *geminiUsageMetadata
}

func newGeminiStreamBuilder() *geminiStreamBuilder {
	return &geminiStreamBuilder{}
}

// consume 处理单个 SSE 分块；返回本块新增的增量消息
func (b *geminiStreamBuilder) consume(chunk geminiStreamChunk) (*schema.AgenticMessage, bool, error) {
	if chunk.decodeErr != nil {
		return nil, false, &ParseError{Message: "解析 Gemini 流式分块失败", Err: chunk.decodeErr}
	}
	if chunk.Error != nil && chunk.Error.Message != "" {
		return nil, false, geminiInBandError(*chunk.Error)
	}
	if chunk.UsageMetadata != nil {
		b.usage = chunk.UsageMetadata
	}

	var blocks []*schema.ContentBlock
	signatures := make(map[string]string)
	chunkHasFinish := false
	for _, candidate := range chunk.Candidates {
		if candidate.FinishReason != "" {
			b.finishReason = candidate.FinishReason
			chunkHasFinish = true
		}
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				if part.Thought {
					b.reasoning.WriteString(part.Text)
					blocks = append(blocks, schema.NewContentBlock(&schema.Reasoning{Text: part.Text}))
				} else {
					b.content.WriteString(part.Text)
					blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: part.Text}))
				}
			}
			if part.FunctionCall == nil {
				continue
			}
			call, err := geminiToolCall(part, len(b.calls)+1)
			if err != nil {
				return nil, false, err
			}
			b.calls = append(b.calls, call)
			signature := decodeThoughtSignature(part.ThoughtSignature)
			if signature != "" {
				signatures[call.CallID] = signature
			}
			blocks = append(blocks, geminiToolCallBlock(call, signature))
		}
	}

	// 只有 finishReason 的末块也要下发，否则 eino 流拿不到结束原因
	if len(blocks) == 0 && chunk.UsageMetadata == nil && !chunkHasFinish {
		return nil, false, nil
	}
	metaUsage := b.usage
	if len(blocks) == 0 && chunk.UsageMetadata == nil {
		metaUsage = nil
	}
	message := &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeAssistant,
		ContentBlocks: blocks,
		ResponseMeta:  geminiResponseMeta(metaUsage, b.finishReason),
		Extra:         geminiMessageSignatureExtra(signatures),
	}
	return message, true, nil
}

// finish 流结束校验：完全空响应按 MaiBot 语义报 EmptyResponseError
func (b *geminiStreamBuilder) finish() error {
	content := strings.TrimSpace(b.content.String())
	reasoning := strings.TrimSpace(b.reasoning.String())
	warnGeminiMaxTokens(b.finishReason, content, len(b.calls))
	if content == "" && reasoning == "" && len(b.calls) == 0 {
		return &EmptyResponseError{Message: "响应中既无文本内容也无工具调用"}
	}
	return nil
}
