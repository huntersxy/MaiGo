package maisaka

import "strings"

// MCPToolResult MCP 工具执行结果的宿主无关投影
//
// mcp_module 的执行结果包含大量协议字段；本类型只保留 Maisaka 工具链需要的部分，
// 由 runtime 负责把 mcp_module 的具体类型投影过来，避免 maisaka 反向依赖 mcp_module。
type MCPToolResult struct {
	Success           bool
	Content           string
	ErrorMessage      string
	StructuredContent any
	ServerName        string
	OutputSchema      any
	ContentItems      []MCPContentItem
	Metadata          map[string]any
}

// MCPContentItem MCP 内容项投影
type MCPContentItem struct {
	ContentType string
	Text        string
	Data        string
	MIMEType    string
	URI         string
	Name        string
	Description string
	Annotation  any
	Metadata    map[string]any
}

// ConvertMCPToolResult 把 MCP 结果投影为 Maisaka 统一工具结果
//
// 对齐 Python MCP 工具调用后的结构化结果透传：文本保持与原 InvokableRun 兼容，
// 同时保留 structuredContent / content_items / output_schema。
func ConvertMCPToolResult(toolName string, result MCPToolResult) ToolExecutionResult {
	converted := ToolExecutionResult{
		ToolName: toolName,
		Success:  result.Success,
		Content:  result.Content,
	}
	if !result.Success {
		message := strings.TrimSpace(result.ErrorMessage)
		if message == "" {
			message = "工具执行失败。"
		}
		converted.Content = ""
		converted.Message = message
	}
	if len(result.ContentItems) > 0 {
		converted.Items = make([]ToolResultItem, 0, len(result.ContentItems))
		for _, item := range result.ContentItems {
			converted.Items = append(converted.Items, convertMCPContentItem(item))
		}
	}
	metadata := make(map[string]any, len(result.Metadata)+3)
	for key, value := range result.Metadata {
		metadata[key] = value
	}
	if result.StructuredContent != nil {
		metadata["structured_content"] = result.StructuredContent
	}
	if result.ServerName != "" {
		metadata["server_name"] = result.ServerName
	}
	if result.OutputSchema != nil {
		metadata["output_schema"] = result.OutputSchema
	}
	if len(metadata) > 0 {
		converted.Metadata = metadata
	}
	return converted
}

func convertMCPContentItem(item MCPContentItem) ToolResultItem {
	converted := ToolResultItem{
		Type:     item.ContentType,
		Text:     item.Text,
		Data:     item.Data,
		MIMEType: item.MIMEType,
	}
	metadata := make(map[string]any, len(item.Metadata)+4)
	for key, value := range item.Metadata {
		metadata[key] = value
	}
	if item.URI != "" {
		metadata["uri"] = item.URI
	}
	if item.Name != "" {
		metadata["name"] = item.Name
	}
	if item.Description != "" {
		metadata["description"] = item.Description
	}
	if item.Annotation != nil {
		metadata["annotation"] = item.Annotation
	}
	if len(metadata) > 0 {
		converted.Metadata = metadata
	}
	return converted
}

// trimSpaceText 去掉首尾空白，保持与 runtime 包的实现一致
func trimSpaceText(text string) string {
	start, end := 0, len(text)
	for start < end && isSpaceByte(text[start]) {
		start++
	}
	for end > start && isSpaceByte(text[end-1]) {
		end--
	}
	return text[start:end]
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}
