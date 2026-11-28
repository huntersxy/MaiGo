package model_client

// 插件 Provider 客户端，对等 src/llm_models/model_client/plugin_client.py。
//
// 经 PluginLLMInvoker（对齐 pluginruntime.Host.InvokeLLMProvider）发起 RPC，请求走
// Context Item 协议（item_schema_version=1），响应优先 output_items，兼容旧版标量协议。
import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"MaiGo/internal/llm_models/contract"
	providerformats "MaiGo/internal/plugin_runtime/provider_formats"
)

const (
	// pluginOperationResponse 文本/多模态响应操作名
	pluginOperationResponse = "response"
	// pluginOperationAudioTranscription 音频转写操作名
	pluginOperationAudioTranscription = "audio_transcription"
)

// PluginLLMResult 插件 Provider RPC 返回信封，对齐 protocol.Envelope 的 Payload/Error
type PluginLLMResult struct {
	Payload map[string]any
	Error   map[string]any
}

// PluginLLMInvoker 插件 Provider RPC 调用入口
type PluginLLMInvoker interface {
	InvokeLLMProvider(ctx context.Context, pluginID, clientType, operation string, request map[string]any, timeoutMS int64) (PluginLLMResult, error)
}

// PluginProviderResolver 把插件声明的 client_type 解析为归属插件与 RPC 入口
type PluginProviderResolver interface {
	ResolvePluginLLMProvider(clientType string) (pluginID string, invoker PluginLLMInvoker, ok bool)
}

// registeredPluginProvider 显式注册的插件 Provider
type registeredPluginProvider struct {
	pluginID string
	invoker  PluginLLMInvoker
	formats  providerformats.ProviderFormats
}

var (
	pluginProviderMu       sync.RWMutex
	pluginProviderResolver PluginProviderResolver
	pluginProviders        = map[string]registeredPluginProvider{}
)

// RegisterPluginProvider 注册 client_type → 插件 Provider RPC 入口
// 对齐 ClientRegistry.register_provider：内置类型不能被插件占用，同一 client_type 只能由一个插件拥有
func RegisterPluginProvider(clientType, pluginID string, invoker PluginLLMInvoker) error {
	clientType = normalizeClientType(clientType)
	if clientType == "" {
		return fmt.Errorf("插件 Provider 的 client_type 不能为空")
	}
	if invoker == nil {
		return fmt.Errorf("插件 Provider %q 缺少 RPC 入口", clientType)
	}
	if isBuiltinPluginClientType(clientType) {
		return fmt.Errorf("LLM Provider client_type 冲突: %s 已由 host 注册", clientType)
	}
	pluginID = trimTask(pluginID)
	pluginProviderMu.Lock()
	defer pluginProviderMu.Unlock()
	if existing, ok := pluginProviders[clientType]; ok && existing.pluginID != pluginID {
		return fmt.Errorf("LLM Provider client_type 冲突: %s 已由 %s 注册", clientType, pluginProviderOwnerLabel(existing.pluginID))
	}
	pluginProviders[clientType] = registeredPluginProvider{pluginID: pluginID, invoker: invoker, formats: existingFormats(clientType)}
	return nil
}

// RegisterPluginProviderFormats 更新已注册 Provider 的 formats 声明
func RegisterPluginProviderFormats(clientType string, formats providerformats.ProviderFormats) {
	clientType = normalizeClientType(clientType)
	pluginProviderMu.Lock()
	defer pluginProviderMu.Unlock()
	provider := pluginProviders[clientType]
	provider.formats = formats
	pluginProviders[clientType] = provider
}

func existingFormats(clientType string) providerformats.ProviderFormats {
	if provider, ok := pluginProviders[clientType]; ok {
		return provider.formats
	}
	return providerformats.ProviderFormats{}
}

