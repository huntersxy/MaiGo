package model_client

import "testing"

func TestBuildChatMessagesSkipsOrphanToolOutput(t *testing.T) {
	items := []ContextItem{
		{Type: ContextItemUserMessage, Parts: []ContextContentPart{{Type: ContextPartText, Text: "hi"}}},
		{Type: ContextItemFunctionCallOutput, CallID: "orphan", Output: "stale"},
		{Type: ContextItemFunctionCall, ToolCall: &ContextToolCall{CallID: "call-1", FuncName: "reply"}},
		{Type: ContextItemFunctionCallOutput, CallID: "call-1", Output: "ok"},
	}
	messages, err := BuildChatMessagesFromContextItems(items, true)
	if err != nil {
		t.Fatalf("构造消息失败: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("孤立工具结果应被跳过，实际消息数=%d", len(messages))
	}
	if messages[0]["role"] != "user" {
		t.Fatalf("首条应为 user: %#v", messages[0])
	}
	if messages[1]["role"] != "assistant" {
		t.Fatalf("工具调用应转为 assistant: %#v", messages[1])
	}
	calls, ok := messages[1]["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 1 || calls[0]["id"] != "call-1" {
		t.Fatalf("assistant tool_calls 不符: %#v", messages[1]["tool_calls"])
	}
	if messages[2]["role"] != "tool" || messages[2]["tool_call_id"] != "call-1" {
		t.Fatalf("配对工具结果应保留: %#v", messages[2])
	}
}

func TestBuildChatMessagesDropsToolCallsWithoutOutputs(t *testing.T) {
	items := []ContextItem{
		{Type: ContextItemUserMessage, Parts: []ContextContentPart{{Type: ContextPartText, Text: "hi"}}},
		{Type: ContextItemFunctionCall, ToolCall: &ContextToolCall{CallID: "call-1", FuncName: "reply"}},
		{Type: ContextItemFunctionCall, ToolCall: &ContextToolCall{CallID: "call-2", FuncName: "reply"}},
		{Type: ContextItemFunctionCallOutput, CallID: "call-1", Output: "ok"},
	}
	messages, err := BuildChatMessagesFromContextItems(items, true)
	if err != nil {
		t.Fatalf("构造消息失败: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("消息数不符: %d", len(messages))
	}
	calls, ok := messages[1]["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 1 || calls[0]["id"] != "call-1" {
		t.Fatalf("缺少结果的 tool_call 应被丢弃: %#v", messages[1]["tool_calls"])
	}
	if messages[2]["role"] != "tool" || messages[2]["tool_call_id"] != "call-1" {
		t.Fatalf("保留的工具结果不符: %#v", messages[2])
	}
}
