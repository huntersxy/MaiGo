package model_client

// OpenAI Responses API 的失败快照，对等 openai_responses_client.py 的失败路径。
//
// provider_request 对齐 snapshot_provider_request（endpoint/method/operation/
// request_kwargs），request_items 等字段沿用 v6。Go 的请求由 eino 构造，
// provider_request 从 Context Items 反序列化重建，不抓取最终 wire body。
import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	// responsesEndpoint OpenAI Responses API 路径
	responsesEndpoint = "/responses"
	// responsesOperation 对齐 MaiBot RESPONSES_OPERATION
	responsesOperation = "responses.create"
)

// responsesSnapshotFailure 保存 Responses 失败请求快照并把路径挂到错误链
func (m *ResponsesResponseFormatModel) snapshotFailure(
	recorder *snapshotAttemptRecorder,
	started time.Time,
	input []*schema.AgenticMessage,
	items []ContextItem,
	options *model.Options,
	plan *responsesRequestPlan,
	stream bool,
	err error,
) error {
	if err == nil {
		return nil
	}
	// 对齐 MaiBot：ReqAbortException 直接抛出不落快照
	var abortErr *AbortError
	if errors.As(err, &abortErr) {
		return err
	}
	providerRequest, buildErr := buildResponsesProviderRequest(m.cfg, items, options, m.extra, plan, stream)
	if buildErr != nil {
		zap.L().Warn("构造 Responses 失败快照请求失败", zap.Error(buildErr))
		providerRequest = responsesMinimalProviderRequest(m.cfg, stream)
	}
	spec := requestSnapshotSpec{
		ClientType:      "openai_responses",
		ProviderName:    strings.TrimSpace(m.cfg.ProviderName),
		BaseURL:         m.baseURL,
		Model:           strings.TrimSpace(m.cfg.Model),
		Operation:       responsesOperation,
		RequestKind:     "response",
		TaskName:        strings.TrimSpace(m.cfg.TaskName),
		SessionID:       strings.TrimSpace(m.cfg.SessionID),
		Input:           input,
		ContextItems:    items,
		Options:         options,
		ProviderRequest: providerRequest,
		InternalRequest: buildResponsesInternalRequest(m.cfg, options, stream),
		Config:          m.cfg,
		Error:           err,
		StartedAt:       started,
	}
	spec.LogicalTurnID = snapshotLogicalTurnID(recorder)
	recordSnapshotAttempt(recorder, &spec, started)
	return captureRequestSnapshot(spec)
}

// buildResponsesProviderRequest 对齐 snapshot_provider_request 的结构
// plan 为 nil（请求计划构造失败）时退回请求参数与函数工具
func buildResponsesProviderRequest(cfg ModelConfig, items []ContextItem, options *model.Options, extra *responsesExtraParams, plan *responsesRequestPlan, stream bool) (map[string]any, error) {
	inputItems, err := buildResponsesInputItems(items)
	if err != nil {
		return nil, err
	}
	extraBody := map[string]any(nil)
	var extraHeaders map[string]string
	var extraQuery map[string]string
	if extra != nil {
		extraBody = nilIfEmptyMap(extra.body)
		extraHeaders = nilIfEmptyStrings(extra.Headers)
		extraQuery = nilIfEmptyStrings(extra.Query)
	}
	tools, err := buildResponsesToolDefinitions(options)
	if err != nil {
		return nil, err
	}
	var textConfig map[string]any
	var maxOutputTokens any
	var temperature any
	if options != nil {
		if options.MaxTokens != nil {
			maxOutputTokens = *options.MaxTokens
		}
		if options.Temperature != nil {
			temperature = *options.Temperature
		}
	}
	if plan != nil {
		extraBody = nilIfEmptyMap(plan.extraBody)
		extraHeaders = nilIfEmptyStrings(plan.headers)
		extraQuery = nilIfEmptyStrings(plan.query)
		tools = plan.tools
		textConfig = plan.text
		maxOutputTokens = plan.maxOutputTokens
		temperature = plan.temperature
	}
	requestKwargs := map[string]any{
		"extra_body":        extraBody,
		"extra_headers":     extraHeaders,
		"extra_query":       extraQuery,
		"input":             inputItems,
		"max_output_tokens": maxOutputTokens,
		"model":             strings.TrimSpace(cfg.Model),
		"store":             false,
		"stream":            stream,
		"temperature":       temperature,
		"text":              textConfig,
		"tools":             tools,
	}
	return map[string]any{
		"base_url":       strings.TrimSpace(cfg.BaseURL),
		"endpoint":       responsesEndpoint,
		"method":         "POST",
		"operation":      responsesOperation,
		"organization":   strings.TrimSpace(cfg.Organization),
		"project":        strings.TrimSpace(cfg.Project),
		"request_kwargs": requestKwargs,
	}, nil
}

// responsesMinimalProviderRequest 请求体构造失败时仍保留可诊断的端点信息
func responsesMinimalProviderRequest(cfg ModelConfig, stream bool) map[string]any {
	return map[string]any{
		"base_url":     strings.TrimSpace(cfg.BaseURL),
		"endpoint":     responsesEndpoint,
		"method":       "POST",
		"operation":    responsesOperation,
		"organization": strings.TrimSpace(cfg.Organization),
		"project":      strings.TrimSpace(cfg.Project),
		"request_kwargs": map[string]any{
			"model":  strings.TrimSpace(cfg.Model),
			"store":  false,
			"stream": stream,
		},
	}
}