// PluginProviderFormats 返回 client_type 对应的插件 formats
func PluginProviderFormats(clientType string) (providerformats.ProviderFormats, bool) {
	provider, ok := lookupRegisteredPluginProvider(clientType)
	if !ok {
		return providerformats.ProviderFormats{}, false
	}
	return provider.formats, true
}

func lookupPluginTTSPreset(clientType, model string) *providerformats.SpeechPreset {
	formats, ok := PluginProviderFormats(clientType)
	if !ok {
		return nil
	}
	if preset, ok := formats.TTS[model]; ok {
		copy := preset
		return &copy
	}
	return nil
}

// ParsePluginWebAdapter 解析 plugin:{owner}/{client_type} 联网适配器名
func ParsePluginWebAdapter(adapter string) (pluginID, clientType string, ok bool) {
	adapter = strings.TrimSpace(adapter)
	if !strings.HasPrefix(adapter, "plugin:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(adapter, "plugin:")
	pluginID, clientType, ok = strings.Cut(rest, "/")
	pluginID = strings.TrimSpace(pluginID)
	clientType = strings.TrimSpace(clientType)
	return pluginID, clientType, ok && pluginID != "" && clientType != ""
}

// FindPluginWebProvider 对齐 plugin web adapter 的 find_web_plugin
func FindPluginWebProvider(adapter string) (pluginID, clientType string, formats providerformats.ProviderFormats, ok bool) {
	pluginID, clientType, ok = ParsePluginWebAdapter(adapter)
	if !ok {
		return "", "", providerformats.ProviderFormats{}, false
	}
	provider, ok := lookupRegisteredPluginProvider(clientType)
	if !ok || provider.pluginID != pluginID || len(provider.formats.Web) == 0 {
		return "", "", providerformats.ProviderFormats{}, false
	}
	return pluginID, clientType, provider.formats, true
}

// InvokePluginWebOperation 调用插件声明的联网 RPC 操作
func InvokePluginWebOperation(ctx context.Context, cfg ModelConfig, pluginID string, invoker PluginLLMInvoker, operation string, args map[string]any) (map[string]any, error) {
	if invoker == nil {
		return nil, fmt.Errorf("联网插件缺少 RPC 入口")
	}
	m := &pluginModel{cfg: cfg, pluginID: pluginID, invoker: invoker}
	request := map[string]any{"api_provider": m.apiProviderSnapshot(true)}
	for key, value := range args {
		request[key] = value
	}
	return m.invoke(ctx, operation, request)
}

// PluginLLMInvokerForClientType 返回已注册插件 Provider 的 RPC 入口
func PluginLLMInvokerForClientType(clientType string) (string, PluginLLMInvoker, bool) {
	provider, ok := lookupRegisteredPluginProvider(clientType)
	if !ok {
		return "", nil, false
	}
	return provider.pluginID, provider.invoker, true
}

// isBuiltinPluginClientType 判断 MaiBot 主程序内置客户端类型，插件不能覆盖
func isBuiltinPluginClientType(clientType string) bool {
	switch clientType {
	case "openai", "openai_responses", "gemini":
		return true
	default:
		return false
	}
}

// pluginProviderOwnerLabel 对齐 Python：空 owner 表示 host
func pluginProviderOwnerLabel(owner string) string {
	if owner == "" {
		return "host"
	}
	return owner
}

// UnregisterPluginProvider 移除显式注册；不存在时静默返回
func UnregisterPluginProvider(clientType string) {
	clientType = normalizeClientType(clientType)
	pluginProviderMu.Lock()
	defer pluginProviderMu.Unlock()
	delete(pluginProviders, clientType)
}

// SetPluginProviderResolver 注册兼容的动态解析器；传 nil 表示停用
func SetPluginProviderResolver(resolver PluginProviderResolver) {
	pluginProviderMu.Lock()
	defer pluginProviderMu.Unlock()
	pluginProviderResolver = resolver
}

func lookupRegisteredPluginProvider(clientType string) (registeredPluginProvider, bool) {
	pluginProviderMu.RLock()
	defer pluginProviderMu.RUnlock()
	provider, ok := pluginProviders[normalizeClientType(clientType)]
	return provider, ok
}

func pluginProviderResolverSnapshot() PluginProviderResolver {
	pluginProviderMu.RLock()
	defer pluginProviderMu.RUnlock()
	return pluginProviderResolver
}

// resolvePluginProvider 解析插件 Provider 归属：显式注册优先，其次动态 resolver
func resolvePluginProvider(cfg ModelConfig) (string, PluginLLMInvoker, error) {
	pluginID := trimTask(cfg.PluginID)
	if registered, ok := lookupRegisteredPluginProvider(cfg.ClientType); ok {
		if pluginID == "" {
			pluginID = registered.pluginID
		}
		if pluginID == "" {
			return "", nil, fmt.Errorf("插件 Provider %q 缺少归属插件 ID", cfg.ClientType)
		}
		return pluginID, registered.invoker, nil
	}
	resolver := pluginProviderResolverSnapshot()
	if resolver == nil {
		if pluginID != "" {
			return "", nil, fmt.Errorf("插件 Provider %q 缺少 RPC 入口（未注册 PluginProviderResolver）", cfg.ClientType)
		}
		return "", nil, fmt.Errorf("client_type %q 未注册（既非内置类型，也没有插件 Provider 声明）", cfg.ClientType)
	}
	resolvedID, invoker, ok := resolver.ResolvePluginLLMProvider(cfg.ClientType)
	if !ok || invoker == nil {
		return "", nil, fmt.Errorf("client_type %q 没有插件 Provider 声明", cfg.ClientType)
	}
	if pluginID == "" {
		pluginID = trimTask(resolvedID)
	}
	if pluginID == "" {
		return "", nil, fmt.Errorf("插件 Provider %q 缺少归属插件 ID", cfg.ClientType)
	}
	return pluginID, invoker, nil
}

// PluginProviderAvailable 判断 client_type 是否由插件提供
func PluginProviderAvailable(clientType string) bool {
	if _, ok := lookupRegisteredPluginProvider(clientType); ok {
		return true
	}
	resolver := pluginProviderResolverSnapshot()
	if resolver == nil {
		return false
	}
	_, _, ok := resolver.ResolvePluginLLMProvider(clientType)
	return ok
}

// RegisteredPluginProvider 已注册插件 Provider 摘要，供 WebUI client-types 列举
type RegisteredPluginProvider struct {
	ClientType string
	PluginID   string
	Formats    providerformats.ProviderFormats
}

// ListRegisteredPluginProviders 返回当前显式注册的插件 client_type 列表（按类型名字典序）
func ListRegisteredPluginProviders() []RegisteredPluginProvider {
	pluginProviderMu.RLock()
	defer pluginProviderMu.RUnlock()
	out := make([]RegisteredPluginProvider, 0, len(pluginProviders))
	for clientType, provider := range pluginProviders {
		out = append(out, RegisteredPluginProvider{
			ClientType: clientType,
			PluginID:   provider.pluginID,
			Formats:    provider.formats,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClientType < out[j].ClientType })
	return out
}

// pluginModel 通过插件 Provider 完成一次对话请求
type pluginModel struct {
	cfg      ModelConfig
	pluginID string
	invoker  PluginLLMInvoker
}

func NewPluginModel(cfg ModelConfig) (model.AgenticModel, error) {
	pluginID, invoker, err := resolvePluginProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &pluginModel{cfg: cfg, pluginID: pluginID, invoker: invoker}, nil
}

// Generate 调用插件 Provider 的 response 操作
func (m *pluginModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	options := model.GetCommonOptions(&model.Options{}, opts...)
	resolveEffectiveParams(m.cfg, options)
	logicalTurnID := NewContextUUID()
	request, items, err := m.buildResponseRequest(ctx, input, options, logicalTurnID)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, options, nil, err)
	}
	result, err := m.invoke(ctx, pluginOperationResponse, request)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, options, request, err)
	}
	message, err := pluginResultToMessage(result, logicalTurnID)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, options, request, err)
	}
	// 插件协议一次响应即结束；缓存统计仅在 Generate 记录一次，Stream 复用 Generate 不重复上报
	recordGenerationCacheUsage(ctx, m.cfg, items, options, effectiveResponseFormat(ctx, m.cfg.ResponseFormat), message)
	return message, nil
}

