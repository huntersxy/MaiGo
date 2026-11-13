package model_client

// OpenAI Responses 请求的 extra_params 拆分与应用。
//
// 对等 src/llm_models/openai_responses_client.py 的 get_response 与
// src/llm_models/openai_compat.py 的 split_openai_request_overrides：
// headers/query/body 三分，受保护字段 input/model/previous_response_id/stream
// 拒绝覆盖，tools 与 function tools 合并且重名拒绝，text 与 response_format
// 合并冲突拒绝。body 字段经 eino 的 option.WithJSONSet 写入顶层 JSON。
//
// 已知边界（eino 不支持显式 null 覆盖、tool_choice 由 AgenticToolChoice 承载）
// 见 agents/MODULE_ALIGNMENT.md。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
)

// responsesProtectedBodyKeys 由客户端管理、禁止 extra_params 覆盖的 Responses 字段
var responsesProtectedBodyKeys = []string{"input", "model", "previous_response_id", "stream"}

// responsesExtraParams Responses extra_params 的拆分与校验结果（模型级，创建后只读）
type responsesExtraParams struct {
	Headers map[string]string
	Query   map[string]string
	// nativeTools extra_params.body.tools 中的原生工具定义
	nativeTools []any
	// nativeText extra_params.body.text 中的原生文本配置
	nativeText map[string]any
	// body 去掉 tools/text/max_tokens/max_output_tokens/temperature/store 后的顶层字段
	body map[string]any
	// maxOutputTokens/maxTokens 保留原始 JSON 值，按优先级解析
	maxOutputTokens    any
	hasMaxOutputTokens bool
	maxTokens          any
	hasMaxTokens       bool
	temperature        any
	hasTemperature     bool
}

// SplitResponsesExtraParams 拆分并校验 Responses 的 extra_params
func SplitResponsesExtraParams(extraParams map[string]any) (*responsesExtraParams, error) {
	extra := &responsesExtraParams{}
	if len(extraParams) == 0 {
		return extra, nil
	}
	raw := cloneAnyMap(extraParams)
	if raw == nil {
		raw = map[string]any{}
	}

	headers, err := popResponsesObject(raw, "headers", ".headers")
	if err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		extra.Headers = make(map[string]string, len(headers))
		for key, value := range headers {
			extra.Headers[key] = fmt.Sprint(value)
		}
	}

	query, err := popResponsesObject(raw, "query", ".query")
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		extra.Query = make(map[string]string, len(query))
		for key, value := range query {
			extra.Query[key] = fmt.Sprint(value)
		}
	}

	body, err := popResponsesObject(raw, "body", ".body")
	if err != nil {
		return nil, err
	}
	merged := make(map[string]any, len(body)+len(raw))
	for key, value := range body {
		merged[key] = value
	}
	// 顶层散落键与 body 同名时以顶层为准，对齐 split_openai_request_overrides
	for key, value := range raw {
		merged[key] = value
	}

	for _, key := range responsesProtectedBodyKeys {
		if _, ok := merged[key]; ok {
			return nil, fmt.Errorf("Responses extra_params 不允许覆盖由客户端管理的字段: %s", key)
		}
	}

	if value, ok := merged["tools"]; ok {
		delete(merged, "tools")
		tools, err := responsesNativeTools(value)
		if err != nil {
			return nil, err
		}
		extra.nativeTools = tools
	}

	if value, ok := merged["text"]; ok {
		delete(merged, "text")
		if value != nil {
			text, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Responses extra_params.body.text 必须是字典")
			}
			// 浅拷贝即可：合并只会改写顶层 format
			extra.nativeText = cloneAnyMap(text)
		}
	}

	if value, ok := merged["store"]; ok {
		delete(merged, "store")
		if store, ok := value.(bool); !ok || store {
			return nil, fmt.Errorf("openai_responses 当前固定使用 store=false")
		}
	}

	if value, ok := merged["max_tokens"]; ok {
		delete(merged, "max_tokens")
		limit, err := responsesTokenLimit(value, "max_tokens")
		if err != nil {
			return nil, err
		}
		extra.maxTokens = limit
		extra.hasMaxTokens = limit != nil
	}
	if value, ok := merged["max_output_tokens"]; ok {
		delete(merged, "max_output_tokens")
		limit, err := responsesTokenLimit(value, "max_output_tokens")
		if err != nil {
			return nil, err
		}
		extra.maxOutputTokens = limit
		extra.hasMaxOutputTokens = limit != nil
	}

	if value, ok := merged["temperature"]; ok {
		delete(merged, "temperature")
		if value != nil {
			if _, ok := responsesFloat64(value); !ok {
				return nil, fmt.Errorf("Responses extra_params.body.temperature 必须是数字")
			}
			extra.temperature = value
			extra.hasTemperature = true
		}
	}

	extra.body = merged
	return extra, nil
}

