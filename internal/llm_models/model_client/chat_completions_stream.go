package model_client

// Chat Completions 流式累积：SSE delta 增量文本、<think> 前缀状态机、工具调用分片拼接
//
// 对等 MaiBot src/llm_models/model_client/openai_client.py 的 _OpenAIStreamAccumulator。
// 与 Python 一致，文本和 reasoning 全程累积，只在流结束构建一次响应；
// 文本工具调用恢复在结束构建时执行，避免已下发的调用块无法回收。

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/cloudwego/eino/schema"
)

// ---------- 流式累积 ----------

// chatStreamText 增量文本：去掉首部空白，尾部空白暂存到确认不是结尾再下发
type chatStreamText struct {
	builder strings.Builder
	pending strings.Builder
	started bool
}

func (t *chatStreamText) push(text string, flush bool) string {
	if text != "" {
		t.builder.WriteString(text)
		t.pending.WriteString(text)
	}
	if text == "" && !flush {
		return ""
	}
	raw := t.pending.String()
	t.pending.Reset()
	if !t.started {
		raw = strings.TrimLeftFunc(raw, unicode.IsSpace)
		if raw == "" {
			return ""
		}
		t.started = true
	}
	trimmed := strings.TrimRightFunc(raw, unicode.IsSpace)
	if trimmed != "" {
		if !flush {
			t.pending.WriteString(raw[len(trimmed):])
		}
		return trimmed
	}
	if !flush {
		t.pending.WriteString(raw)
	}
	return ""
}

func (t *chatStreamText) value() string {
	return strings.TrimSpace(t.builder.String())
}

type chatThinkState int

const (
	chatThinkUnknown chatThinkState = iota
	chatThinkReasoning
	chatThinkContent
	chatThinkDone
)

type chatToolCallState struct {
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
}

// chatCompletionsStreamBuilder SSE 增量累积器
type chatCompletionsStreamBuilder struct {
	client               *chatCompletionsClient
	mode                 string
	toolArgMode          string
	maxTokens            *int
	model                string
	finishReason         string
	usage                *chatUsage
	content              chatStreamText
	reasoning            chatStreamText
	nativeReasoning      strings.Builder
	think                chatThinkState
	thinkBuffer          strings.Builder
	usingNativeReasoning bool
	// rawContent 记录未经过 <think> 解析的正文原文，供打断快照对齐 Python content_buffer
	rawContent strings.Builder
	tools      map[int]*chatToolCallState
	// tools/toolChoice 用于流结束后恢复正文中的文本工具调用
	toolInfos  []*schema.ToolInfo
	toolChoice string
	// occupiedCallIDs 请求上下文中已占用的调用 ID
	occupiedCallIDs []string
	// requestID 本次流式响应的上游请求编号，供流内错误事件对账
	requestID string
}

func newChatCompletionsStreamBuilder(client *chatCompletionsClient, maxTokens *int, toolInfos []*schema.ToolInfo, toolChoice string, occupiedCallIDs []string, requestID string) *chatCompletionsStreamBuilder {
	return &chatCompletionsStreamBuilder{
		client:          client,
		mode:            client.reasoningParseMode,
		toolArgMode:     client.toolArgumentParseMode,
		maxTokens:       maxTokens,
		tools:           map[int]*chatToolCallState{},
		toolInfos:       toolInfos,
		toolChoice:      toolChoice,
		occupiedCallIDs: occupiedCallIDs,
		requestID:       requestID,
	}
}

func (b *chatCompletionsStreamBuilder) consume(chunk chatCompletionChunk) (*schema.AgenticMessage, bool, error) {
	if chunk.decodeErr != nil {
		return nil, false, &ParseError{Message: "解析 OpenAI 兼容流式响应失败", Err: chunk.decodeErr}
	}
	if chunk.Error != nil {
		return nil, false, chatCompletionInBandError(*chunk.Error, b.requestID)
	}
	if b.model == "" && chunk.Model != "" {
		b.model = chunk.Model
	}
	if chunk.Usage != nil {
		b.usage = chunk.Usage
	}
	if len(chunk.Choices) > 0 {
		choice := chunk.Choices[0]
		if b.finishReason == "" && choice.FinishReason != "" {
			b.finishReason = choice.FinishReason
		}
		if choice.Delta != nil {
			native := b.client.nativeReasoning(choice.Delta.ReasoningContent, choice.Delta.Reasoning)
			if native != "" {
				b.usingNativeReasoning = true
				b.nativeReasoning.WriteString(native)
				if b.mode != chatReasoningModeNone {
					b.reasoning.push(native, false)
				}
			} else if text, ok := chatRawString(choice.Delta.Content); ok && text != "" {
				b.rawContent.WriteString(text)
				b.addContent(text)
			}
			for _, toolCall := range choice.Delta.ToolCalls {
				b.accumulateToolCall(toolCall)
			}
		}
	}
	return nil, false, nil
}