// Stream 插件 Provider 协议只有整体响应；按单帧流回放（对齐 MaiBot PluginLLMClient 不支持宿主流式处理器）
func (m *pluginModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{message}), nil
}

// snapshotFailure 保存插件 Provider 失败请求快照
func (m *pluginModel) snapshotFailure(recorder *snapshotAttemptRecorder, started time.Time, input []*schema.AgenticMessage, options *model.Options, request map[string]any, err error) error {
	if err == nil {
		return nil
	}
	internal := map[string]any{
		"model":       m.cfg.Model,
		"base_url":    m.cfg.BaseURL,
		"plugin_id":   m.pluginID,
		"client_type": m.cfg.ClientType,
		"operation":   pluginOperationResponse,
	}
	spec := newRequestSnapshotSpec(m.cfg, "plugin.llm_provider", "response", input, options, request, internal)
	spec.LogicalTurnID = snapshotLogicalTurnID(recorder)
	spec.Error = err
	spec.StartedAt = started
	recordSnapshotAttempt(recorder, &spec, started)
	return captureRequestSnapshot(spec)
}

// buildResponseRequest 构造 MaiBot context_items 请求载荷
func (m *pluginModel) buildResponseRequest(ctx context.Context, input []*schema.AgenticMessage, options *model.Options, logicalTurnID string) (map[string]any, []ContextItem, error) {
	items, err := contextItemsForConfig(input, m.cfg)
	if err != nil {
		return nil, nil, &ParseError{Message: "构造 Context Items 失败: " + err.Error()}
	}
	if err := ValidateContextItems(items, ContextProtocolRequestContext, nil); err != nil {
		return nil, nil, &ParseError{Message: err.Error()}
	}
	resolveEffectiveParams(m.cfg, options)
	// 对齐 Python：send_temperature=false 只清空 request.temperature，不剥 extra_params 内温度
	extraParams := cloneAnyMap(m.cfg.ExtraParams)
	request := map[string]any{
		"item_schema_version": ContextItemSchemaVersion,
		"context_items":       SerializeContextItemsSnapshot(items),
		"logical_turn_id":     logicalTurnID,
		"request_kind":        "response",
		"extra_params":        extraParams,
		"model_info":          m.modelInfoSnapshot(),
		"tool_options":        PluginToolOptions(options.Tools),
		"api_provider":        m.apiProviderSnapshot(true),
	}
	if maxTokens := m.maxTokens(options); maxTokens != nil {
		request["max_tokens"] = *maxTokens
	}
	if temperature := m.temperature(options); temperature != nil {
		request["temperature"] = *temperature
	}
	if choice := pluginToolChoice(options); choice != "" {
		request["tool_choice"] = choice
	}
	if formatSpec := m.responseFormatSpec(ctx); formatSpec != nil {
		request["response_format"] = responseFormatSnapshot(*formatSpec)
	}
	return request, items, nil
}