// buildResponsesInternalRequest 对齐 serialize_response_request_snapshot 的可重放字段
// context_items 由顶层 request_items 承载，避免同一份消息在快照中重复落盘
func buildResponsesInternalRequest(cfg ModelConfig, options *model.Options, stream bool) map[string]any {
	internal := map[string]any{
		"request_kind": "response",
		"stream":       stream,
		"model":        strings.TrimSpace(cfg.Model),
		"base_url":     normalizeOpenAIBaseURL(cfg.BaseURL),
	}
	if options != nil {
		if options.MaxTokens != nil {
			internal["max_tokens"] = *options.MaxTokens
		}
		if options.Temperature != nil {
			internal["temperature"] = *options.Temperature
		}
	}
	if spec, err := parseResponseFormat(cfg.ResponseFormat); err == nil && spec.Kind != "" {
		internal["response_format"] = responseFormatSnapshotValue(spec)
	}
	return internal
}

// buildResponsesInputItems 对齐 _convert_context_items 的可移植部分
func buildResponsesInputItems(items []ContextItem) ([]any, error) {
	out := make([]any, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case ContextItemSystemMessage:
			text, err := responsesMessageText(item)
			if err != nil {
				return nil, err
			}
			if text != "" {
				out = append(out, map[string]any{"role": "system", "content": text})
			}
		case ContextItemUserMessage:
			content, err := responsesUserContent(item)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"role": "user", "content": content})
		case ContextItemAssistantMessage:
			text, err := responsesMessageText(item)
			if err != nil {
				return nil, err
			}
			if text != "" {
				out = append(out, map[string]any{"role": "assistant", "content": text})
			}
		case ContextItemFunctionCall:
			if item.ToolCall == nil {
				return nil, fmt.Errorf("FunctionCallItem 缺少 tool_call")
			}
			out = append(out, map[string]any{
				"type":      "function_call",
				"call_id":   item.ToolCall.CallID,
				"name":      item.ToolCall.FuncName,
				"arguments": string(item.ToolCall.ArgsJSON()),
			})
		case ContextItemFunctionCallOutput:
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": item.CallID,
				"output":  item.Output,
			})
		case ContextItemReasoning, ContextItemProviderActivity, ContextItemProviderOpaque:
			// 没有可移植的 Responses input 投影
		}
	}
	return out, nil
}

// responsesMessageText 对齐 _convert_text_parts：system/assistant 只允许文本与拒答
func responsesMessageText(item ContextItem) (string, error) {
	var builder strings.Builder
	for _, part := range item.Parts {
		switch part.Type {
		case ContextPartText:
			builder.WriteString(part.Text)
		case ContextPartRefusal:
			builder.WriteString(part.Refusal)
		default:
			return "", fmt.Errorf("%s 消息仅支持文本片段", item.Type)
		}
	}
	return builder.String(), nil
}

// responsesUserContent 对齐 _convert_user_content
func responsesUserContent(item ContextItem) ([]any, error) {
	content := make([]any, 0, len(item.Parts))
	for _, part := range item.Parts {
		switch part.Type {
		case ContextPartText:
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			content = append(content, map[string]any{"type": "input_text", "text": part.Text})
		case ContextPartImage:
			format, encoded, ok := normalizeChatImageData(part.ImageBase64)
			if !ok {
				content = append(content, map[string]any{"type": "input_text", "text": chatImageUnavailableText})
				continue
			}
			content = append(content, map[string]any{
				"type":      "input_image",
				"detail":    "auto",
				"image_url": fmt.Sprintf("data:image/%s;base64,%s", format, encoded),
			})
		default:
			return nil, fmt.Errorf("用户消息不支持的内容片段类型: %s", part.Type)
		}
	}
	return content, nil
}

// buildResponsesToolDefinitions 对齐 _convert_tool_options：Responses 的 function 定义是扁平结构
func buildResponsesToolDefinitions(options *model.Options) ([]any, error) {
	if options == nil || len(options.Tools) == 0 {
		return []any{}, nil
	}
	chatDefinitions, err := BuildChatToolDefinitions(options.Tools)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(chatDefinitions))
	for _, definition := range chatDefinitions {
		function, _ := definition["function"].(map[string]any)
		if function == nil {
			continue
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        function["name"],
			"description": function["description"],
			"parameters":  function["parameters"],
		})
	}
	return out, nil
}

// responseFormatSnapshotValue 对齐 RespFormat.to_dict
func responseFormatSnapshotValue(spec ResponseFormatSpec) map[string]any {
	if spec.Kind == "" {
		return nil
	}
	out := map[string]any{"format_type": spec.Kind}
	if spec.Kind == "json_schema" && spec.Schema != nil {
		out["schema"] = cloneAnyMap(spec.Schema)
	}
	return out
}

// nilIfEmptyMap 快照中的空映射按 null 记录，对齐 Python 的 `x or None`
func nilIfEmptyMap(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	return source
}

// nilIfEmptyStrings 快照中的空请求头/查询参数按 null 记录
func nilIfEmptyStrings(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	return source
}
