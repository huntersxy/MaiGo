package model_client

// Provider 原生工具块到 Context Item 的投影
// eino 的 ServerTool/MCP 块在 Go 侧没有独立的 Item 类型，这里按 Python ProviderActivityItem 的语义折叠，
// 不伪造未提供的字段；details 只放可序列化的摘要字符串

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

func projectServerToolCall(call *schema.ServerToolCall, turnID string) ContextItem {
	item := NewProviderActivityItem("server_tool", strings.TrimSpace(call.Name))
	item.Meta.LogicalTurnID = turnID
	item.ActionType = "call"
	item.CallID = call.CallID
	item.Status = "called"
	item.Details = compactJSONDetails(map[string]any{"name": call.Name, "arguments": call.Arguments})
	return item
}

func projectServerToolResult(result *schema.ServerToolResult, turnID string) ContextItem {
	item := NewProviderActivityItem("server_tool", strings.TrimSpace(result.Name))
	item.Meta.LogicalTurnID = turnID
	item.ActionType = "result"
	item.CallID = result.CallID
	item.Status = "completed"
	item.Details = compactJSONDetails(map[string]any{"name": result.Name, "content": result.Content})
	return item
}

func projectMCPToolCall(call *schema.MCPToolCall, turnID string) ContextItem {
	summary := strings.TrimSpace(strings.Trim(call.ServerLabel+"/"+call.Name, "/"))
	item := NewProviderActivityItem("mcp", summary)
	item.Meta.LogicalTurnID = turnID
	item.ActionType = "call"
	item.CallID = call.CallID
	item.Status = "called"
	item.Details = compactJSONDetails(map[string]any{
		"server_label": call.ServerLabel, "name": call.Name, "arguments": call.Arguments,
		"approval_request_id": call.ApprovalRequestID,
	})
	return item
}

func projectMCPToolResult(result *schema.MCPToolResult, turnID string) ContextItem {
	summary := strings.TrimSpace(strings.Trim(result.ServerLabel+"/"+result.Name, "/"))
	item := NewProviderActivityItem("mcp", summary)
	item.Meta.LogicalTurnID = turnID
	item.ActionType = "result"
	item.CallID = result.CallID
	item.Status = "completed"
	if result.Error != nil {
		item.Status = "failed"
		item.Details = compactJSONDetails(map[string]any{"server_label": result.ServerLabel, "name": result.Name, "error": result.Error})
	} else {
		item.Details = compactJSONDetails(map[string]any{"server_label": result.ServerLabel, "name": result.Name, "content": result.Content})
	}
	return item
}

func projectMCPListToolsResult(result *schema.MCPListToolsResult, turnID string) ContextItem {
	item := NewProviderActivityItem("mcp_list_tools", strings.TrimSpace(result.ServerLabel))
	item.Meta.LogicalTurnID = turnID
	item.ActionType = "list_tools"
	item.Status = "completed"
	item.SourceCount = len(result.Tools)
	if result.Error != "" {
		item.Status = "failed"
		item.Details = []string{"error: " + result.Error}
	}
	return item
}

func projectMCPApprovalRequest(req *schema.MCPToolApprovalRequest, turnID string) ContextItem {
	item := NewProviderOpaqueItem("mcp_approval", strings.TrimSpace(req.Name))
	item.Meta.LogicalTurnID = turnID
	item.DisplaySummary = strings.TrimSpace(strings.Trim(req.ServerLabel+"/"+req.Name, "/"))
	item.Details = compactJSONDetails(map[string]any{
		"id": req.ID, "name": req.Name, "arguments": req.Arguments, "server_label": req.ServerLabel,
	})
	return item
}

func projectMCPApprovalResponse(resp *schema.MCPToolApprovalResponse, turnID string) ContextItem {
	status := "rejected"
	if resp.Approve {
		status = "approved"
	}
	item := NewProviderOpaqueItem("mcp_approval", status)
	item.Meta.LogicalTurnID = turnID
	item.Details = compactJSONDetails(map[string]any{
		"approval_request_id": resp.ApprovalRequestID, "approve": resp.Approve, "reason": resp.Reason,
	})
	return item
}

func projectProviderOpaqueBlock(block *schema.ContentBlock, turnID string) ContextItem {
	providerType := string(block.Type)
	summary := ""
	if raw, ok := block.Extra["display_summary"]; ok {
		summary = strings.TrimSpace(fmt.Sprint(raw))
	}
	if summary == "" {
		summary = providerType
	}
	item := NewProviderOpaqueItem(providerType, summary)
	item.Meta.LogicalTurnID = turnID
	if len(block.Extra) > 0 {
		item.ProviderData = map[string]any{"extra": cloneContextValue(block.Extra)}
	}
	return item
}

func compactJSONDetails(values map[string]any) []string {
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	return []string{string(encoded)}
}

func cloneContextValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	return decoded
}