// maxTokens 返回已解析的请求级 max_tokens；模型级/任务级默认值由 resolveEffectiveParams 写入
func (m *pluginModel) maxTokens(options *model.Options) *int {
	if options == nil {
		return nil
	}
	return options.MaxTokens
}

// temperature 返回已解析的请求级温度；send_temperature=false 时 resolveEffectiveParams 已清空
func (m *pluginModel) temperature(options *model.Options) *float32 {
	if options == nil {
		return nil
	}
	return options.Temperature
}

func (m *pluginModel) responseFormatSpec(ctx context.Context) *ResponseFormatSpec {
	raw := effectiveResponseFormat(ctx, m.cfg.ResponseFormat)
	if raw == nil {
		return nil
	}
	spec, err := parseResponseFormat(raw)
	if err != nil || spec.Kind == "" {
		return nil
	}
	return &spec
}

// modelInfoSnapshot 模型信息快照，字段对齐 serialize_model_info_snapshot
func (m *pluginModel) modelInfoSnapshot() map[string]any {
	return ModelInfoSnapshotForConfig(m.cfg)
}

// ModelInfoSnapshotForConfig 供插件请求与 hard_timeout 快照共用
func ModelInfoSnapshotForConfig(cfg ModelConfig) map[string]any {
	displayName := trimTask(cfg.ModelDisplayName)
	if displayName == "" {
		displayName = cfg.Model
	}
	// Python ModelInfo.api_provider 是 Provider 名称字符串；完整 Provider 配置在请求顶层 api_provider
	providerName := trimTask(cfg.ProviderName)
	if providerName == "" {
		providerName = cfg.ClientType
	}
	maxImages, imageMaxSide, imageMinSide := cfg.EffectiveImageLimits()
	snapshot := map[string]any{
		"api_provider":      providerName,
		"extra_params":      cloneAnyMap(cfg.ExtraParams),
		"force_stream_mode": cfg.ForceStreamMode,
		"max_images":        maxImages,
		"image_max_side":    imageMaxSide,
		"image_min_side":    imageMinSide,
		"max_tokens":        nil,
		"model_identifier":  cfg.Model,
		"model_type":        cfg.ModelType,
		"name":              displayName,
		"send_temperature":  effectiveSendTemperature(cfg),
		"temperature":       nil,
		"visual":            cfg.Visual,
	}
	if value, ok := IntFromAny(cfg.ModelMaxTokens); ok {
		snapshot["max_tokens"] = value
	}
	if cfg.ModelTemperature != nil {
		if value, ok := floatFromAny(cfg.ModelTemperature); ok {
			snapshot["temperature"] = value
		}
	}
	return snapshot
}

