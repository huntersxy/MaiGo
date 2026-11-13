package llm

// 单模型回退路径的 413 / data URI 图片压缩重试
// binding、活跃档案、web_search 与空 model_list 不经过 failoverModel，
// 这里补一层与失败切换层相同的“本模型压缩后重发一次”语义。

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// imageCompressionRetryModel 在未内置压缩重试的客户端外层补一次压缩重发
type imageCompressionRetryModel struct {
	inner model.AgenticModel
}

func newImageCompressionRetryModel(inner model.AgenticModel) model.AgenticModel {
	if inner == nil {
		return nil
	}
	if _, ok := unwrapImageCompressionClient(inner).(interface{ usesBuiltinImageCompression() }); ok {
		return inner
	}
	return &imageCompressionRetryModel{inner: inner}
}

func (m *imageCompressionRetryModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	message, err := m.inner.Generate(ctx, input, opts...)
	if compressed, ok := imageCompressionRetryInput(err, input, m.inner); ok {
		return m.inner.Generate(ctx, compressed, opts...)
	}
	return message, err
}

func (m *imageCompressionRetryModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	reader, err := m.inner.Stream(ctx, input, opts...)
	if compressed, ok := imageCompressionRetryInput(err, input, m.inner); ok {
		return m.inner.Stream(ctx, compressed, opts...)
	}
	return reader, err
}

// imageCompressionRetryInput 判断错误是否可按图片过大处理并返回压缩后的输入；
// 已内置重试的 Chat Completions 客户端不再重复压缩
func imageCompressionRetryInput(err error, input []*schema.AgenticMessage, client model.AgenticModel) ([]*schema.AgenticMessage, bool) {
	if err == nil || client == nil {
		return nil, false
	}
	if _, ok := unwrapImageCompressionClient(client).(interface{ usesBuiltinImageCompression() }); ok {
		return nil, false
	}
	target, ok := imageCompressionTargetForError(err)
	if !ok {
		return nil, false
	}
	return compressAgenticMessages(input, target)
}

// unwrapImageCompressionClient 穿透 usage 包装，识别底层客户端是否内置换算逻辑
func unwrapImageCompressionClient(client model.AgenticModel) model.AgenticModel {
	for {
		switch wrapped := client.(type) {
		case *usageModel:
			if wrapped == nil {
				return client
			}
			client = wrapped.inner
		case interface{ innerModel() model.AgenticModel }:
			inner := wrapped.innerModel()
			if inner == nil {
				return client
			}
			client = inner
		default:
			return client
		}
	}
}
