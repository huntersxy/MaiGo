package model_client

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestBuildChatMessagesMergesConsecutiveAssistant(t *testing.T) {
	input := []*schema.AgenticMessage{
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-1", Name: "reply", Arguments: `{"msg_id":"1"}`}),
			},
		},
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "hello"}),
			},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.FunctionToolResult{
					CallID: "call-1",
					Name:   "reply",
					Content: []*schema.FunctionToolResultContentBlock{{
						Type: schema.FunctionToolResultContentBlockTypeText,
						Text: &schema.UserInputText{Text: "ok"},
					}},
				}),
			},
		},
	}

	messages, err := buildChatMessages(input, true)
	if err != nil {
		t.Fatalf("构造消息失败: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("连续 assistant 应合并为一条，实际消息数=%d", len(messages))
	}
	assistant := messages[0]
	if assistant["role"] != "assistant" || assistant["content"] != "hello" {
		t.Fatalf("assistant 消息不符: %#v", assistant)
	}
	calls, ok := assistant["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 1 || calls[0]["id"] != "call-1" {
		t.Fatalf("assistant tool_calls 不符: %#v", assistant["tool_calls"])
	}
	tool := messages[1]
	if tool["role"] != "tool" || tool["tool_call_id"] != "call-1" {
		t.Fatalf("tool 消息不符: %#v", tool)
	}
}
