package llm

import (
	"context"
	"io"
	"sync"
	"time"

	"MaiGo/internal/services/model_stats"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// usageSink 是 llm 层写统计的端口；生产实现落到 modelstats，测试可替换
type usageSink interface {
	RecordModelUsage(record modelstats.ModelUsageRecord)
	RecordModelTaskUsage(record modelstats.ModelTaskUsageRecord)
}

type modelstatsUsageSink struct{}

func (modelstatsUsageSink) RecordModelUsage(record modelstats.ModelUsageRecord) {
	if recorder := modelstats.Default(); recorder != nil {
		recorder.RecordModelUsage(record)
	}
}

func (modelstatsUsageSink) RecordModelTaskUsage(record modelstats.ModelTaskUsageRecord) {
	if recorder := modelstats.Default(); recorder != nil {
		recorder.RecordModelTaskUsage(record)
	}
}

var (
	usageSinkMu   sync.RWMutex
	usageSinkTest usageSink
)

func activeUsageSink() usageSink {
	usageSinkMu.RLock()
	override := usageSinkTest
	usageSinkMu.RUnlock()
	if override != nil {
		return override
	}
	return modelstatsUsageSink{}
}

func setUsageSinkForTest(sink usageSink) func() {
	usageSinkMu.Lock()
	previous := usageSinkTest
	usageSinkTest = sink
	usageSinkMu.Unlock()
	return func() {
		usageSinkMu.Lock()
		usageSinkTest = previous
		usageSinkMu.Unlock()
	}
}

// usageRecordingModel 在真实 Provider 客户端外层记录每次实际调用
type usageRecordingModel struct {
	inner model.AgenticModel
	cfg   ModelConfig
}

func newUsageRecordingModel(inner model.AgenticModel, cfg ModelConfig) model.AgenticModel {
	if inner == nil {
		return nil
	}
	return &usageRecordingModel{inner: inner, cfg: cfg}
}

func (m *usageRecordingModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	ctx = m.recordingContext(ctx)
	started := time.Now()
	message, err := m.inner.Generate(ctx, input, opts...)
	if err != nil {
		m.recordFailure(ctx, started)
		return nil, err
	}
	m.recordSuccess(ctx, started, message)
	return message, nil
}

func (m *usageRecordingModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	ctx = m.recordingContext(ctx)
	started := time.Now()
	inner, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		m.recordFailure(ctx, started)
		return nil, err
	}
	if inner == nil {
		m.recordSuccess(ctx, started, nil)
		return nil, nil
	}
	reader, writer := schema.Pipe[*schema.AgenticMessage](8)
	go func() {
		defer writer.Close()
		defer inner.Close()
		var last *schema.AgenticMessage
		for {
			message, recvErr := inner.Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					m.recordSuccess(ctx, started, last)
				} else {
					m.recordFailure(ctx, started)
				}
				writer.Send(nil, recvErr)
				return
			}
			if message != nil {
				last = message
			}
			if writer.Send(message, nil) {
				// 消费方提前关闭：视为未完成调用，与失败重试统计一致
				m.recordFailure(ctx, started)
				return
			}
		}
	}()
	return reader, nil
}

// recordingContext 标记本次调用由包装器写统计，避免 eino 内置回调重复计数
func (m *usageRecordingModel) recordingContext(ctx context.Context) context.Context {
	task, _, _ := resolveUsageMetadata(ctx, m.cfg)
	if task == "" || m.cfg.Model == "" {
		return ctx
	}
	return modelstats.WithClientUsageRecording(ctx)
}

// innerModel 暴露被包装客户端，供 failover 判断内置图片压缩能力
func (m *usageRecordingModel) innerModel() model.AgenticModel {
	if m == nil {
		return nil
	}
	return m.inner
}

