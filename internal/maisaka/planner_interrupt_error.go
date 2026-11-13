package maisaka

import (
	"errors"
	"strings"
)

// ErrPlannerInterrupted 表示 Planner 请求被新消息打断，调用方应结束本轮而不是视为模型故障
var ErrPlannerInterrupted = errors.New("Planner 被新消息打断")

// InterruptedToolCall 流式工具调用的未完成片段，只作展示不执行
type InterruptedToolCall struct {
	Name      string
	Arguments string
}

// InterruptedStreamOutput 对齐 MaiBot InterruptedStreamOutput：
// 保留已收到的原始片段，不修复、不执行半截工具参数。
type InterruptedStreamOutput struct {
	Reasoning string
	Content   string
	ToolCalls []InterruptedToolCall
	ModelName string
}

// DisplayText 拼装展示文本，对齐 InterruptedStreamOutput.display_text
func (o *InterruptedStreamOutput) DisplayText() string {
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
		name := strings.TrimSpace(call.Name)
		if name == "" {
			name = "尚未收到名称"
		}
		sections = append(sections, "[未执行的工具调用片段："+name+"]\n"+call.Arguments)
	}
	return strings.Join(sections, "\n\n")
}

// PlannerInterruptError 携带未完成输出的 Planner 打断错误
//
// errors.Is(err, ErrPlannerInterrupted) 仍成立，调用方按打断结束本轮；
// Partial 仅流式客户端在上报打断时填充。
type PlannerInterruptError struct {
	Partial *InterruptedStreamOutput
}

func (e *PlannerInterruptError) Error() string {
	return ErrPlannerInterrupted.Error()
}

// Unwrap 让 errors.Is/As 继续识别 ErrPlannerInterrupted
func (e *PlannerInterruptError) Unwrap() error { return ErrPlannerInterrupted }
