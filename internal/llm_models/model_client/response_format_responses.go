package model_client

// OpenAI Responses API 的请求包装。
//
// agenticopenai 没有把 text.format 放进 ResponsesConfig，只能在每次 Generate/Stream
// 时注入：response_format → body.text.format，extra_params 三分与请求参数合并
// （见 responses_request.go），失败时按 MaiBot 的 provider_request 结构落盘快照，
// 并把 eino-ext 包装的错误还原为 MaiBot 的异常语义（见 responses_errors.go）。
import (
	"context"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type ResponsesResponseFormatModel struct {
	inner   model.AgenticModel
	cfg     ModelConfig
	baseURL string
	// extra extra_params 拆分结果（模型级配置，创建后只读）
	extra *responsesExtraParams
	// configExtraFields 传给 ResponsesConfig.ExtraFields 的模型级字段（thinking）
	configExtraFields map[string]any
}

func (m *ResponsesResponseFormatModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	options := model.GetCommonOptions(&model.Options{}, opts...)
	resolveEffectiveParams(m.cfg, options)
	input = agenticMessagesForConfig(input, m.cfg)
	items, err := contextItemsForConfig(input, m.cfg)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, nil, options, nil, false, err)
	}
	plan, err := m.buildRequestPlan(ctx, options)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, items, options, nil, false, err)
	}
	message, err := m.inner.Generate(ctx, input, responsesDownstreamOptions(options, plan.requestOptions)...)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, items, options, plan, false, normalizeResponsesCallError(ctx, err))
	}
	message, err = normalizeTextToolCalls(message, options.Tools, ChatToolChoiceValue(options), textToolCallOccupiedIDs(items))
	if err != nil {
		err = &ParseError{Message: err.Error(), Err: err}
		return nil, m.snapshotFailure(recorder, started, input, items, options, plan, false, err)
	}
	cacheCfg := m.cfg
	if options != nil && options.Model != nil && strings.TrimSpace(*options.Model) != "" {
		cacheCfg.Model = strings.TrimSpace(*options.Model)
	}
	recordGenerationCacheUsage(ctx, cacheCfg, items, options, effectiveResponseFormat(ctx, m.cfg.ResponseFormat), message)
	return message, nil
}

func (m *ResponsesResponseFormatModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	options := model.GetCommonOptions(&model.Options{}, opts...)
	resolveEffectiveParams(m.cfg, options)
	input = agenticMessagesForConfig(input, m.cfg)
	items, err := contextItemsForConfig(input, m.cfg)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, nil, options, nil, true, err)
	}
	plan, err := m.buildRequestPlan(ctx, options)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, items, options, nil, true, err)
	}
	reader, err := m.inner.Stream(ctx, input, responsesDownstreamOptions(options, plan.requestOptions)...)
	if err != nil {
		return nil, m.snapshotFailure(recorder, started, input, items, options, plan, true, normalizeResponsesCallError(ctx, err))
	}
	// 先还原 eino-ext 的流错误与未完成状态，再挂失败快照，保证快照记录的是 MaiBot 异常分类
	reader = wrapResponsesStreamErrors(ctx, reader)
	reader = wrapAgenticStreamSnapshot(reader, func(streamErr error) error {
		return m.snapshotFailure(recorder, started, input, items, options, plan, true, streamErr)
	})
	// 缓存统计只在整个流正常 EOF 时上报；中途错误由上面的错误包装先返回，不会触发 OnEOF
	cacheCfg := m.cfg
	if options != nil && options.Model != nil && strings.TrimSpace(*options.Model) != "" {
		cacheCfg.Model = strings.TrimSpace(*options.Model)
	}
	return wrapAgenticStreamCacheStats(reader, ctx, cacheCfg, items, options, effectiveResponseFormat(ctx, m.cfg.ResponseFormat)), nil
}

// buildRequestPlan 解析 response_format 并生成请求级 extra_params 应用结果
func (m *ResponsesResponseFormatModel) buildRequestPlan(ctx context.Context, options *model.Options) (*responsesRequestPlan, error) {
	textConfig, err := resolveResponsesTextConfig(ctx, m.cfg.ResponseFormat)
	if err != nil {
		return nil, err
	}
	extra := m.extra
	if extra == nil {
		extra = &responsesExtraParams{}
	}
	return extra.buildPlan(options, textConfig, m.configExtraFields)
}

// responsesDownstreamOptions 把已解析的通用参数重新编码为 eino options
// 不能直接复用调用方 opts：send_temperature=false 时已清空的温度必须真正丢弃，
// 且任务/模型级默认值只有转成 option 才会被 eino-ext 写进请求体
func responsesDownstreamOptions(options *model.Options, extra []model.Option) []model.Option {
	downstream := make([]model.Option, 0, 10+len(extra))
	if options != nil {
		if options.Model != nil {
			downstream = append(downstream, model.WithModel(*options.Model))
		}
		if options.Temperature != nil {
			downstream = append(downstream, model.WithTemperature(*options.Temperature))
		}
		if options.TopP != nil {
			downstream = append(downstream, model.WithTopP(*options.TopP))
		}
		if options.Tools != nil {
			downstream = append(downstream, model.WithTools(options.Tools))
		}
		if options.DeferredTools != nil {
			downstream = append(downstream, model.WithDeferredTools(options.DeferredTools))
		}
		if options.ToolSearchTool != nil {
			downstream = append(downstream, model.WithToolSearchTool(options.ToolSearchTool))
		}
		if options.MaxTokens != nil {
			downstream = append(downstream, model.WithMaxTokens(*options.MaxTokens))
		}
		if options.Stop != nil {
			downstream = append(downstream, model.WithStop(options.Stop))
		}
		if options.ToolChoice != nil {
			downstream = append(downstream, model.WithToolChoice(*options.ToolChoice, options.AllowedToolNames...))
		}
		if options.AgenticToolChoice != nil {
			downstream = append(downstream, model.WithAgenticToolChoice(options.AgenticToolChoice))
		}
	}
	return append(downstream, extra...)
}

// NewResponsesResponseFormatModel 构造 Responses 结构化输出包装模型。
//
// 字段在包外不可见，因此提供构造器；extra 由
// SplitResponsesExtraParams 产出，类型不导出但可透传。
func NewResponsesResponseFormatModel(inner model.AgenticModel, cfg ModelConfig, baseURL string, extra *responsesExtraParams, configExtraFields map[string]any) *ResponsesResponseFormatModel {
	return &ResponsesResponseFormatModel{
		inner:             inner,
		cfg:               cfg,
		baseURL:           baseURL,
		extra:             extra,
		configExtraFields: configExtraFields,
	}
}