func (m *usageRecordingModel) recordSuccess(ctx context.Context, started time.Time, message *schema.AgenticMessage) {
	var usage *schema.TokenUsage
	if message != nil && message.ResponseMeta != nil {
		usage = message.ResponseMeta.TokenUsage
	}
	// 对齐 Python record_usage_to_database：仅在响应带 usage 时写 llm_usage；
	// 调用次数与失败次数独立于 usage 上报，均写 model_call_hourly
	m.recordModelCall(ctx, started, usage, false)
	m.recordTaskUsage(ctx, started, true)
}

func (m *usageRecordingModel) recordFailure(ctx context.Context, started time.Time) {
	// llm_usage 不写失败；model_call_hourly 与 model_task_usage 记录失败尝试
	m.recordModelCall(ctx, started, nil, true)
	m.recordTaskUsage(ctx, started, false)
}

// recordModelCall 累加一次实际 Provider 调用；usage 为空时只计次数
func (m *usageRecordingModel) recordModelCall(ctx context.Context, started time.Time, usage *schema.TokenUsage, failed bool) {
	task, requestType, sessionID := resolveUsageMetadata(ctx, m.cfg)
	recordModelCallUsage(activeUsageSink(), m.cfg, task, requestType, sessionID, started, usage, failed)
}

// recordModelCallUsage 写 model_call_hourly；失败与无 usage 的成功调用也计次
func recordModelCallUsage(sink usageSink, cfg ModelConfig, task, requestType, sessionID string, started time.Time, usage *schema.TokenUsage, failed bool) {
	if task == "" || cfg.Model == "" {
		return
	}
	record := modelstats.ModelUsageRecord{
		Timestamp:       time.Now(),
		Task:            task,
		Model:           cfg.ModelDisplayName,
		ModelIdentifier: cfg.Model,
		Provider:        cfg.ProviderName,
		SessionID:       sessionID,
		RequestType:     requestType,
		Duration:        time.Since(started),
		Failed:          failed,
		UsageReported:   usage != nil,
	}
	if usage != nil {
		hit, miss, cacheEnabled := cachedTokenFields(usage)
		record.CacheEnabled = cacheEnabled
		record.CacheHitTokens = hit
		record.CacheMissTokens = miss
		record.PromptTokens = int64(usage.PromptTokens)
		record.CompletionTokens = int64(usage.CompletionTokens)
		record.TotalTokens = int64(usage.TotalTokens)
		record.Cost = calculateModelUsageCost(cfg, usage, started)
	}
	sink.RecordModelUsage(record)
}

func (m *usageRecordingModel) recordTaskUsage(ctx context.Context, started time.Time, succeeded bool) {
	task, _, _ := resolveUsageMetadata(ctx, m.cfg)
	if task == "" || m.cfg.Model == "" {
		return
	}
	activeUsageSink().RecordModelTaskUsage(modelstats.ModelTaskUsageRecord{
		Timestamp:       time.Now(),
		Task:            task,
		Model:           m.cfg.ModelDisplayName,
		ModelIdentifier: m.cfg.Model,
		Provider:        m.cfg.ProviderName,
		Duration:        time.Since(started),
		Succeeded:       succeeded,
	})
}

// usageRecordingEmbeddingClient 记录每次实际嵌入请求
type usageRecordingEmbeddingClient struct {
	inner EmbeddingClient
	cfg   ModelConfig
}

func newUsageRecordingEmbeddingClient(inner EmbeddingClient, cfg ModelConfig) EmbeddingClient {
	if inner == nil {
		return nil
	}
	return &usageRecordingEmbeddingClient{inner: inner, cfg: cfg}
}

func (c *usageRecordingEmbeddingClient) EmbedText(ctx context.Context, text string) (EmbeddingResult, error) {
	return c.record(ctx, c.cfg.TaskName, c.cfg.RequestType, c.cfg.SessionID, func() (EmbeddingResult, error) {
		return c.inner.EmbedText(ctx, text)
	})
}

func (c *usageRecordingEmbeddingClient) EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return nil, errorEmbeddingInputEmpty
	}
	out := make([]EmbeddingResult, 0, len(texts))
	for _, text := range texts {
		row, err := c.EmbedText(ctx, text)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (c *usageRecordingEmbeddingClient) EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error) {
	return embedTextsConcurrently(ctx, texts, maxConcurrent, c.EmbedText)
}

