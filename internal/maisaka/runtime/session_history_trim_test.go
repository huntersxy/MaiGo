package runtime

import (
	"testing"

	"MaiGo/internal/maisaka"
	"MaiGo/internal/platform_io/drivers/onebot"

	"github.com/cloudwego/eino/schema"
)

func trimTestKindMessage(kind string) *schema.AgenticMessage {
	message := schema.UserAgenticMessage(kind)
	message.Extra = map[string]any{"kind": kind}
	return message
}

func TestApplyTrimmedHistory(t *testing.T) {
	session := &Session{
		kind:        "group",
		targetID:    1,
		latestLogID: 200,
		seen: []sessionEntry{
			{msg: trimTestKindMessage(kindSystemPrompt), transcriptID: 101, visibleSeq: 5},
			{msg: schema.UserAgenticMessage("较早的用户消息"), user: &sessionUser{LogID: 100}, transcriptID: 102, visibleSeq: 6},
			{msg: &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}, transcriptID: 103, visibleSeq: 7},
			{msg: schema.UserAgenticMessage("较早的工具结果"), transcriptID: 104, visibleSeq: 7},
			{msg: trimTestKindMessage("reference"), transcriptID: 105},
			{msg: schema.UserAgenticMessage("未确认回执"), transcriptID: 106},
			{msg: trimTestKindMessage(kindSystemNote), transcriptID: 107, visibleSeq: 8},
			{msg: schema.UserAgenticMessage("最新用户消息"), user: &sessionUser{LogID: 200}, transcriptID: 108, visibleSeq: 9},
			{msg: schema.UserAgenticMessage("中间的用户消息"), user: &sessionUser{LogID: 150}, transcriptID: 109, visibleSeq: 8},
			{msg: schema.UserAgenticMessage("拆分的文本"), transcriptID: 110, visibleSeq: 10},
			{msg: schema.UserAgenticMessage("拆分的工具结果"), transcriptID: 110, visibleSeq: 10},
			{msg: schema.UserAgenticMessage("中间被归一化删除的孤儿工具结果"), transcriptID: 111, visibleSeq: 10},
		},
		pending: []*sessionUser{{LogID: 10, TranscriptID: 900}},
	}
	original := []maisaka.ContextMessage{
		{TranscriptID: 101}, {TranscriptID: 102}, {TranscriptID: 103}, {TranscriptID: 104},
		{TranscriptID: 105}, {TranscriptID: 106}, {TranscriptID: 107}, {TranscriptID: 108},
		{TranscriptID: 109}, {TranscriptID: 110}, {TranscriptID: 110}, {TranscriptID: 111},
	}
	final := []maisaka.ContextMessage{
		{TranscriptID: 0},
		{TranscriptID: 101}, {TranscriptID: 105}, {TranscriptID: 106},
		{TranscriptID: 110},
	}
	latestBefore := session.LatestLogID()

	if removed := session.ApplyTrimmedHistory(original, final); removed != 6 {
		t.Fatalf("删除条数 = %d, 期望 6", removed)
	}
	wantIDs := []uint{101, 105, 106, 107, 110, 110}
	if len(session.seen) != len(wantIDs) {
		t.Fatalf("工作集剩余 %d 条, 期望 %d 条: %+v", len(session.seen), len(wantIDs), session.seen)
	}
	for i, want := range wantIDs {
		if got := session.seen[i].transcriptID; got != want {
			t.Fatalf("工作集第 %d 条行号 = %d, 期望 %d", i, got, want)
		}
	}
	if len(session.pending) != 1 || session.pending[0].TranscriptID != 900 {
		t.Fatalf("pending 被改动: %+v", session.pending)
	}
	// 水位独立于工作集，裁切后不回落
	if latest := session.LatestLogID(); latest != latestBefore {
		t.Fatalf("LatestLogID = %d, 期望保持 %d", latest, latestBefore)
	}
}

func TestLatestLogIDSurvivesTrim(t *testing.T) {
	session := &Session{kind: "group", targetID: 1}
	session.AppendUser(&onebot.ConversationMessage{MessageID: 1, Content: "一"}, 42, nil)
	session.AppendUser(&onebot.ConversationMessage{MessageID: 2, Content: "二"}, 77, nil)
	if got := session.LatestLogID(); got != 77 {
		t.Fatalf("LatestLogID = %d, 期望 77", got)
	}
	session.mu.Lock()
	original := make([]maisaka.ContextMessage, 0, len(session.seen)+len(session.pending))
	for _, entry := range session.seen {
		original = append(original, maisaka.ContextMessage{TranscriptID: entry.transcriptID})
	}
	for _, user := range session.pending {
		original = append(original, maisaka.ContextMessage{TranscriptID: user.TranscriptID})
	}
	session.mu.Unlock()

	session.ApplyTrimmedHistory(original, nil)
	if got := session.LatestLogID(); got != 77 {
		t.Fatalf("裁切后 LatestLogID = %d, 期望保持 77", got)
	}
}

func TestAgenticMessagesToMaisakaStampsTranscriptID(t *testing.T) {
	toolResult := schema.NewContentBlock(&schema.FunctionToolResult{
		CallID: "call-1",
		Name:   "search",
		Content: []*schema.FunctionToolResultContentBlock{{
			Type: schema.FunctionToolResultContentBlockTypeText,
			Text: &schema.UserInputText{Text: "工具结果"},
		}},
	})
	userText := schema.NewContentBlock(&schema.UserInputText{Text: "用户文本"})
	messages := []*schema.AgenticMessage{
		{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{toolResult, userText}},
		{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "回复"})}},
		schema.SystemAgenticMessage("系统提示"),
	}

	history := agenticMessagesToMaisakaHistory(messages, []uint{42, 7, 5})
	if len(history) != 3 {
		t.Fatalf("历史条数 = %d, 期望 3: %+v", len(history), history)
	}
	if history[0].Role != maisaka.RoleTool || history[0].TranscriptID != 42 {
		t.Fatalf("工具结果 = %+v, 期望带行号 42", history[0])
	}
	if history[1].Role != maisaka.RoleUser || history[1].TranscriptID != 42 {
		t.Fatalf("同源用户文本 = %+v, 期望共享行号 42", history[1])
	}
	if history[2].Role != maisaka.RoleAssistant || history[2].TranscriptID != 7 {
		t.Fatalf("助手消息 = %+v, 期望带行号 7", history[2])
	}

	short := agenticMessagesToMaisakaHistory(messages[:1], nil)
	if len(short) != 2 {
		t.Fatalf("缺行号时历史条数 = %d, 期望 2", len(short))
	}
	for _, message := range short {
		if message.TranscriptID != 0 {
			t.Fatalf("缺位行号 = %d, 期望 0: %+v", message.TranscriptID, message)
		}
	}
}
