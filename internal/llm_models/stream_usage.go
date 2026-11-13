package llm

import (
	"io"

	"github.com/cloudwego/eino/schema"
)

// trackStreamUsage 在流正常读完时按最后一个非零 usage 累计 token
// 中途关闭或出错的流不统计，避免把未完成响应计入 balance
func trackStreamUsage(router *Router, modelName string, reader *schema.StreamReader[*schema.AgenticMessage]) *schema.StreamReader[*schema.AgenticMessage] {
	if router == nil || modelName == "" || reader == nil {
		return reader
	}
	tokens := 0
	return schema.StreamReaderWithConvert(reader, func(message *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		if count := agenticMessageTokens(message); count > 0 {
			tokens = count
		}
		return message, nil
	}, schema.WithOnEOF(func() (any, error) {
		if tokens > 0 {
			router.adjustModelUsage(modelName, ModelUsage{TotalTokens: tokens})
		}
		return nil, io.EOF
	}))
}