// invoke 发起 RPC 并按 MaiBot 语义分类错误
func (m *pluginModel) invoke(ctx context.Context, operation string, request map[string]any) (map[string]any, error) {
	result, err := m.invoker.InvokeLLMProvider(ctx, m.pluginID, m.cfg.ClientType, operation, request, m.timeoutMS())
	if err != nil {
		if abortErr := abortErrorFromContext(ctx); abortErr != nil {
			return nil, abortErr
		}
		return nil, &ParseError{Message: fmt.Sprintf("插件 LLM Provider RPC 调用失败: %v", err)}
	}
	if len(result.Error) > 0 {
		return nil, &ParseError{Message: pluginErrorMessage(result.Error, "插件 LLM Provider 调用失败")}
	}
	success, _ := result.Payload["success"].(bool)
	payloadResult := result.Payload["result"]
	if !success {
		if text, ok := payloadResult.(string); ok && strings.TrimSpace(text) != "" {
			return nil, &ParseError{Message: text}
		}
		return nil, &ParseError{Message: "插件 LLM Provider 返回失败"}
	}
	decoded, ok := payloadResult.(map[string]any)
	if !ok {
		return nil, &ParseError{Message: "插件 LLM Provider 返回值必须是字典"}
	}
	return decoded, nil
}

// timeoutMS 对齐 MaiBot：max(1000, int(timeout)*1000)
func (m *pluginModel) timeoutMS() int64 {
	milliseconds := int64(m.cfg.Timeout / time.Millisecond)
	if milliseconds < 1000 {
		return 1000
	}
	return milliseconds
}

