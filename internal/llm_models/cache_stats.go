package llm

// LLM 缓存统计上报
// 对齐 MaiBot src/services/llm_cache_stats.py 的采集入口：
//   - prompt_text 由实际请求的 Context Items、tool_options、response_format 序列化而来
//   - 只上报 prompt/cache token 与文本，不做业务判断；采样开关与聚合在 internal/services
//   - chat/gemini 的 Generate 与流式 EOF 共用同一上报路径

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"MaiGo/internal/llm_models/contract"
)

// CacheUsageEvent 是一次生成请求的缓存统计输入
type CacheUsageEvent struct {
	TaskName     string
	RequestType  string
	ModelName    string
	SessionID    string
	PromptTokens int64
	HitTokens    int64
	MissTokens   int64
	PromptText   string
}

type cacheUsageRecorderFunc func(CacheUsageEvent)

var cacheUsageRecorder = struct {
	mu sync.RWMutex
	fn cacheUsageRecorderFunc
}{}

// ConfigureCacheUsageRecorder 注入缓存统计接收器；nil 表示关闭上报
func ConfigureCacheUsageRecorder(recorder func(CacheUsageEvent)) {
	cacheUsageRecorder.mu.Lock()
	defer cacheUsageRecorder.mu.Unlock()
	if recorder == nil {
		cacheUsageRecorder.fn = nil
		return
	}
	cacheUsageRecorder.fn = recorder
}

func emitCacheUsage(event CacheUsageEvent) {
	cacheUsageRecorder.mu.RLock()
	recorder := cacheUsageRecorder.fn
	cacheUsageRecorder.mu.RUnlock()
	if recorder != nil {
		recorder(event)
	}
}

type cacheStatsMetadataContextKey struct{}

type cacheStatsMetadata struct {
	TaskName    string
	RequestType string
	SessionID   string
}

// WithCacheStatsMetadata 在请求上下文标注任务与请求类型，供缓存统计使用
func WithCacheStatsMetadata(ctx context.Context, taskName, requestType, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, cacheStatsMetadataContextKey{}, cacheStatsMetadata{
		TaskName: strings.TrimSpace(taskName), RequestType: strings.TrimSpace(requestType), SessionID: strings.TrimSpace(sessionID),
	})
}

func cacheStatsMetadataFromContext(ctx context.Context, cfg ModelConfig) cacheStatsMetadata {
	meta := cacheStatsMetadata{TaskName: strings.TrimSpace(cfg.TaskName), RequestType: strings.TrimSpace(cfg.RequestType), SessionID: strings.TrimSpace(cfg.SessionID)}
	if ctx == nil {
		return meta
	}
	if injected, ok := ctx.Value(cacheStatsMetadataContextKey{}).(cacheStatsMetadata); ok {
		if injected.TaskName != "" {
			meta.TaskName = injected.TaskName
		}
		if injected.RequestType != "" {
			meta.RequestType = injected.RequestType
		}
		if injected.SessionID != "" {
			meta.SessionID = injected.SessionID
		}
	}
	return meta
}

// buildCacheStatsPromptText 对齐 _build_cache_stats_prompt_text
func buildCacheStatsPromptText(items []ContextItem, options *model.Options, rawFormat any) string {
	contextItems := make([]any, 0, len(items))
	for _, item := range items {
		contextItems = append(contextItems, serializeCacheStatsItem(item))
	}
	toolOptions := make([]any, 0)
	if options != nil && len(options.Tools) > 0 {
		for _, tool := range pluginToolOptions(options.Tools) {
			toolOptions = append(toolOptions, tool)
		}
	}
	payload := map[string]any{
		"context_items":   contextItems,
		"tool_options":    toolOptions,
		"response_format": cacheStatsResponseFormat(rawFormat),
	}
	return pythonCacheJSON(payload)
}

func serializeCacheStatsItem(item ContextItem) map[string]any {
	payload := map[string]any{
		"item_id":   item.Meta.ItemID,
		"item_type": string(item.Type),
		"parts":     cacheStatsItemParts(item.Parts),
	}
	switch item.Type {
	case ContextItemReasoning:
		representation := strings.TrimSpace(item.Representation)
		if representation == "" {
			representation = "opaque"
		}
		payload["reasoning"] = GetContextItemText(item)
		payload["representation"] = representation
	case ContextItemFunctionCall:
		if item.ToolCall != nil {
			payload["tool_call"] = map[string]any{"id": item.ToolCall.CallID, "name": item.ToolCall.FuncName, "arguments": item.ToolCall.Args}
		}
	case ContextItemFunctionCallOutput:
		payload["call_id"] = item.CallID
		payload["output"] = item.Output
		payload["tool_name"] = item.ToolName
	}
	return payload
}

func cacheStatsItemParts(parts []ContextContentPart) []any {
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case ContextPartImage:
			encoded := part.ImageBase64
			sum := sha256.Sum256([]byte(encoded))
			out = append(out, map[string]any{
				"type": "image", "format": part.ImageFormat, "size": len(encoded), "sha256": hex.EncodeToString(sum[:]),
			})
		case ContextPartRefusal:
			// Python 只序列化 text/image，其他片段类型跳过
			continue
		default:
			out = append(out, map[string]any{"type": "text", "text": part.Text})
		}
	}
	return out
}

