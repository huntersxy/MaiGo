package maisaka

import (
	"context"
	"strings"
	"testing"
)

type staticPromptSource map[string]string

func (s staticPromptSource) Get(name string) (string, error) {
	return s[name], nil
}

type fakeJudgeClient struct {
	response ChatResponse
	requests []ChatRequest
}

func (c *fakeJudgeClient) Chat(_ context.Context, request ChatRequest) (ChatResponse, error) {
	c.requests = append(c.requests, request)
	return c.response, nil
}

func judgeLoop(t *testing.T, client ChatClient) *ChatLoopService {
	t.Helper()
	return &ChatLoopService{
		SessionID:   "session-1",
		Prompts:     staticPromptSource{"planner_action_judge": "判断动作"},
		JudgeClient: client,
	}
}

func replyWaitTools(routes ...string) []ToolSpec {
	sessionSchema := func() map[string]any {
		if len(routes) == 0 {
			return map[string]any{"type": "object", "properties": map[string]any{}}
		}
		values := make([]any, 0, len(routes))
		for _, route := range routes {
			values = append(values, route)
		}
		return map[string]any{"type": "object", "properties": map[string]any{
			"session_id": map[string]any{"type": "string", "enum": values},
		}}
	}
	return []ToolSpec{
		{Name: "reply", Parameters: sessionSchema()},
		{Name: "wait", Parameters: sessionSchema()},
	}
}

func judgeHistory() []ContextMessage {
	return []ContextMessage{{Role: RoleUser, Source: SourceUser, MessageID: "m1", SessionID: "session-1", Content: "在吗"}}
}

func TestPlannerActionJudgeCompletesReply(t *testing.T) {
	client := &fakeJudgeClient{response: ChatResponse{Content: `{"action":"reply"}`, ModelName: "fast"}}
	service := judgeLoop(t, client)
	judgment, err := service.CompleteMissingPlannerAction(context.Background(), ChatResponse{Content: "我来回答这个问题"},
		replyWaitTools(), judgeHistory())
	if err != nil {
		t.Fatalf("判断失败: %v", err)
	}
	if judgment == nil || judgment.ToolCall.Name != "reply" {
		t.Fatalf("应补全 reply 调用: %+v", judgment)
	}
	if judgment.ToolCall.Arguments["reply_reference"] != "我来回答这个问题" {
		t.Fatalf("reply_reference 应使用 Planner 正文: %+v", judgment.ToolCall.Arguments)
	}
	if judgment.ToolCall.ExtraContent["tool_call_source"] != PlannerActionJudgeSource {
		t.Fatalf("应标记动作判断来源: %+v", judgment.ToolCall.ExtraContent)
	}
	if len(client.requests) != 1 || client.requests[0].ModelTaskName != "fast_model" {
		t.Fatalf("应使用 fast_model 发起一次独立请求: %+v", client.requests)
	}
	if !strings.Contains(client.requests[0].Items[1].Text, "planner_text") {
		t.Fatalf("判断载荷缺少 planner_text: %+v", client.requests[0].Items)
	}
}

func TestPlannerActionJudgeFocusUsesRoutedSession(t *testing.T) {
	client := &fakeJudgeClient{response: ChatResponse{Content: `{"action":"wait"}`}}
	service := judgeLoop(t, client)
	judgment, err := service.CompleteMissingPlannerAction(context.Background(), ChatResponse{Content: "先不回复"},
		replyWaitTools("session-a", "session-b"), judgeHistory())
	if err != nil {
		t.Fatalf("判断失败: %v", err)
	}
	if judgment == nil || judgment.ToolCall.Name != "wait" {
		t.Fatalf("应补全 wait 调用: %+v", judgment)
	}
	if judgment.ToolCall.Arguments["session_id"] != "session-a" {
		t.Fatalf("Focus wait 应指定首个真实会话: %+v", judgment.ToolCall.Arguments)
	}
}

func TestPlannerActionJudgeRejectsInvalidJSON(t *testing.T) {
	client := &fakeJudgeClient{response: ChatResponse{Content: "reply"}}
	service := judgeLoop(t, client)
	if _, err := service.CompleteMissingPlannerAction(context.Background(), ChatResponse{Content: "回复"},
		replyWaitTools(), judgeHistory()); err == nil {
		t.Fatal("非 JSON 判断结果应报错")
	}
}

func TestPlannerActionJudgeSkipsWhenToolCallExists(t *testing.T) {
	client := &fakeJudgeClient{response: ChatResponse{Content: `{"action":"reply"}`}}
	service := judgeLoop(t, client)
	judgment, err := service.CompleteMissingPlannerAction(context.Background(), ChatResponse{
		Content: "正文", ToolCalls: []ToolCall{{CallID: "c1", Name: "query_memory"}},
	}, replyWaitTools(), judgeHistory())
	if err != nil || judgment != nil || len(client.requests) != 0 {
		t.Fatalf("已有工具调用时不应判断: judgment=%+v err=%v requests=%d", judgment, err, len(client.requests))
	}
}

func TestPlannerActionJudgeSkipsPassiveMode(t *testing.T) {
	client := &fakeJudgeClient{response: ChatResponse{Content: `{"action":"reply"}`}}
	service := judgeLoop(t, client)
	passive := true
	service.PassiveReplyerOverride = &passive
	judgment, err := service.CompleteMissingPlannerAction(context.Background(), ChatResponse{Content: "指导"},
		replyWaitTools(), judgeHistory())
	if err != nil || judgment != nil || len(client.requests) != 0 {
		t.Fatalf("被动模式不应判断: judgment=%+v err=%v requests=%d", judgment, err, len(client.requests))
	}
}
