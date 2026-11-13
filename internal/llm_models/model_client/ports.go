package model_client

// 与父包（llm）之间的端口注入。
//
// 为什么需要：Python 的 model_client/*.py 直接 import 上层的 request_snapshot、
// cache_stats、usage_recording、image_normalizer、image_compression 等模块；
// Python 运行期导入不构成循环，但 Go 编译期不允许子包反向 import 父包。
//
// 解法与 plugin_runtime/capabilities 一致：子包只声明「我需要外部提供什么」，
// 由父包在 init 时注入实现。未注入时降级为「不做这件事」，而不是 panic——
// 这样 model_client 也能被单测单独构造。

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Ports 父包需要提供给客户端实现的能力
type Ports struct {
	// NormalizeContextImages 规范化上下文里的图片（尺寸/格式）
	NormalizeContextImages func(items []ContextItem) []ContextItem
	// CompressContextImages 按目标体积压缩图片，用于 413 重试
	CompressContextImages func(items []ContextItem, targetSize int) []ContextItem
	// ImageCompressionTargetForError 判断错误是否属于「图片过大」并给出目标体积
	ImageCompressionTargetForError func(err error) (int, bool)
	// CaptureRequestSnapshot 落盘失败请求快照
	CaptureRequestSnapshot func(spec RequestSnapshotSpec) error
	// RecordSnapshotAttempt 把本次尝试写进记录器
	RecordSnapshotAttempt func(recorder *AttemptRecorder, spec *RequestSnapshotSpec, started time.Time)
	// WrapStreamSnapshot 给流式读取器挂失败快照记录
	WrapStreamSnapshot func(reader *schema.StreamReader[*schema.AgenticMessage], wrap func(error) error) *schema.StreamReader[*schema.AgenticMessage]
	// RecordGenerationCacheUsage 记录非流式生成的缓存用量
	RecordGenerationCacheUsage func(ctx context.Context, cfg ModelConfig, items []ContextItem, options *model.Options, rawFormat any, message *schema.AgenticMessage)
	// WrapStreamCacheStats 给流式读取器挂缓存统计
	WrapStreamCacheStats func(reader *schema.StreamReader[*schema.AgenticMessage], ctx context.Context, cfg ModelConfig, items []ContextItem, options *model.Options, rawFormat any) *schema.StreamReader[*schema.AgenticMessage]
	// NewUsageRecordingAudioTranscriber 包装音频转写以记录用量
	NewUsageRecordingAudioTranscriber func(inner AudioTranscriber, cfg ModelConfig) AudioTranscriber
	// NewUsageRecordingEmbeddingClient 包装嵌入客户端以记录用量
	NewUsageRecordingEmbeddingClient func(inner EmbeddingClient, cfg ModelConfig) EmbeddingClient
	// WrapUsageRecordingModel 包装模型客户端以记录用量
	WrapUsageRecordingModel func(inner model.AgenticModel, cfg ModelConfig) model.AgenticModel
}

var ports Ports

// Configure 由父包在 init 阶段注入实现；重复调用以最后一次为准
func Configure(p Ports) {
	ports = p
}

// PortsConfigured 是否已完成注入，供测试断言
func PortsConfigured() bool {
	return ports.CaptureRequestSnapshot != nil
}

func normalizeContextImages(items []ContextItem) []ContextItem {
	if ports.NormalizeContextImages == nil {
		return items
	}
	return ports.NormalizeContextImages(items)
}

func compressContextImages(items []ContextItem, targetSize int) []ContextItem {
	if ports.CompressContextImages == nil {
		return items
	}
	return ports.CompressContextImages(items, targetSize)
}

func imageCompressionTargetForError(err error) (int, bool) {
	if ports.ImageCompressionTargetForError == nil {
		return 0, false
	}
	return ports.ImageCompressionTargetForError(err)
}

// captureRequestSnapshot 落盘失败快照并把路径挂到错误链。
//
// 未注入时仍然返回原始错误：调用方普遍写成
// `return nil, c.snapshotFailure(...)`，这里吞掉错误会把失败当成功。
func captureRequestSnapshot(spec RequestSnapshotSpec) error {
	if ports.CaptureRequestSnapshot == nil {
		return spec.Error
	}
	return ports.CaptureRequestSnapshot(spec)
}

func recordSnapshotAttempt(recorder *AttemptRecorder, spec *RequestSnapshotSpec, started time.Time) {
	if ports.RecordSnapshotAttempt == nil {
		return
	}
	ports.RecordSnapshotAttempt(recorder, spec, started)
}

func wrapAgenticStreamSnapshot(reader *schema.StreamReader[*schema.AgenticMessage], wrap func(error) error) *schema.StreamReader[*schema.AgenticMessage] {
	if ports.WrapStreamSnapshot == nil {
		return reader
	}
	return ports.WrapStreamSnapshot(reader, wrap)
}

func recordGenerationCacheUsage(ctx context.Context, cfg ModelConfig, items []ContextItem, options *model.Options, rawFormat any, message *schema.AgenticMessage) {
	if ports.RecordGenerationCacheUsage == nil {
		return
	}
	ports.RecordGenerationCacheUsage(ctx, cfg, items, options, rawFormat, message)
}

func wrapAgenticStreamCacheStats(reader *schema.StreamReader[*schema.AgenticMessage], ctx context.Context, cfg ModelConfig, items []ContextItem, options *model.Options, rawFormat any) *schema.StreamReader[*schema.AgenticMessage] {
	if ports.WrapStreamCacheStats == nil {
		return reader
	}
	return ports.WrapStreamCacheStats(reader, ctx, cfg, items, options, rawFormat)
}

func newUsageRecordingAudioTranscriber(inner AudioTranscriber, cfg ModelConfig) AudioTranscriber {
	if ports.NewUsageRecordingAudioTranscriber == nil {
		return inner
	}
	return ports.NewUsageRecordingAudioTranscriber(inner, cfg)
}

func newUsageRecordingEmbeddingClient(inner EmbeddingClient, cfg ModelConfig) EmbeddingClient {
	if ports.NewUsageRecordingEmbeddingClient == nil {
		return inner
	}
	return ports.NewUsageRecordingEmbeddingClient(inner, cfg)
}

// wrapUsageRecordingModel 在真实客户端外层记录一次实际调用。
//
// 未注入时原样返回：用量统计属于上层服务职责，子包不做。
func wrapUsageRecordingModel(inner model.AgenticModel, cfg ModelConfig) model.AgenticModel {
	if ports.WrapUsageRecordingModel == nil {
		return inner
	}
	return ports.WrapUsageRecordingModel(inner, cfg)
}
