package model_client

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func testDeepSeekConfig() ModelConfig {
	return ModelConfig{
		ClientType:         "openai",
		ProviderName:       "DeepSeek",
		BaseURL:            "https://api.deepseek.com/v1",
		Model:              "deepseek-flash",
		ReasoningParseMode: "none",
	}
}

func TestChatReasoningReplayCapturedFromResponse(t *testing.T) {
	client, err := newChatCompletionsClient(testDeepSeekConfig())
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	if client.reasoningScope == nil {
		t.Fatal("DeepSeek 官方端点应启用推理回放范围")
	}
	raw := []byte(`{"choices":[{"message":{"role":"assistant","content":"答案","reasoning_content":"原生推理"},"finish_reason":"stop"}]}`)
	message, err := client.parseResponse(raw, nil)
	if err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	items, err := ContextItemsFromMessages([]*schema.AgenticMessage{message})
	if err != nil {
		t.Fatalf("投影上下文失败: %v", err)
	}
	var replay *ContextProviderReplayFragment
	for _, item := range items {
		if item.Type == ContextItemReasoning && item.Replay != nil {
			replay = item.Replay
		}
	}
	if replay == nil {
		t.Fatal("未保留原生推理回放片段")
	}
	payload, err := replay.Materialize()
	if err != nil || payload["reasoning_content"] != "原生推理" {
		t.Fatalf("回放内容不符: %#v, %v", payload, err)
	}
}

func TestChatReasoningReplayOnlyForMatchingScope(t *testing.T) {
	client, err := newChatCompletionsClient(testDeepSeekConfig())
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	scope := *client.reasoningScope
	other := scope
	other.ModelIdentifier = "deepseek-v4-pro"
	payload := map[string]any{"reasoning_content": "原生推理"}
	fragment, err := newContextProviderReplayFragment(scope, payload)
	if err != nil {
		t.Fatalf("创建回放片段失败: %v", err)
	}
	item := NewReasoningItem("turn-1", "")
	item.Representation = ""
	item.Replay = &fragment
	items := []ContextItem{
		NewUserMessageItem(ContextContentPart{Type: ContextPartText, Text: "问题"}),
		item,
		NewFunctionCallItem("turn-1", NewContextToolCall("call_doc", "read_document", nil)),
	}
	messages, err := BuildChatMessagesFromContextItemsWithScope(items, true, &scope)
	if err != nil {
		t.Fatalf("构建请求失败: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("消息数量不符: %d", len(messages))
	}
	if messages[1]["reasoning_content"] != "原生推理" {
		t.Fatalf("同范围应回放推理: %#v", messages[1])
	}
	otherMessages, err := BuildChatMessagesFromContextItemsWithScope(items, true, &other)
	if err != nil {
		t.Fatalf("构建请求失败: %v", err)
	}
	for _, message := range otherMessages {
		if _, ok := message["reasoning_content"]; ok {
			t.Fatal("不同模型范围不应回放推理")
		}
	}
}
