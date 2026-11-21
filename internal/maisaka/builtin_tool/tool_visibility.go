package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// deferredToolMarker 由动态工具实现，用于在工具目录中标记 deferred
type deferredToolMarker interface {
	IsDeferredTool() bool
}

// CollectDeferredToolInfos 按目录顺序收集 deferred 工具的 ToolInfo
func CollectDeferredToolInfos(ctx context.Context, chatTools []tool.BaseTool, catalog []ToolCatalogEntry) []*schema.ToolInfo {
	if len(chatTools) == 0 || len(catalog) == 0 {
		return nil
	}
	deferred := make(map[string]struct{}, len(catalog))
	for _, entry := range catalog {
		if entry.Deferred {
			deferred[entry.Name] = struct{}{}
		}
	}
	if len(deferred) == 0 {
		return nil
	}
	out := make([]*schema.ToolInfo, 0, len(deferred))
	seen := make(map[string]struct{}, len(deferred))
	for _, item := range chatTools {
		if item == nil {
			continue
		}
		info, err := item.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		if _, ok := deferred[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, info)
	}
	return out
}

// IsDeferredTool 判断工具是否属于 deferred 目录
func (tc *ToolContext) IsDeferredTool(name string) bool {
	if tc == nil {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, entry := range tc.ToolCatalog {
		if entry.Name == name {
			return entry.Deferred
		}
	}
	return false
}

// IsToolDiscovered 判断 deferred 工具是否已被 tool_search 发现
func (tc *ToolContext) IsToolDiscovered(name string) bool {
	if tc == nil {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	tc.discoveredMu.Lock()
	defer tc.discoveredMu.Unlock()
	_, ok := tc.DiscoveredTools[name]
	return ok
}

// SyncDiscoveredToolsFromMessages 从当前模型上下文中的 tool_search 结果同步发现状态
//
// 对齐 MaiBot sync_discovered_deferred_tools_with_context：只有上下文中仍保留
// tool_search 的完整结果时，对应 deferred 工具才继续可见；结果被裁掉后自动折回 deferred
func (tc *ToolContext) SyncDiscoveredToolsFromMessages(messages []*schema.AgenticMessage) {
	if tc == nil {
		return
	}
	deferred := make(map[string]struct{})
	for _, entry := range tc.ToolCatalog {
		if entry.Deferred {
			deferred[entry.Name] = struct{}{}
		}
	}
	discovered := make(map[string]struct{})
	for _, message := range messages {
		content, ok := toolSearchResultContent(message)
		if !ok {
			continue
		}
		var payload struct {
			MatchedToolNames []string `json:"matched_tool_names"`
		}
		if err := sonic.UnmarshalString(content, &payload); err != nil {
			continue
		}
		for _, raw := range payload.MatchedToolNames {
			name := strings.TrimSpace(raw)
			if _, ok := deferred[name]; ok {
				discovered[name] = struct{}{}
			}
		}
	}
	tc.discoveredMu.Lock()
	if len(discovered) == 0 {
		tc.DiscoveredTools = nil
	} else {
		tc.DiscoveredTools = discovered
	}
	tc.discoveredMu.Unlock()
}

// FilterVisibleToolInfos 从顶层声明排除按需加载工具。
func FilterVisibleToolInfos(tc *ToolContext, infos []*schema.ToolInfo) []*schema.ToolInfo {
	if tc == nil || len(tc.ToolCatalog) == 0 {
		return infos
	}
	visible := make([]*schema.ToolInfo, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		if tc.IsDeferredTool(name) {
			continue
		}
		visible = append(visible, info)
	}
	return visible
}

// DeferredToolsReminder 构造按需加载工具的稳定目录。
func DeferredToolsReminder(tc *ToolContext) string {
	if tc == nil {
		return ""
	}
	var lines []string
	for _, entry := range tc.ToolCatalog {
		if !entry.Deferred {
			continue
		}
		if desc := strings.TrimSpace(entry.Description); desc != "" {
			lines = append(lines, fmt.Sprintf("%d. %s: %s", len(lines)+1, entry.Name, desc))
		} else {
			lines = append(lines, fmt.Sprintf("%d. %s", len(lines)+1, entry.Name))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join([]string{
		"<system-reminder>",
		"以下工具当前未直接暴露给你，但可以通过 tool_search 工具发现并在后续轮次中使用：",
		strings.Join(lines, "\n"),
		"",
		"如需其中某个工具，请先调用 tool_search。tool_search 只负责发现工具，不直接执行。",
		"</system-reminder>",
	}, "\n")
}

func toolSearchResultContent(message *schema.AgenticMessage) (string, bool) {
	if message == nil {
		return "", false
	}
	for _, block := range message.ContentBlocks {
		if block == nil || block.FunctionToolResult == nil {
			continue
		}
		if strings.TrimSpace(block.FunctionToolResult.Name) != "tool_search" {
			continue
		}
		var builder strings.Builder
		for _, part := range block.FunctionToolResult.Content {
			if part != nil && part.Text != nil {
				builder.WriteString(part.Text.Text)
			}
		}
		return builder.String(), true
	}
	return "", false
}
