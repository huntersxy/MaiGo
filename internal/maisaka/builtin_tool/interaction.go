package tools

import (
	"context"
	"fmt"
	"strings"


	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// ==================== 发言工具 ====================

// SpeakInput 发言的输入参数
type SpeakInput struct {
	// Content 你想说的话
	Content string `json:"content" jsonschema:"description=说话内容"`
	// ReplyTo 要回复的 OneBot 消息 ID（可选）
	ReplyTo int64 `json:"reply_to,omitempty" jsonschema:"description=要回复的 OneBot 消息 ID"`
	// Mentions 要提及的用户 QQ 号列表（可选）
	Mentions []int64 `json:"mentions,omitempty" jsonschema:"description=要@的用户QQ号列表"`
}

// PrivateSpeakInput 私聊发言的输入参数
type PrivateSpeakInput struct {
	Content string `json:"content" jsonschema:"description=说话内容"`
	ReplyTo int64  `json:"reply_to,omitempty" jsonschema:"description=要回复的 OneBot 消息 ID"`
}

// SpeakOutput 发言的输出
type SpeakOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	// Content 本次实际发送的正文，reply 工具用于生成 Python 回执
	Content string `json:"content,omitempty"`
	// ReplyTo 本次回复引用的目标消息 ID
	ReplyTo int64 `json:"reply_to,omitempty"`
	// MsgID 本次回复引用的原始 msg_id 字符串
	MsgID string `json:"msg_id,omitempty"`
}

// speakFunc 发言的实际实现 - 会通过回调实际发送消息
func speakContent(ctx context.Context, content string, replyTo int64, inputMentions []int64) (*SpeakOutput, error) {
	tc := GetToolContext(ctx)
	if tc == nil {
		return nil, NewTerminalToolError(fmt.Errorf("工具上下文未初始化"))
	}
	if tc.SpeakCallback == nil {
		return nil, NewTerminalToolError(fmt.Errorf("发言回调未初始化"))
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("说话内容不能为空")
	}

	mentions := make([]int64, 0, len(inputMentions))
	seenMentions := make(map[int64]struct{}, len(inputMentions))
	for _, userID := range inputMentions {
		if userID <= 0 {
			return nil, fmt.Errorf("包含无效的 mentions")
		}
		if _, seen := seenMentions[userID]; seen {
			continue
		}
		seenMentions[userID] = struct{}{}
		mentions = append(mentions, userID)
	}

	if err := tc.SpeakCallback(ctx, tc.TargetID, content, replyTo, mentions); err != nil {
		return nil, err
	}

	return &SpeakOutput{
		Success: true,
		Message: "发言成功",
	}, nil
}

func speakFunc(ctx context.Context, input *SpeakInput) (*SpeakOutput, error) {
	if input == nil {
		return nil, fmt.Errorf("说话参数不能为空")
	}
	return speakContent(ctx, input.Content, input.ReplyTo, input.Mentions)
}

func privateSpeakFunc(ctx context.Context, input *PrivateSpeakInput) (*SpeakOutput, error) {
	if input == nil {
		return nil, fmt.Errorf("说话参数不能为空")
	}
	return speakContent(ctx, input.Content, input.ReplyTo, nil)
}

// NewSpeakTool 创建发言工具
func NewSpeakTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"speak",
		"在群里说话。",
		speakFunc,
	)
}

// NewPrivateSpeakTool 创建私聊发言工具
func NewPrivateSpeakTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"speak",
		"在私聊里说话。",
		privateSpeakFunc,
	)
}

// ==================== 保持沉默工具 ====================

// StayQuietInput 保持沉默的输入参数
type StayQuietInput struct {
	// Reason 不说话的原因
	Reason string `json:"reason,omitempty" jsonschema:"description=不说话的原因（给自己看的笔记）"`
}

// StayQuietOutput 保持沉默的输出
type StayQuietOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// stayQuietFunc 保持沉默的实际实现
func stayQuietFunc(ctx context.Context, input *StayQuietInput) (*StayQuietOutput, error) {
	return &StayQuietOutput{
		Success: true,
		Message: "保持沉默",
	}, nil
}

// NewStayQuietTool 创建保持沉默工具
func NewStayQuietTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"stayQuiet",
		"结束这一轮，不说话。",
		stayQuietFunc,
	)
}

// ==================== 戳一戳工具 ====================

// PokeInput 戳一戳的输入参数