func (c *usageRecordingEmbeddingClient) EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error) {
	task, requestType, sessionID := c.cfg.TaskName, c.cfg.RequestType, c.cfg.SessionID
	if trimTask(input.TaskName) != "" {
		task = input.TaskName
	}
	if trimTask(input.RequestType) != "" {
		requestType = input.RequestType
	}
	if trimTask(input.SessionID) != "" {
		sessionID = input.SessionID
	}
	return c.record(ctx, task, requestType, sessionID, func() (EmbeddingResult, error) {
		return c.inner.EmbedImage(ctx, input)
	})
}

func (c *usageRecordingEmbeddingClient) record(ctx context.Context, task, requestType, sessionID string, call func() (EmbeddingResult, error)) (EmbeddingResult, error) {
	started := time.Now()
	result, err := call()
	task, requestType, sessionID = resolveUsageMetadataValues(ctx, c.cfg, task, requestType, sessionID)
	if task == "" || c.cfg.Model == "" {
		return result, err
	}
	sink := activeUsageSink()
	var usage *schema.TokenUsage
	if err == nil {
		usage = result.Usage
	}
	recordModelCallUsage(sink, c.cfg, task, requestType, sessionID, started, usage, err != nil)
	sink.RecordModelTaskUsage(modelstats.ModelTaskUsageRecord{
		Timestamp:       time.Now(),
		Task:            task,
		Model:           c.cfg.ModelDisplayName,
		ModelIdentifier: c.cfg.Model,
		Provider:        c.cfg.ProviderName,
		Duration:        time.Since(started),
		Succeeded:       err == nil,
	})
	return result, err
}

// usageRecordingAudioTranscriber 记录每次实际语音转写请求
type usageRecordingAudioTranscriber struct {
	inner AudioTranscriber
	cfg   ModelConfig
}

func newUsageRecordingAudioTranscriber(inner AudioTranscriber, cfg ModelConfig) AudioTranscriber {
	if inner == nil {
		return nil
	}
	return &usageRecordingAudioTranscriber{inner: inner, cfg: cfg}
}

func (t *usageRecordingAudioTranscriber) Transcribe(ctx context.Context, req AudioTranscriptionRequest) (AudioTranscriptionResult, error) {
	started := time.Now()
	result, err := t.inner.Transcribe(ctx, req)
	task, requestType, sessionID := resolveUsageMetadata(ctx, t.cfg)
	if task == "" || t.cfg.Model == "" {
		return result, err
	}
	sink := activeUsageSink()
	var usage *schema.TokenUsage
	if err == nil {
		usage = result.Usage
	}
	recordModelCallUsage(sink, t.cfg, task, requestType, sessionID, started, usage, err != nil)
	sink.RecordModelTaskUsage(modelstats.ModelTaskUsageRecord{
		Timestamp:       time.Now(),
		Task:            task,
		Model:           t.cfg.ModelDisplayName,
		ModelIdentifier: t.cfg.Model,
		Provider:        t.cfg.ProviderName,
		Duration:        time.Since(started),
		Succeeded:       err == nil,
	})
	return result, err
}

func resolveUsageMetadataValues(ctx context.Context, cfg ModelConfig, task, requestType, sessionID string) (string, string, string) {
	defaultTask, defaultRequestType, defaultSessionID := resolveUsageMetadata(ctx, cfg)
	if trimTask(task) == "" {
		task = defaultTask
	}
	if trimTask(requestType) == "" {
		requestType = defaultRequestType
	}
	if trimTask(sessionID) == "" {
		sessionID = defaultSessionID
	}
	return trimTask(task), trimTask(requestType), trimTask(sessionID)
}

var errorEmbeddingInputEmpty = errEmbeddingInputEmpty{}

type errEmbeddingInputEmpty struct{}

func (errEmbeddingInputEmpty) Error() string { return "嵌入输入列表不能为空" }