func (b *chatCompletionsStreamBuilder) addContent(text string) []*schema.ContentBlock {
	if text == "" {
		return nil
	}
	if b.mode == chatReasoningModeNone || b.mode == chatReasoningModeNative || b.usingNativeReasoning {
		return chatTextBlocks(b.content.push(text, false))
	}
	switch b.think {
	case chatThinkContent, chatThinkDone:
		return chatTextBlocks(b.content.push(text, false))
	case chatThinkReasoning:
		b.thinkBuffer.WriteString(text)
		return b.drainReasoning()
	default:
		b.thinkBuffer.WriteString(text)
		return b.drainUnknown()
	}
}

func (b *chatCompletionsStreamBuilder) drainUnknown() []*schema.ContentBlock {
	trimmed := strings.TrimLeftFunc(b.thinkBuffer.String(), unicode.IsSpace)
	if trimmed == "" {
		b.thinkBuffer.Reset()
		return nil
	}
	const openTag = "<think>"
	if strings.HasPrefix(trimmed, openTag) {
		b.thinkBuffer.Reset()
		b.think = chatThinkReasoning
		b.thinkBuffer.WriteString(trimmed[len(openTag):])
		return b.drainReasoning()
	}
	if strings.HasPrefix(openTag, trimmed) {
		b.thinkBuffer.Reset()
		b.thinkBuffer.WriteString(trimmed)
		return nil
	}
	b.thinkBuffer.Reset()
	b.think = chatThinkContent
	return chatTextBlocks(b.content.push(trimmed, false))
}

func (b *chatCompletionsStreamBuilder) drainReasoning() []*schema.ContentBlock {
	buffer := b.thinkBuffer.String()
	const closeTag = "</think>"
	if index := strings.Index(buffer, closeTag); index >= 0 {
		reasoningPart := buffer[:index]
		rest := buffer[index+len(closeTag):]
		b.thinkBuffer.Reset()
		b.think = chatThinkDone
		blocks := chatReasoningBlocks(b.reasoning.push(reasoningPart, false))
		return append(blocks, chatTextBlocks(b.content.push(rest, false))...)
	}
	hold := chatPartialSuffixLength(buffer, closeTag)
	b.thinkBuffer.Reset()
	b.thinkBuffer.WriteString(buffer[len(buffer)-hold:])
	return chatReasoningBlocks(b.reasoning.push(buffer[:len(buffer)-hold], false))
}

func chatPartialSuffixLength(text, marker string) int {
	maximum := len(marker) - 1
	if len(text) < maximum {
		maximum = len(text)
	}
	for length := maximum; length > 0; length-- {
		if strings.HasSuffix(text, marker[:length]) {
			return length
		}
	}
	return 0
}

func (b *chatCompletionsStreamBuilder) accumulateToolCall(toolCall chatToolCall) {
	index := 0
	if toolCall.Index != nil {
		index = *toolCall.Index
	}
	state := b.tools[index]
	if state == nil {
		state = &chatToolCallState{}
		b.tools[index] = state
	}
	state.id.WriteString(toolCall.ID)
	if toolCall.Function != nil {
		state.name.WriteString(toolCall.Function.Name)
		if arguments, ok := chatRawString(toolCall.Function.Arguments); ok {
			state.arguments.WriteString(arguments)
		}
	}
}

