package maisaka

import "testing"

func TestBuildLightweightToolSpecsNarrowsParameters(t *testing.T) {
	specs := []ToolSpec{
		{
			Name:        "query_memory",
			Description: "查询记忆",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":  map[string]any{"type": "string"},
					"limit":  map[string]any{"type": "integer"},
					"person": map[string]any{"type": "string"},
				},
				"required": []any{"query"},
			},
		},
		{
			Name:        "wait",
			Description: "旧描述",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"seconds": map[string]any{"type": "integer"}},
			},
		},
		{Name: "reply", Description: "不应出现"},
	}
	out := BuildLightweightToolSpecs(specs)
	if len(out) != 2 {
		t.Fatalf("只应保留轻量工具: %+v", out)
	}
	memory := out[0]
	properties := memory.Parameters["properties"].(map[string]any)
	if len(properties) != 1 || properties["query"] == nil {
		t.Fatalf("query_memory 应只保留 query 参数: %+v", properties)
	}
	if memory.Parameters["additionalProperties"] != false {
		t.Fatalf("应关闭额外参数: %+v", memory.Parameters)
	}
	required, _ := memory.Parameters["required"].([]any)
	if len(required) != 1 || required[0] != "query" {
		t.Fatalf("query_memory 的 required 应只保留 query: %+v", required)
	}
	if out[1].Description != "不发送消息，静默结束本轮思考并等待新消息；无需回复时单独调用，不填参数。" {
		t.Fatalf("wait 描述应改为轻量模式文案: %q", out[1].Description)
	}
}