func cacheStatsResponseFormat(raw any) any {
	if raw == nil {
		return nil
	}
	if text, ok := raw.(string); ok && strings.EqualFold(strings.TrimSpace(text), "text") {
		return map[string]any{"format_type": "text"}
	}
	spec, err := parseResponseFormat(raw)
	if err != nil {
		return nil
	}
	if spec.Kind == "" {
		return nil
	}
	return responseFormatSnapshot(spec)
}

// recordGenerationCacheUsage 在非流式成功后上报一次缓存统计
func recordGenerationCacheUsage(ctx context.Context, cfg ModelConfig, items []ContextItem, options *model.Options, rawFormat any, message *schema.AgenticMessage) {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.TokenUsage == nil {
		return
	}
	usage := message.ResponseMeta.TokenUsage
	details, _ := message.ResponseMeta.Extension.(*contract.TokenUsageDetails)
	hit, miss, _ := cachedTokenFields(usage, details)
	meta := cacheStatsMetadataFromContext(ctx, cfg)
	emitCacheUsage(CacheUsageEvent{
		TaskName: meta.TaskName, RequestType: meta.RequestType, ModelName: cfg.Model, SessionID: meta.SessionID,
		PromptTokens: int64(usage.PromptTokens), HitTokens: hit, MissTokens: miss,
		PromptText: buildCacheStatsPromptText(items, options, rawFormat),
	})
}

type cacheStatsStreamCollector struct {
	ctx       context.Context
	cfg       ModelConfig
	items     []ContextItem
	options   *model.Options
	rawFormat any
	usage     *schema.TokenUsage
	details   *contract.TokenUsageDetails
}

// wrapAgenticStreamCacheStats 记录流中最后一次 usage，并在 EOF 时上报
func wrapAgenticStreamCacheStats(reader *schema.StreamReader[*schema.AgenticMessage], ctx context.Context, cfg ModelConfig, items []ContextItem, options *model.Options, rawFormat any) *schema.StreamReader[*schema.AgenticMessage] {
	collector := &cacheStatsStreamCollector{ctx: ctx, cfg: cfg, items: items, options: options, rawFormat: rawFormat}
	return schema.StreamReaderWithConvert(reader, func(message *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		if message != nil && message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
			collector.usage = message.ResponseMeta.TokenUsage
			collector.details, _ = message.ResponseMeta.Extension.(*contract.TokenUsageDetails)
		}
		return message, nil
	}, schema.WithOnEOF(func() (any, error) {
		if collector.usage != nil {
			hit, miss, _ := cachedTokenFields(collector.usage, collector.details)
			meta := cacheStatsMetadataFromContext(collector.ctx, collector.cfg)
			emitCacheUsage(CacheUsageEvent{
				TaskName: meta.TaskName, RequestType: meta.RequestType, ModelName: collector.cfg.Model, SessionID: meta.SessionID,
				PromptTokens: int64(collector.usage.PromptTokens), HitTokens: hit, MissTokens: miss,
				PromptText: buildCacheStatsPromptText(collector.items, collector.options, collector.rawFormat),
			})
		}
		return nil, io.EOF
	}))
}

// pythonCacheJSON 复刻 Python json.dumps(ensure_ascii=False, sort_keys=True) 的常见形态
func pythonCacheJSON(value any) string {
	var builder strings.Builder
	writePythonCacheJSON(&builder, value)
	return builder.String()
}

func writePythonCacheJSON(builder *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		writePythonCacheString(builder, typed)
	case int:
		builder.WriteString(strconv.Itoa(typed))
	case int8:
		builder.WriteString(strconv.FormatInt(int64(typed), 10))
	case int16:
		builder.WriteString(strconv.FormatInt(int64(typed), 10))
	case int32:
		builder.WriteString(strconv.FormatInt(int64(typed), 10))
	case int64:
		builder.WriteString(strconv.FormatInt(typed, 10))
	case uint:
		builder.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint8:
		builder.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint16:
		builder.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint32:
		builder.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint64:
		builder.WriteString(strconv.FormatUint(typed, 10))
	case float32:
		writePythonCacheFloat(builder, float64(typed))
	case float64:
		writePythonCacheFloat(builder, typed)
	case json.Number:
		builder.WriteString(typed.String())
	case []any:
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonCacheJSON(builder, item)
		}
		builder.WriteByte(']')
	case []string:
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonCacheString(builder, item)
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonCacheString(builder, key)
			builder.WriteString(": ")
			writePythonCacheJSON(builder, typed[key])
		}
		builder.WriteByte('}')
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = item
		}
		writePythonCacheJSON(builder, converted)
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			writePythonCacheString(builder, fmt.Sprint(typed))
			return
		}
		builder.Write(raw)
	}
}

func writePythonCacheString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"':
			builder.WriteString("\\\"")
		case '\\':
			builder.WriteString("\\\\")
		case '\b':
			builder.WriteString("\\b")
		case '\f':
			builder.WriteString("\\f")
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		case '\t':
			builder.WriteString("\\t")
		default:
			if char < 0x20 {
				builder.WriteString(fmt.Sprintf("\\u%04x", char))
			} else {
				builder.WriteRune(char)
			}
		}
	}
	builder.WriteByte('"')
}

func writePythonCacheFloat(builder *strings.Builder, value float64) {
	if value != value {
		builder.WriteString("NaN")
		return
	}
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	builder.WriteString(text)
}
