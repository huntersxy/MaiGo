package runtime

import (
	"strings"

	"maibot-go/internal/maisaka"
	"maibot-go/internal/maisaka/builtin_tool"
)

// structuredToolExecutionResult 把工具的结构化结果转换为 Maisaka 统一结果
// 对齐 Python ToolExecutionResult：失败只留 error_message，structured_content 始终落 metadata
func structuredToolExecutionResult(invocation maisaka.ToolInvocation, result tools.StructuredToolResult) maisaka.ToolExecutionResult {
	converted := maisaka.ToolExecutionResult{
		ToolName:           invocation.ToolName,
		Success:            result.Success,
		Content:            result.Content,
		PauseTool:          result.PauseTool,
		StopAfterExecution: result.StopAfterExecution,
	}
	metadata := make(map[string]any, len(result.Metadata)+2)
	for key, value := range result.Metadata {
		metadata[key] = value
	}
	if result.StructuredContent != nil {
		metadata["structured_content"] = result.StructuredContent
	}
	if !result.Success {
		converted.Content = ""
		converted.Message = strings.TrimSpace(result.ErrorMessage)
		if converted.Message == "" {
			converted.Message = "工具执行失败。"
		}
		metadata["error_message"] = converted.Message
	}
	if len(metadata) > 0 {
		converted.Metadata = metadata
	}
	return converted
}
