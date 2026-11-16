package maisaka

// read_tool_result 在记忆预览启用时保持可用的局部回归测试

import "testing"

func TestReadToolResultEnabledByMemoryQuery(t *testing.T) {
	cfg := BuiltinToolConfig{AMemorixEnabled: true, MemoryQueryEnabled: true}
	if !BuiltinToolEnabledByConfig(string(ToolReadToolResult), cfg) {
		t.Fatal("记忆查询开启时应暴露 read_tool_result")
	}
	cfg.AMemorixEnabled = false
	if BuiltinToolEnabledByConfig(string(ToolReadToolResult), cfg) {
		t.Fatal("A_Memorix 关闭时不应仅凭查询开关暴露 read_tool_result")
	}
	cfg = BuiltinToolConfig{ToolResultCondense: true}
	if !BuiltinToolEnabledByConfig(string(ToolReadToolResult), cfg) {
		t.Fatal("长结果整理开启时应暴露 read_tool_result")
	}
	if BuiltinToolEnabledByConfig(string(ToolReadToolResult), BuiltinToolConfig{}) {
		t.Fatal("全部开关关闭时不应暴露 read_tool_result")
	}
}