// popResponsesObject 取出并删除对象字段；缺失或 null 视为未配置
func popResponsesObject(raw map[string]any, key, path string) (map[string]any, error) {
	value, ok := raw[key]
	if !ok {
		return nil, nil
	}
	delete(raw, key)
	if value == nil {
		return nil, nil
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Responses extra_params%s 必须是对象", path)
	}
	return typed, nil
}

// responsesNativeTools 校验 body.tools 是工具定义字典列表
func responsesNativeTools(value any) ([]any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []any:
		tools := make([]any, 0, len(typed))
		for _, item := range typed {
			definition, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Responses extra_params.body.tools 必须是工具定义字典列表")
			}
			tools = append(tools, definition)
		}
		return tools, nil
	case []map[string]any:
		tools := make([]any, 0, len(typed))
		for _, item := range typed {
			tools = append(tools, item)
		}
		return tools, nil
	default:
		return nil, fmt.Errorf("Responses extra_params.body.tools 必须是工具定义字典列表")
	}
}

// responsesTokenLimit 校验 token 上限是非负整数，返回原始值以保持 JSON 精度
func responsesTokenLimit(value any, field string) (any, error) {
	if value == nil {
		return nil, nil
	}
	number, ok := responsesFloat64(value)
	if !ok || number < 0 || number != math.Trunc(number) {
		return nil, fmt.Errorf("Responses extra_params.body.%s 必须是非负整数", field)
	}
	return value, nil
}

// responsesFloat64 把 JSON 数字与 Go 数值统一成 float64
func responsesFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// responsesRequestPlan 一次 Responses 请求应用 extra_params 后的最终参数
type responsesRequestPlan struct {
	// extraBody 需要写入 provider_request 快照的剩余顶层字段
	extraBody map[string]any
	headers   map[string]string
	query     map[string]string
	// tools 合并后的工具定义，nil 表示沿用 eino 的函数工具
	tools []any
	text  map[string]any
	// maxOutputTokens/temperature 是最终生效值，供快照记录
	maxOutputTokens any
	temperature     any
	extraFields     map[string]any
	requestOptions  []model.Option
}

