package model_client

// Responses API 失败快照的重放构造
// 对齐 MaiBot scripts/replay_llm_request.py 的 response 分支：
//   - 消息只从 request_items 反序列化，provider_request.input 已脱敏不再使用
//   - tools 从 tool_definitions 恢复为 Responses 的扁平 function 定义
//   - request_parameters 提供 model/max_tokens/temperature/response_format 的回退值

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func BuildResponsesReplayRequest(snapshot map[string]any, apiProvider, modelInfo map[string]any, items []ContextItem, apiKey string) (*http.Request, error) {
	provider := replayBody(snapshot["provider_request"])
	kwargs := responsesReplayRequestKwargs(provider)
	body := map[string]any{}
	for key, value := range kwargs {
		if key == "input" || key == "tools" || key == "extra_body" || key == "extra_headers" || key == "extra_query" {
			continue
		}
		body[key] = value
	}
	extraHeaders := replayStringMap(kwargs, "extra_headers")
	extraQuery := replayStringMap(kwargs, "extra_query")
	if extraBody, ok := kwargs["extra_body"].(map[string]any); ok {
		for key, value := range extraBody {
			if _, exists := body[key]; !exists {
				body[key] = value
			}
		}
	}
	parameters := replayRequestParameters(snapshot)
	if model := firstReplayString(replayString(body, "model"), replayString(modelInfo, "model_identifier"), metadataString(snapshot, "model_name")); model != "" {
		body["model"] = model
	}
	if body["model"] == nil || replayString(body, "model") == "" {
		return nil, fmt.Errorf("快照 model_identifier 为空，无法构造 Responses 重放请求")
	}
	input, err := buildResponsesInputItems(items)
	if err != nil {
		return nil, err
	}
	body["input"] = input
	body["tools"] = buildResponsesToolsFromSnapshot(replayToolDefinitions(snapshot))
	if body["store"] == nil {
		body["store"] = false
	}
	if body["stream"] == nil {
		body["stream"] = false
	}
	if body["max_output_tokens"] == nil {
		if value, ok := parameters["max_tokens"]; ok && value != nil {
			body["max_output_tokens"] = value
		}
	}
	if body["temperature"] == nil {
		if value, ok := parameters["temperature"]; ok && value != nil {
			body["temperature"] = value
		}
	}
	if body["text"] == nil {
		if text := responsesReplayText(parameters["response_format"]); text != nil {
			body["text"] = text
		}
	}
	if body["tool_choice"] == nil {
		if value, ok := parameters["tool_choice"]; ok && value != nil {
			body["tool_choice"] = value
		}
	}
	baseURL := normalizeOpenAIBaseURL(firstReplayString(replayString(apiProvider, "base_url"), replayString(provider, "base_url")))
	if baseURL == "" {
		return nil, fmt.Errorf("快照 api_provider.base_url 为空，无法构造 Responses 重放地址")
	}
	endpoint := strings.TrimRight(baseURL, "/") + responsesEndpoint
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化 Responses 重放请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("创建 Responses 重放请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if stream, _ := body["stream"].(bool); stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	applyReplayHeaders(req, apiProvider, apiKey)
	// request_kwargs 的显式覆盖优先于 api_provider 默认值
	for key, value := range extraHeaders {
		req.Header.Set(key, value)
	}
	if len(extraQuery) > 0 {
		query := req.URL.Query()
		for key, value := range extraQuery {
			query.Set(key, value)
		}
		req.URL.RawQuery = query.Encode()
	}
	return req, nil
}

// responsesReplayRequestKwargs 取出 Responses 请求体；兼容 provider_request 直接是请求体的旧快照
func responsesReplayRequestKwargs(provider map[string]any) map[string]any {
	if raw, ok := provider["request_kwargs"]; ok {
		return replayBody(raw)
	}
	if _, ok := provider["input"]; ok {
		return replayBody(provider)
	}
	return map[string]any{}
}

func replayRequestParameters(snapshot map[string]any) map[string]any {
	parameters, _ := snapshot["request_parameters"].(map[string]any)
	return parameters
}

// buildResponsesToolsFromSnapshot 把 Chat 风格 function 定义恢复成 Responses 扁平结构
func buildResponsesToolsFromSnapshot(definitions []any) []any {
	out := make([]any, 0, len(definitions))
	for _, entry := range definitions {
		definition, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		function, _ := definition["function"].(map[string]any)
		if function == nil {
			// 已经是 Responses 的扁平结构
			if replayString(definition, "name") != "" {
				out = append(out, definition)
			}
			continue
		}
		parameters, _ := function["parameters"].(map[string]any)
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        function["name"],
			"description": function["description"],
			"parameters":  parameters,
		})
	}
	return out
}

// responsesReplayText 从 request_parameters.response_format 恢复 Responses text 配置
func responsesReplayText(raw any) map[string]any {
	formatSpec, _ := raw.(map[string]any)
	if formatSpec == nil {
		return nil
	}
	switch replayString(formatSpec, "format_type") {
	case "json_object":
		return map[string]any{"format": map[string]any{"type": "json_object"}}
	case "json_schema":
		format := map[string]any{"type": "json_schema"}
		if schemaSpec, ok := formatSpec["schema"].(map[string]any); ok {
			for key, value := range cloneAnyMap(schemaSpec) {
				format[key] = value
			}
		}
		return map[string]any{"format": format}
	default:
		return nil
	}
}