func pluginErrorMessage(payload map[string]any, fallback string) string {
	if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
		return message
	}
	return fallback
}

// pluginResultToMessage Context Items / 旧标量协议 → eino 消息
func pluginResultToMessage(result map[string]any, logicalTurnID string) (*schema.AgenticMessage, error) {
	if rawItems, hasItems := result["output_items"]; hasItems && rawItems != nil {
		items, err := pluginOutputItems(rawItems, result["item_schema_version"], logicalTurnID)
		if err != nil {
			return nil, err
		}
		if err := ValidateContextItems(items, ContextProtocolModelOutput, nil); err != nil {
			return nil, &ParseError{Message: err.Error()}
		}
		message, err := AgenticMessageFromContextItems(items)
		if err != nil {
			return nil, err
		}
		message.ResponseMeta = pluginUsageMeta(result["usage"])
		return message, nil
	}
	content := pluginFirstString(result, "content", "response")
	reasoning := pluginFirstString(result, "reasoning_content", "reasoning")
	calls, err := pluginToolCalls(result["tool_calls"])
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) == "" && len(calls) == 0 {
		return nil, &EmptyResponseError{Message: "插件 LLM Provider 未返回任何内容"}
	}
	toolCalls := make([]ContextToolCall, 0, len(calls))
	for _, call := range calls {
		args := parseProjectedToolArguments(call.Arguments)
		toolCalls = append(toolCalls, NewContextToolCall(call.CallID, call.Name, args))
	}
	items, err := BuildPortableOutputItems(content, reasoning, toolCalls, logicalTurnID)
	if err != nil {
		return nil, &ParseError{Message: err.Error()}
	}
	message, err := AgenticMessageFromContextItems(items)
	if err != nil {
		return nil, err
	}
	message.ResponseMeta = pluginUsageMeta(result["usage"])
	return message, nil
}

func pluginOutputItems(raw any, schemaVersion any, logicalTurnID string) ([]ContextItem, error) {
	version, ok := IntFromAny(schemaVersion)
	if !ok || version != ContextItemSchemaVersion {
		return nil, &ParseError{Message: fmt.Sprintf("插件 LLM Provider 返回 output_items 时 item_schema_version 必须为 %d", ContextItemSchemaVersion)}
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, &ParseError{Message: "插件 LLM Provider output_items 必须是列表"}
	}
	items := make([]ContextItem, 0, len(list))
	timestamp := time.Now().Format(time.RFC3339Nano)
	for _, entry := range list {
		rawItem, ok := entry.(map[string]any)
		if !ok {
			return nil, &ParseError{Message: "插件 LLM Provider output_items 项必须是字典"}
		}
		cloned := cloneAnyMap(rawItem)
		cloned["meta"] = map[string]any{
			"item_id":         NewContextUUID(),
			"logical_turn_id": logicalTurnID,
			"timestamp":       timestamp,
		}
		item, err := DeserializeContextItemSnapshot(cloned)
		if err != nil {
			return nil, &ParseError{Message: "插件 LLM Provider output_items 结构不合法: " + err.Error()}
		}
		if item.Meta.ItemID == "" || item.Meta.LogicalTurnID == "" {
			return nil, &ParseError{Message: "插件 LLM Provider output_items 缺少有效 meta"}
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, &EmptyResponseError{Message: "插件 LLM Provider 未返回任何内容"}
	}
	return items, nil
}

// pluginFirstString 依次取第一个非空字符串字段
func pluginFirstString(result map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := result[key].(string); ok {
			return text
		}
	}
	return ""
}