// buildPlan 生成请求级应用结果：合并 tools/text 并按优先级解析参数
	func (extra *responsesExtraParams) buildPlan(options *model.Options, textConfig map[string]any, configExtraFields map[string]any) (*responsesRequestPlan, error) {
	plan := &responsesRequestPlan{
		extraBody: cloneAnyMap(extra.body),
		headers:   extra.Headers,
		query:     extra.Query,
	}
	functionTools, err := buildResponsesToolDefinitions(options)
	if err != nil {
		return nil, err
	}
	plan.tools = functionTools
	if len(extra.nativeTools) > 0 {
		plan.tools, err = mergeResponsesNativeTools(functionTools, extra.nativeTools)
		if err != nil {
			return nil, err
		}
	}
	plan.text, err = mergeResponsesText(extra.nativeText, textConfig)
	if err != nil {
		return nil, err
	}

	// max_output_tokens 优先级：请求参数 > body.max_output_tokens > body.max_tokens（Python 四档顺序）
	requestMaxTokens := options != nil && options.MaxTokens != nil
	switch {
	case requestMaxTokens:
		plan.maxOutputTokens = *options.MaxTokens
	case extra.hasMaxOutputTokens:
		plan.maxOutputTokens = extra.maxOutputTokens
	case extra.hasMaxTokens:
		plan.maxOutputTokens = extra.maxTokens
	}
		// 对齐 Python openai_responses_client.py:710：extra_body.temperature 优先于 request.temperature；
		// send_temperature=false 只让上游把 request.temperature 置空，不剥 extra_body。
		switch {
		case extra.temperature != nil:
			plan.temperature = extra.temperature
		case options != nil && options.Temperature != nil:
			plan.temperature = float64(*options.Temperature)
		default:
			plan.temperature = nil
		}

	fields := make(map[string]any, len(configExtraFields)+len(extra.body)+4)
	// ResponsesConfig.ExtraFields 的模型级字段（thinking）必须保留：
	// WithExtraFields 会整体替换模型级配置
	for key, value := range configExtraFields {
		fields[key] = value
	}
	for key, value := range extra.body {
		fields[key] = value
	}
	if options != nil && options.AgenticToolChoice != nil {
		// 请求级 tool_choice 由 eino 原生参数承载，优先级高于 extra_params
		delete(fields, "tool_choice")
	}
	if len(extra.nativeTools) > 0 {
		fields["tools"] = plan.tools
	}
	if len(plan.text) > 0 {
		fields["text"] = plan.text
	}
	if !requestMaxTokens && (extra.hasMaxOutputTokens || extra.hasMaxTokens) {
		fields["max_output_tokens"] = plan.maxOutputTokens
	}
	if plan.temperature != nil {
		fields["temperature"] = plan.temperature
	}
	if len(fields) > 0 {
		plan.extraFields = fields
		plan.requestOptions = append(plan.requestOptions, agenticopenai.WithExtraFields(fields))
	}
	return plan, nil
}

// mergeResponsesNativeTools 合并 MaiBot function tools 与原生工具定义，function 重名拒绝
func mergeResponsesNativeTools(functionTools, nativeTools []any) ([]any, error) {
	functionNames := make(map[string]struct{}, len(functionTools))
	for _, tool := range functionTools {
		definition, _ := tool.(map[string]any)
		if definition == nil || definition["type"] != "function" {
			continue
		}
		if name, _ := definition["name"].(string); name != "" {
			functionNames[name] = struct{}{}
		}
	}
	duplicates := make(map[string]struct{})
	for _, tool := range nativeTools {
		definition, _ := tool.(map[string]any)
		if definition == nil || definition["type"] != "function" {
			continue
		}
		name, _ := definition["name"].(string)
		if _, ok := functionNames[name]; ok && name != "" {
			duplicates[name] = struct{}{}
		}
	}
	if len(duplicates) > 0 {
		names := make([]string, 0, len(duplicates))
		for name := range duplicates {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("Responses 原生 function tools 与 MaiBot 工具重名: %s", strings.Join(names, ", "))
	}
	merged := make([]any, 0, len(functionTools)+len(nativeTools))
	merged = append(merged, functionTools...)
	merged = append(merged, nativeTools...)
	return merged, nil
}

// mergeResponsesText 合并原生 text 配置与 response_format 的 format，冲突拒绝
func mergeResponsesText(nativeText, textConfig map[string]any) (map[string]any, error) {
	text := cloneAnyMap(nativeText)
	if text == nil {
		text = map[string]any{}
	}
	format := textConfig["format"]
	if format != nil {
		if existing, ok := text["format"]; ok && existing != nil && !reflect.DeepEqual(existing, format) {
			return nil, fmt.Errorf("Responses 原生 text.format 与 MaiBot response_format 冲突")
		}
		text["format"] = format
	}
	if len(text) == 0 {
		return nil, nil
	}
	return text, nil
}

// resolveResponsesTextConfig 生成 response_format 对应的 text 配置；nil 表示不发送 text
func resolveResponsesTextConfig(ctx context.Context, configured any) (map[string]any, error) {
	spec, err := parseResponseFormat(effectiveResponseFormat(ctx, configured))
	if err != nil {
		return nil, err
	}
	switch spec.Kind {
	case "json_object":
		return map[string]any{"format": map[string]any{"type": "json_object"}}, nil
	case "json_schema":
		format := map[string]any{"type": "json_schema"}
		for key, value := range spec.Schema {
			format[key] = value
		}
		return map[string]any{"format": format}, nil
	default:
		return nil, nil
	}
}
