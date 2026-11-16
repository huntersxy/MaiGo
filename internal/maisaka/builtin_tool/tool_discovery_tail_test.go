package tools

import (
	"strings"
	"testing"
)

func TestConvertToolSearchResultTailLoading(t *testing.T) {
	result := convertToolSearchResult(map[string]any{
		"success":                     true,
		"query":                       "web",
		"matched_tool_names":          []any{"web_search"},
		"newly_discovered_tool_names": []any{"web_search"},
		"status":                      "loaded",
		"instruction":                 "直接调用",
		"tools":                       []any{map[string]any{"name": "web_search", "parameters": map[string]any{"type": "object"}}},
	})
	if !result.Success {
		t.Fatal("tail loading 结果应成功")
	}
	if !strings.Contains(result.Content, `"status":"loaded"`) || !strings.Contains(result.Content, `"web_search"`) {
		t.Fatalf("正文为空时应序列化结构化结果: %s", result.Content)
	}
	structured, _ := result.StructuredContent.(map[string]any)
	if structured["status"] != "loaded" {
		t.Fatalf("结构化结果缺少 status: %+v", structured)
	}
	detail, _ := result.Metadata["monitor_detail"].(map[string]any)
	if detail["status"] != "loaded" {
		t.Fatalf("监控详情应复用结构化结果: %+v", detail)
	}
}

func TestMergedWaitAndReplySpecs(t *testing.T) {
	waitSpec, ok := MaisakaToolSpecFor("wait")
	if !ok {
		t.Fatal("缺少 wait 工具声明")
	}
	waitSchema := waitSpec.Schema
	if _, required := waitSchema["required"]; required {
		t.Fatalf("wait 不应有必填参数: %+v", waitSchema)
	}
	if waitSchema["additionalProperties"] != false {
		t.Fatalf("wait 应关闭额外参数: %+v", waitSchema)
	}
	replySpec, ok := MaisakaToolSpecFor("reply")
	if !ok {
		t.Fatal("缺少 reply 工具声明")
	}
	properties, _ := replySpec.Schema["properties"].(map[string]any)
	afterReply, _ := properties["after_reply"].(map[string]any)
	enum, _ := afterReply["enum"].([]any)
	if len(enum) != 2 || enum[0] != "continue" || enum[1] != "wait" {
		t.Fatalf("reply.after_reply 只允许 continue/wait: %+v", afterReply)
	}
	if afterReply["default"] != "wait" {
		t.Fatalf("reply.after_reply 默认应为 wait: %+v", afterReply)
	}
	if _, ok := MaisakaToolSpecFor("stop"); ok {
		t.Fatal("stop 工具声明应已移除")
	}
}

func TestConvertWaitResultNoArgumentEndsTurn(t *testing.T) {
	result := convertWaitResult(map[string]any{"success": true, "content": "已结束本轮思考，等待新消息。"})
	if !result.Success || result.PauseTool != "wait" {
		t.Fatalf("无参数 wait 应暂停本轮: %+v", result)
	}
	if _, exists := result.Metadata["wait_seconds"]; exists {
		t.Fatalf("无参数 wait 不应伪造等待时长: %+v", result.Metadata)
	}
	timed := convertWaitResult(map[string]any{"success": true, "content": "等待", "wait_seconds": float64(5)})
	if timed.Metadata["wait_seconds"] != 5 {
		t.Fatalf("定时 wait 应保留秒数: %+v", timed.Metadata)
	}
}
