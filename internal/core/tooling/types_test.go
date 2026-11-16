package tooling

import "testing"

func TestFilterToolArgumentsKeepsDeclaredOnly(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"msg_id":          map[string]any{"type": "string"},
			"reply_reference": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
	args := map[string]any{"msg_id": "1", "reply_reference": "x", "unexpected": 3}
	got := FilterToolArguments(args, schema)
	if len(got) != 2 || got["unexpected"] != nil {
		t.Fatalf("过滤结果 = %#v", got)
	}
}

func TestFilterToolArgumentsResolvesComposition(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"allOf": []any{
			map[string]any{"properties": map[string]any{"a": map[string]any{}}},
			map[string]any{"properties": map[string]any{"b": map[string]any{}}},
		},
	}
	got := FilterToolArguments(map[string]any{"a": 1, "b": 2, "c": 3}, schema)
	if len(got) != 2 || got["c"] != nil {
		t.Fatalf("组合 schema 过滤结果 = %#v", got)
	}
}

func TestFilterToolArgumentsHonorsAdditionalProperties(t *testing.T) {
	got := FilterToolArguments(map[string]any{"x": 1}, map[string]any{"additionalProperties": true})
	if len(got) != 1 {
		t.Fatalf("additionalProperties=true 应保留参数: %#v", got)
	}
}
