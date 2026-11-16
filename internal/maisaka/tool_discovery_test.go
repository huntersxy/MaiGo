package maisaka

import (
	"strings"
	"testing"
)

func TestSearchToolSpecsRanksExactNameFirst(t *testing.T) {
	specs := []ToolSpec{
		{Name: "web_search", Description: "搜索网页"},
		{Name: "web", Description: "web 工具"},
		{Name: "search", Description: "搜索工具"},
	}
	matches := SearchToolSpecs(specs, "web", 1)
	if len(matches) != 1 || matches[0].Name != "web" {
		t.Fatalf("精确名称应排在最前: %+v", matches)
	}
	matches = SearchToolSpecs(specs, "搜索", 5)
	if len(matches) != 2 {
		t.Fatalf("应命中描述中的关键词: %+v", matches)
	}
}

func TestBuildToolSearchResultCarriesDefinitions(t *testing.T) {
	result := BuildToolSearchResult("web", []DiscoveredToolDefinition{{
		Name:        "web_search",
		Description: "搜索网页",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
	}}, []string{"web_search"})
	if result["status"] != "loaded" {
		t.Fatalf("命中时应标记 loaded: %+v", result)
	}
	if result["instruction"] != ToolLoadedInstruction {
		t.Fatalf("命中时应携带调用指引: %+v", result)
	}
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("应携带完整工具定义: %+v", result)
	}
	tool := tools[0].(map[string]any)
	parameters := tool["parameters"].(map[string]any)
	if parameters["type"] != "object" {
		t.Fatalf("工具参数定义丢失: %+v", tool)
	}
}

func TestDeferredToolsReminderFollowsTailLoading(t *testing.T) {
	runtime := NewRuntime(RuntimeOptions{SessionID: "s"})
	runtime.UpdateDeferredToolSpecs([]ToolSpec{
		{Name: "search", Description: "搜索"},
		{Name: "fetch", Description: "抓取"},
	})
	runtime.DiscoverDeferredTools([]string{"search"})
	reminder := runtime.BuildDeferredToolsReminder()
	if reminder == "" || strings.Contains(reminder, "1. search:") || !strings.Contains(reminder, "1. fetch:") {
		t.Fatalf("普通模式只应提示未发现工具: %s", reminder)
	}
	runtime.SetToolSearchTailLoading(true)
	reminder = runtime.BuildDeferredToolsReminder()
	if !strings.Contains(reminder, "1. fetch:") || !strings.Contains(reminder, "2. search:") {
		t.Fatalf("末尾加载模式应保留完整目录: %s", reminder)
	}
}

func TestBuildToolSearchResultNotFound(t *testing.T) {
	result := BuildToolSearchResult("missing", nil, nil)
	if result["status"] != "not_found" {
		t.Fatalf("未命中时应标记 not_found: %+v", result)
	}
	tools, _ := result["tools"].([]any)
	if len(tools) != 0 {
		t.Fatalf("未命中时不应携带工具定义: %+v", result)
	}
}
