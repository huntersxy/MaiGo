package context

import (
	"fmt"
	"strings"
	"time"

	maisaka "MaiGo/internal/maisaka"
)

// 参考消息类型，对齐 Python ReferenceMessageType
const (
	ReferenceContextRestore = "context_restore"
	ReferenceCustom         = "custom"
	ReferenceJargon         = "jargon"
	ReferenceMemory         = "memory"
	ReferenceToolHint       = "tool_hint"
)

// ToolResultMediaKind 工具结果媒体消息的 metadata.kind 标记
const ToolResultMediaKind = maisaka.ToolResultMediaKind

// NewReferenceMessage 构造一条参考消息
func NewReferenceMessage(content string, referenceType string, displayPrefix string) maisaka.ContextMessage {
	metadata := map[string]any{}
	if referenceType != "" {
		metadata["reference_type"] = referenceType
	}
	if displayPrefix != "" {
		metadata["display_prefix"] = displayPrefix
	}
	return maisaka.ContextMessage{
		Role:      maisaka.RoleReference,
		Content:   content,
		Source:    maisaka.SourceReference,
		Timestamp: time.Now(),
		Metadata:  metadata,
	}
}

// IsReferenceMessage 判断历史条目是否是参考消息
func IsReferenceMessage(message maisaka.ContextMessage) bool {
	return message.Role == maisaka.RoleReference
}

// IsToolResultMessage 判断历史条目是否是工具结果消息
func IsToolResultMessage(message maisaka.ContextMessage) bool {
	return message.Role == maisaka.RoleTool || message.Source == maisaka.SourceToolResult
}

// IsModelOutputMessage 判断历史条目是否是模型输出
func IsModelOutputMessage(message maisaka.ContextMessage) bool {
	return message.Role == maisaka.RoleAssistant
}

// CountInContext 判断历史条目是否计入真实收发条数
func CountInContext(message maisaka.ContextMessage) bool {
	return message.CountInContext
}

// CloneContextMessage 深拷贝一条历史消息
func CloneContextMessage(message maisaka.ContextMessage) maisaka.ContextMessage {
	clone := message
	if message.ToolCalls != nil {
		clone.ToolCalls = make([]maisaka.ToolCall, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			arguments := map[string]any{}
			for key, value := range call.Arguments {
				arguments[key] = value
			}
			call.Arguments = arguments
			if call.ExtraContent != nil {
				extraContent := make(map[string]any, len(call.ExtraContent))
				for key, value := range call.ExtraContent {
					extraContent[key] = value
				}
				call.ExtraContent = extraContent
			}
			clone.ToolCalls[index] = call
		}
	}
	if message.Images != nil {
		clone.Images = append([]maisaka.ImageComponent{}, message.Images...)
	}
	if message.Metadata != nil {
		clone.Metadata = make(map[string]any, len(message.Metadata))
		for key, value := range message.Metadata {
			clone.Metadata[key] = value
		}
	}
	if message.ReceivedAt != nil {
		receivedAt := *message.ReceivedAt
		clone.ReceivedAt = &receivedAt
	}
	return clone
}

// BuildHistoryMessage 按输入构造一条历史消息的通用入口
type BuildHistoryMessage func(message maisaka.ContextMessage) maisaka.ContextMessage

// FormatToolResultContent 按成功与否格式化工具结果正文
func FormatToolResultContent(success bool, content, message string) string {
	status := "success"
	if !success {
		status = "failed"
	}
	body := strings.TrimSpace(content)
	if body == "" {
		body = strings.TrimSpace(message)
	}
	return fmt.Sprintf("[tool_result status=%s]\n%s", status, body)
}

// LogicalTurnID 返回历史条目的逻辑轮次标记
func LogicalTurnID(message maisaka.ContextMessage) string {
	return strings.TrimSpace(message.LogicalTurnID)
}
