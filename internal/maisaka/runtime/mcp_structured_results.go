package runtime

import (
	"maibot-go/internal/maisaka"
	"maibot-go/internal/mcp_module"

	"github.com/cloudwego/eino/schema"
)

// mcpToolExecutionResult 把 MCP 执行结果投影为 Maisaka 统一工具结果
//
// 转换语义在 maisaka.ConvertMCPToolResult（无 mcp_module 依赖）；这里只做类型投影，
// 避免 maisaka 反向依赖 mcp_module。
func mcpToolExecutionResult(invocation maisaka.ToolInvocation, info *schema.ToolInfo, result mcp.ToolExecutionResult) maisaka.ToolExecutionResult {
	projected := maisaka.MCPToolResult{
		Success:           result.Success,
		Content:           result.Content,
		ErrorMessage:      result.ErrorMessage,
		StructuredContent: result.StructuredContent,
		ServerName:        result.ServerName,
		Metadata:          result.Metadata,
	}
	if info != nil && info.Extra != nil {
		if outputSchema, ok := info.Extra["output_schema"]; ok && outputSchema != nil {
			projected.OutputSchema = outputSchema
		}
	}
	if len(result.ContentItems) > 0 {
		projected.ContentItems = make([]maisaka.MCPContentItem, 0, len(result.ContentItems))
		for _, item := range result.ContentItems {
			projected.ContentItems = append(projected.ContentItems, maisaka.MCPContentItem{
				ContentType: item.ContentType,
				Text:        item.Text,
				Data:        item.Data,
				MIMEType:    item.MIMEType,
				URI:         item.URI,
				Name:        item.Name,
				Description: item.Description,
				Annotation:  item.Annotation,
				Metadata:    item.Metadata,
			})
		}
	}
	return maisaka.ConvertMCPToolResult(invocation.ToolName, projected)
}
