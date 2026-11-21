package test

import (
	"context"
	"strings"
	"testing"

	"MaiGo/internal/maisaka"
)

type judgePromptSource map[string]string

func (s judgePromptSource) Get(name string) (string, error) {
	return s[name], nil
}

type fakeJudgeClient struct {
	response maisaka.ChatResponse
	requests []maisaka.ChatRequest
}

func (c *fakeJudgeClient) Chat(_ context.Context, request maisaka.ChatRequest) (maisaka.ChatResponse, error) {
	c.requests = append(c.requests, request)
	return c.response, nil
}

func judgeLoop(t *testing.T, client maisaka.ChatClient) *maisaka.ChatLoopService {
	t.Helper()
	return &maisaka.ChatLoopService{
		SessionID:   "session-1",
		Prompts:     judgePromptSource{"planner_action_judge": "判断动作", "planner_action_judge_focus": "判断 Focus 目标"},
		JudgeClient: client,
	}
}

func replyWaitTools(routes ...string) []maisaka.ToolSpec {
	sessionSchema := func() map[string]any {
		properties := map[string]any{"chat_anlyze": map[string]any{"type": "string"}}
		if len(routes) == 0 {
			return map[string]any{"type": "object", "properties": properties}
		}
		values := make([]any, 0, len(routes))
		for _, route := range routes {
			values = append(values, route)
		}
		properties["session_id"] = map[string]any{"type": "string", "enum": values}
		return map[string]any{"type": "object", "properties": properties}
	}
	return []maisaka.ToolSpec{
		{Name: "reply", Parameters: sessionSchema()},
		{Name: "wait", Parameters: sessionSchema()},
	}
}

func judgeHistory() []maisaka.ContextMessage {
	return []maisaka.ContextMessage{{Role: maisaka.RoleUser, Source: maisaka.SourceUser, MessageID: "m1", SessionID: "session-1", Content: "在吗"}}
}

func TestPlannerActionJudgeCompletesReply(t *testing.T) {
	client := &fakeJudgeClient{response: maisaka.ChatResponse{ToolCalls: []maisaka.ToolCall{{
		Name: "reply", Arguments: map[string]any{"chat_anlyze": "用户问在不在"},
	}}, ModelName: "fast"}}
	service := judgeLoop(t, client)
	judgment, err := service.CompleteMissingPlannerAction(context.Background(), maisaka.ChatResponse{Content: "我来回答这个问题"},
		replyWaitTools(), judgeHistory())
	if err != nil {
		t.Fatalf("判断失败: %v", err)
	}
	if judgment == nil || judgment.ToolCall.Name != "reply" {
		t.Fatalf("应补全 reply 调用: %+v", judgment)
	}
	if judgment.ToolCall.Arguments["chat_anlyze"] != "用户问在不在" {
		t.Fatalf("应保留判断后的 chat_anlyze: %+v", judgment.ToolCall.Arguments)
	}
	if judgment.ToolCall.ExtraContent["tool_call_source"] != maisaka.PlannerActionJudgeSource {
		t.Fatalf("应标记动作判断来源: %+v", judgment.ToolCall.ExtraContent)
	}
	if len(client.requests) != 1 || client.requests[0].ModelTaskName != "fast_model" {
		t.Fatalf("应使用 fast_model 发起一次独立请求: %+v", client.requests)
	}
	if tools := client.requests[0].Tools; len(tools) != 2 || tools[0].Name != "reply" || tools[1].Name != "wait" {
		t.Fatalf("判断请求必须声明 reply 和 wait: %+v", tools)
	}
	if !strings.Contains(client.requests[0].Items[1].Text, "planner_text") {
		t.Fatalf("判断载荷缺少 planner_text: %+v", client.requests[0].Items)
	}
}

func TestPlannerActionJudgeFocusUsesRoutedSession(t *testing.T) {
	client := &fakeJudgeClient{response: maisaka.ChatResponse{ToolCalls: []maisaka.ToolCall{{
		Name: "wait", Arguments: map[string]any{"session_id": "session-b"},
	}}}}
	service := judgeLoop(t, client)
	judgment, err := service.CompleteMissingPlannerAction(context.Background(), maisaka.ChatResponse{Content: "先不回复"},
		replyWaitTools("session-a", "session-b"), judgeHistory())
	if err != nil {
		t.Fatalf("判断失败: %v", err)
	}
	if judgment == nil || judgment.ToolCall.Name != "wait" {
		t.Fatalf("应补全 wait 调用: %+v", judgment)
	}
	if judgment.ToolCall.Arguments["session_id"] != "session-b" {
		t.Fatalf("Focus wait 应保留判断后的真实会话: %+v", judgment.ToolCall.Arguments)
	}
	if !strings.Contains(client.requests[0].Items[0].Text, "判断 Focus 目标") {
		t.Fatal("Focus 判断未加载目标选择提示词")
	}
}

func TestPlannerActionJudgeRejectsInvalidCall(t *testing.T) {
	for name, response := range map[string]maisaka.ChatResponse{
		"missing":           {Content: `{"action":"wait","arguments":{}}`},
		"multiple":          {ToolCalls: []maisaka.ToolCall{{Name: "reply"}, {Name: "wait"}}},
		"unknown":           {ToolCalls: []maisaka.ToolCall{{Name: "stop"}}},
		"unknown parameter": {ToolCalls: []maisaka.ToolCall{{Name: "reply", Arguments: map[string]any{"session_id": "session-a", "unknown": true}}}},
		"unknown route":     {ToolCalls: []maisaka.ToolCall{{Name: "reply", Arguments: map[string]any{"session_id": "session-other"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			service := judgeLoop(t, &fakeJudgeClient{response: response})
			if _, err := service.CompleteMissingPlannerAction(context.Background(), maisaka.ChatResponse{Content: "回复"},
				replyWaitTools("session-a"), judgeHistory()); err == nil {
				t.Fatal("非法工具调用应报错")
			}
		})
	}
}

func TestPlannerActionJudgeSkipsWhenToolCallExists(t *testing.T) {
	client := &fakeJudgeClient{}
	service := judgeLoop(t, client)
	judgment, err := service.CompleteMissingPlannerAction(context.Background(), maisaka.ChatResponse{
		Content: "正文", ToolCalls: []maisaka.ToolCall{{CallID: "c1", Name: "query_memory"}},
	}, replyWaitTools(), judgeHistory())
	if err != nil || judgment != nil || len(client.requests) != 0 {
		t.Fatalf("已有工具调用时不应判断: judgment=%+v err=%v requests=%d", judgment, err, len(client.requests))
	}
}

func TestPlannerActionJudgeSkipsPassiveMode(t *testing.T) {
	client := &fakeJudgeClient{}
	service := judgeLoop(t, client)
	passive := true
	service.PassiveReplyerOverride = &passive
	judgment, err := service.CompleteMissingPlannerAction(context.Background(), maisaka.ChatResponse{Content: "指导"},
		replyWaitTools(), judgeHistory())
	if err != nil || judgment != nil || len(client.requests) != 0 {
		t.Fatalf("被动模式不应判断: judgment=%+v err=%v requests=%d", judgment, err, len(client.requests))
	}
}