// snapshotInterruptedOutput 提取已累积的原始片段，供打断后保留未完成输出
//
// 对齐 Python _OpenAIStreamAccumulator.snapshot_interrupted_output：
// 只取缓冲区原文，不解析半截工具参数；<think> 标签保持原样留在 content。
func (b *chatCompletionsStreamBuilder) snapshotInterruptedOutput() StreamPartialOutput {
	indices := make([]int, 0, len(b.tools))
	for index := range b.tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]StreamPartialToolCall, 0, len(indices))
	for _, index := range indices {
		state := b.tools[index]
		calls = append(calls, StreamPartialToolCall{
			Name:      state.name.String(),
			Arguments: state.arguments.String(),
		})
	}
	reasoning := ""
	if b.mode != chatReasoningModeNone && (b.mode == chatReasoningModeNative || b.usingNativeReasoning) {
		reasoning = b.reasoning.builder.String()
	}
	return StreamPartialOutput{
		Reasoning: reasoning,
		Content:   b.rawContent.String(),
		ToolCalls: calls,
		ModelName: b.model,
	}
}

func (b *chatCompletionsStreamBuilder) finish() (*schema.AgenticMessage, bool, error) {
	switch b.think {
	case chatThinkUnknown:
		trimmed := strings.TrimLeftFunc(b.thinkBuffer.String(), unicode.IsSpace)
		b.thinkBuffer.Reset()
		const openTag = "<think>"
		if strings.HasPrefix(trimmed, openTag) {
			b.reasoning.push(trimmed[len(openTag):], true)
		} else {
			b.content.push(trimmed, false)
		}
	case chatThinkReasoning:
		b.reasoning.push(b.thinkBuffer.String(), true)
		b.thinkBuffer.Reset()
	}
	b.reasoning.push("", true)
	b.content.push("", true)

	content := b.content.value()
	reasoning := b.reasoning.value()
	calls, err := b.buildToolCalls()
	if err != nil {
		return nil, false, err
	}
	source := chatStandardToolCallSource(reasoning)
	chatWarnMaxTokens(b.model, b.finishReason, content, len(calls), b.maxTokens)
	hasReplay := b.client.reasoningScope != nil && b.nativeReasoning.Len() > 0
	if content == "" && reasoning == "" && len(calls) == 0 && !hasReplay {
		return nil, false, &EmptyResponseError{}
	}
	blocks := make([]*schema.ContentBlock, 0, 2+len(calls))
	blocks = append(blocks, chatReasoningBlocks(reasoning)...)
	blocks = append(blocks, chatTextBlocks(content)...)
	for _, call := range calls {
		blocks = append(blocks, chatToolCallBlock(call, source))
	}
	blocks = attachChatReasoningReplay(blocks, b.client.reasoningScope, b.nativeReasoning.String())
	if len(blocks) == 0 && b.usage == nil {
		return nil, false, nil
	}
	message, err := normalizeTextToolCalls(b.message(blocks), b.toolInfos, b.toolChoice, b.occupiedCallIDs)
	if err != nil {
		return nil, false, &ParseError{Message: err.Error(), Err: err}
	}
	return message, true, nil
}

func (b *chatCompletionsStreamBuilder) buildToolCalls() ([]*schema.FunctionToolCall, error) {
	indices := make([]int, 0, len(b.tools))
	for index := range b.tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]*schema.FunctionToolCall, 0, len(indices))
	for _, index := range indices {
		state := b.tools[index]
		name := strings.TrimSpace(state.name.String())
		if name == "" {
			return nil, &ParseError{Message: fmt.Sprintf("响应解析失败，工具调用 %d 缺少函数名。", index)}
		}
		arguments, err := parseChatToolArguments(state.arguments.String(), b.toolArgMode)
		if err != nil {
			return nil, err
		}
		callID := strings.TrimSpace(state.id.String())
		if callID == "" {
			callID = chatGeneratedToolCallID(index)
		}
		calls = append(calls, &schema.FunctionToolCall{
			CallID:    callID,
			Name:      name,
			Arguments: chatMarshalArguments(arguments),
		})
	}
	return calls, nil
}

func (b *chatCompletionsStreamBuilder) message(blocks []*schema.ContentBlock) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeAssistant,
		ContentBlocks: blocks,
		ResponseMeta:  chatResponseMeta(b.usage),
	}
}
