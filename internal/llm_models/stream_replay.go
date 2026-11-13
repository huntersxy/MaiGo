package llm

// 流式请求的整请求重放
// 对齐 MaiBot utils_model.LLMOrchestrator._attempt_request_on_model（utils_model.py:1083-1289）与
// gemini_client._default_stream_response_handler（gemini_client.py:640-662）：
// Python 先把整条流缓冲成单个 APIResponse，建流后中途失败时丢弃已缓冲内容并按同一重试预算重放整个请求；
// 重试耗尽才把错误交给调用方，绝不把半截流当成功返回
//
// Go 的 eino 流在 failover / usage 包装层执行同一语义：一次尝试内先读完并缓冲整条流，
// 可重试错误触发整请求重放，成功后再把缓冲块回放给调用方

import (
	"errors"
	"io"

	"github.com/cloudwego/eino/schema"
)

// drainAgenticStream 读完一次流尝试并返回全部增量块
// 中途错误原样上抛，已收到的部分块一并丢弃（不向调用方泄漏半截内容）；流关闭由本函数负责
func drainAgenticStream(reader *schema.StreamReader[*schema.AgenticMessage]) ([]*schema.AgenticMessage, error) {
	if reader == nil {
		return nil, nil
	}
	defer reader.Close()
	messages := make([]*schema.AgenticMessage, 0, 8)
	for {
		message, err := reader.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return messages, nil
			}
			return nil, err
		}
		messages = append(messages, message)
	}
}

// replayAgenticStream 用缓冲块构造可回放流
func replayAgenticStream(messages []*schema.AgenticMessage) *schema.StreamReader[*schema.AgenticMessage] {
	return schema.StreamReaderFromArray(messages)
}
