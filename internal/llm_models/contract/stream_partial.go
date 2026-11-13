package contract

import (
	"context"
	"strings"
	"sync"
)

// StreamPartialToolCall 流式工具调用的未完成片段
type StreamPartialToolCall struct {
	Name      string
	Arguments string
}

// StreamPartialOutput 对齐 MaiBot InterruptedStreamOutput：
// 只保留已收到的原始片段，不尝试修复或执行半截工具参数。
type StreamPartialOutput struct {
	Reasoning string
	Content   string
	ToolCalls []StreamPartialToolCall
	ModelName string
}

// DisplayText 拼装展示文本，对齐 InterruptedStreamOutput.display_text
func (o *StreamPartialOutput) DisplayText() string {
	if o == nil {
		return ""
	}
	sections := make([]string, 0, 2+len(o.ToolCalls))
	if o.Reasoning != "" {
		sections = append(sections, "[已接收的 reasoning 片段]\n"+o.Reasoning)
	}
	if o.Content != "" {
		sections = append(sections, "[已接收的正文片段]\n"+o.Content)
	}
	for _, call := range o.ToolCalls {
		name := call.Name
		if strings.TrimSpace(name) == "" {
			name = "尚未收到名称"
		}
		sections = append(sections, "[未执行的工具调用片段："+name+"]\n"+call.Arguments)
	}
	return strings.Join(sections, "\n\n")
}

// StreamPartialSink 承接底层流在中断时的未完成输出
//
// 只有协议客户端所在的 goroutine 写入，中断检测方读取，读写用互斥锁隔离。
type StreamPartialSink struct {
	mu    sync.Mutex
	value *StreamPartialOutput
}

// Set 记录一次中断快照
func (s *StreamPartialSink) Set(value StreamPartialOutput) {
	if s == nil {
		return
	}
	s.mu.Lock()
	copied := value
	s.value = &copied
	s.mu.Unlock()
}

// Snapshot 读取已记录的中断快照
func (s *StreamPartialSink) Snapshot() *StreamPartialOutput {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value == nil {
		return nil
	}
	copied := *s.value
	return &copied
}

type streamPartialSinkKey struct{}

// WithStreamPartialSink 在请求上下文注入中断快照接收器
func WithStreamPartialSink(ctx context.Context, sink *StreamPartialSink) context.Context {
	if ctx == nil || sink == nil {
		return ctx
	}
	return context.WithValue(ctx, streamPartialSinkKey{}, sink)
}

// StreamPartialSinkFromContext 读取请求上下文里的中断快照接收器
func StreamPartialSinkFromContext(ctx context.Context) *StreamPartialSink {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(streamPartialSinkKey{}).(*StreamPartialSink)
	return sink
}
