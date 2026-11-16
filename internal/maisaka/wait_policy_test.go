package maisaka

import "testing"

func TestHasTerminalWaitToolCall(t *testing.T) {
	if !hasTerminalWaitToolCall([]ToolCall{{Name: "wait", Arguments: map[string]any{}}}) {
		t.Fatal("无参数 wait 应视为终止信号")
	}
	if hasTerminalWaitToolCall([]ToolCall{{Name: "wait", Arguments: map[string]any{"seconds": 30}}}) {
		t.Fatal("定时 wait 不应提前截断")
	}
	if hasTerminalWaitToolCall([]ToolCall{{Name: "reply", Arguments: map[string]any{}}}) {
		t.Fatal("非 wait 工具不应视为终止信号")
	}
}

func TestReplyWaitCompletionBecomesReferenceMessage(t *testing.T) {
	runtime := NewRuntime(RuntimeOptions{SessionID: "s"})
	seconds := 0.0
	if err := runtime.EnterWaitState(&seconds, "call-1", "turn-1", true); err != nil {
		t.Fatalf("进入回复后等待失败: %v", err)
	}
	message := NewReasoningEngine(runtime).BuildWaitCompletedMessage(false)
	if message.Role != RoleReference || message.MetadataString("display_prefix") != "[回复后等待结束]" {
		t.Fatalf("回复后等待应追加参考消息: %+v", message)
	}
	if runtime.PendingWaitAfterReply() {
		t.Fatal("消费等待状态后应清除 after_reply 标记")
	}
}

func TestCycleEndForMergedWaitBehavior(t *testing.T) {
	cases := map[string]string{
		"wait:end":       CycleEndPlannerNoTool,
		"wait":           CycleEndToolPauseWait,
		"reply:wait:end": CycleEndPlannerNoTool,
		"reply:wait":     CycleEndToolPauseWait,
	}
	for pause, want := range cases {
		if got := cycleEndForPauseTool(pause).Reason; got != want {
			t.Fatalf("%s 的结束原因应为 %s，实际 %s", pause, want, got)
		}
	}
}