// pluginToolCalls 解析旧版 tool_calls 协议
// 支持两种形状：{id, function:{name, arguments}} 与 {call_id, name, arguments}
func pluginToolCalls(raw any) ([]*schema.FunctionToolCall, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, &ParseError{Message: "插件 LLM Provider tool_calls 必须是列表"}
	}
	calls := make([]*schema.FunctionToolCall, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, &ParseError{Message: "插件 LLM Provider tool_calls 项必须是字典"}
		}
		name := ""
		var args any
		if function, ok := entry["function"].(map[string]any); ok {
			name, _ = function["name"].(string)
			args = function["arguments"]
		} else {
			name, _ = entry["name"].(string)
			args = entry["arguments"]
		}
		callID := pluginFirstString(entry, "id", "call_id")
		if strings.TrimSpace(name) == "" || strings.TrimSpace(callID) == "" {
			return nil, &ParseError{Message: "插件 LLM Provider tool_call 缺少 id 或 function.name"}
		}
		arguments := "{}"
		switch typed := args.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				arguments = typed
			}
		case map[string]any:
			if encoded, err := sonic.MarshalString(typed); err == nil {
				arguments = encoded
			}
		}
		calls = append(calls, &schema.FunctionToolCall{CallID: callID, Name: name, Arguments: arguments})
	}
	return calls, nil
}

// pluginUsageMeta 解析使用量记录，保留供应商上报的命中、未命中与上报标志
func pluginUsageMeta(raw any) *schema.AgenticResponseMeta {
	payload, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	promptTokens := pluginInt(payload, "prompt_tokens")
	hit, reportedHit := pluginIntField(payload, "prompt_cache_hit_tokens")
	miss, reportedMiss := pluginIntField(payload, "prompt_cache_miss_tokens")
	usage := &schema.TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: pluginInt(payload, "completion_tokens"),
		TotalTokens:      pluginInt(payload, "total_tokens"),
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: max(hit, 0),
		},
	}
	var details *contract.TokenUsageDetails
	if reportedHit || reportedMiss {
		if !reportedMiss {
			miss = max(promptTokens-hit, 0)
		}
		details = &contract.TokenUsageDetails{
			PromptCacheHitTokens:  max(hit, 0),
			PromptCacheMissTokens: max(miss, 0),
			PromptCacheReported:   true,
		}
	}
	return &schema.AgenticResponseMeta{TokenUsage: usage, Extension: details}
}

func pluginUsage(raw any) *schema.TokenUsage {
	meta := pluginUsageMeta(raw)
	if meta == nil {
		return nil
	}
	return meta.TokenUsage
}

func pluginInt(payload map[string]any, key string) int {
	value, _ := pluginIntField(payload, key)
	return value
}

func pluginIntField(payload map[string]any, key string) (int, bool) {
	if value, ok := payload[key]; ok {
		if parsed, ok := IntFromAny(value); ok {
			return parsed, true
		}
		return 0, true
	}
	return 0, false
}

// PluginToolOptions 工具定义 → MaiBot tool_options（OpenAI function 包装）
func PluginToolOptions(tools []*schema.ToolInfo) []map[string]any {
	options := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			continue
		}
		parameters := map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
		if tool.ParamsOneOf != nil {
			if jsonSchema, err := tool.ParamsOneOf.ToJSONSchema(); err == nil && jsonSchema != nil {
				if encoded, err := sonic.Marshal(jsonSchema); err == nil {
					var parsed map[string]any
					if sonic.Unmarshal(encoded, &parsed) == nil && parsed != nil {
						parameters = parsed
					}
				}
			}
		}
		options = append(options, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Desc,
				"parameters":  parameters,
			},
		})
	}
	return options
}

// pluginToolChoice eino 工具选择 → MaiBot tool_choice
// 优先 AgenticToolChoice（与 Chat Completions 的 ChatToolChoiceValue 一致），legacy ToolChoice 回退
// Python ResponseRequest 仅能表达 None / "none"（base_client.py:282），auto/required 属于 Go 侧扩展
func pluginToolChoice(options *model.Options) string {
	return ChatToolChoiceValue(options)
}

// truncateRunes 按字符截断，避免破坏 UTF-8
func truncateRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}
