package model_client

import (
	"github.com/cloudwego/eino/components/model"
)

// imageCompressionRetryPayload 对 413 / data URI 单项超限错误构造一次压缩后的请求体。
// 返回的 sentLegacyMaxTokens 用于之后 400 max_completion_tokens 回退判断。
func (c *chatCompletionsClient) imageCompressionRetryPayload(err error, items []ContextItem, options *model.Options, stream bool) (map[string]any, []ContextItem, bool, bool) {
	targetSize, ok := imageCompressionTargetForError(err)
	if !ok || len(items) == 0 {
		return nil, nil, false, false
	}
	compressed := compressContextImages(items, targetSize)
	payload, sentLegacyMaxTokens, buildErr := c.buildRequestBodyFromContextItems(compressed, options, stream, c.requiresMaxCompletionTokens())
	if buildErr != nil {
		return nil, nil, false, false
	}
	return payload, compressed, sentLegacyMaxTokens, true
}
