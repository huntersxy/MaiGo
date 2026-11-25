package test

import (
	"context"
	"strings"
	"testing"
	"time"

	messagereceive "MaiGo/internal/chat/message_receive"
	"MaiGo/internal/chat/replyer"
	maisakactx "MaiGo/internal/maisaka/context"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/platform_io/drivers/snowluma"
)

func TestAtMentionRenderAtTextAddsTrailingSpace(t *testing.T) {
	if got := maisakactx.RenderAtText("Alice", "a", "1001"); got != "@Alice " {
		t.Fatalf("RenderAtText(card) = %q, want %q", got, "@Alice ")
	}
	if got := maisakactx.RenderAtText("", "Bob", "1002"); got != "@Bob " {
		t.Fatalf("RenderAtText(nick) = %q, want %q", got, "@Bob ")
	}
	if got := maisakactx.RenderAtText("", "", "1003"); got != "@1003 " {
		t.Fatalf("RenderAtText(id) = %q, want %q", got, "@1003 ")
	}
}

func TestAtMentionReplyerAddsTrailingSpace(t *testing.T) {
	isGroup := true
	builder := &replyer.PromptBuilder{
		Config: replyer.GeneratorConfig{
			IsGroupChat: &isGroup,
			BotName:     "麦麦",
		},
		PromptLoader: func(name string, vars map[string]string) (string, error) {
			return vars["dialogue_prompt"], nil
		},
	}

	target := &replyer.ReplyTarget{
		MessageID:  "1",
		SenderName: "Alice",
		Parts: []replyer.MessagePart{
			{Kind: replyer.PartAt, TargetUserCardname: "Bob"},
			{Kind: replyer.PartText, Text: "你好"},
		},
	}
	if got := builder.BuildTargetMessageContent(target); got != "@Bob 你好" {
		t.Fatalf("BuildTargetMessageContent = %q, want %q", got, "@Bob 你好")
	}

	history := []replyer.ChatMessage{
		{
			Role:       "user",
			SourceKind: "user",
			MessageID:  "1",
			SenderName: "Alice",
			Timestamp:  time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC),
			Parts: []replyer.MessagePart{
				{Kind: replyer.PartAt, TargetUserNickname: "Carl"},
				{Kind: replyer.PartText, Text: "看看"},
			},
		},
	}
	if got := builder.BuildRetroDialogueBlock(history); !strings.Contains(got, "@Carl 看看") {
		t.Fatalf("BuildRetroDialogueBlock = %q, want substring %q", got, "@Carl 看看")
	}

	msgs, err := builder.RequestMessages(context.Background(), replyer.ReplyRequest{
		EnableVisualMessage: true,
		ThinkLevel:          1,
		Target:              target,
		History:             history,
	})
	if err != nil {
		t.Fatalf("RequestMessages error: %v", err)
	}
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content, "@Carl 看看") {
		t.Fatalf("RequestMessages(visual) = %#v, want substring %q", msgs, "@Carl 看看")
	}
}

func TestAtMentionMessageReceiveAddsTrailingSpace(t *testing.T) {
	resolve := func(_ context.Context, _ *onebot.ConversationMessage, uid int64) string {
		if uid == 1001 {
			return "Alice"
		}
		return ""
	}

	msg := &onebot.ConversationMessage{
		MessageParts: []onebot.MessagePart{
			{Kind: "at", AtUserID: 1001},
			{Kind: "text", Text: "你好"},
		},
	}
	pieces, _, _ := messagereceive.ParseMessageContent(context.Background(), msg, resolve)
	if got := messagereceive.RenderContentPieces(pieces, nil); got != "@Alice 你好" {
		t.Fatalf("RenderContentPieces(no space) = %q, want %q", got, "@Alice 你好")
	}

	msgWithLeadingSpace := &onebot.ConversationMessage{
		MessageParts: []onebot.MessagePart{
			{Kind: "at", AtUserID: 1001},
			{Kind: "text", Text: " 你好"},
		},
	}
	piecesWithSpace, _, _ := messagereceive.ParseMessageContent(context.Background(), msgWithLeadingSpace, resolve)
	if got := messagereceive.RenderContentPieces(piecesWithSpace, nil); got != "@Alice 你好" {
		t.Fatalf("RenderContentPieces(leading space) = %q, want %q", got, "@Alice 你好")
	}

	msgAll := &onebot.ConversationMessage{
		MessageParts: []onebot.MessagePart{
			{Kind: "at", AtUserID: onebot.AtAllUserID},
			{Kind: "text", Text: "开会"},
		},
	}
	piecesAll, _, _ := messagereceive.ParseMessageContent(context.Background(), msgAll, nil)
	if got := messagereceive.RenderContentPieces(piecesAll, nil); got != "@全体成员 开会" {
		t.Fatalf("RenderContentPieces(at all) = %q, want %q", got, "@全体成员 开会")
	}
}

func TestAtMentionSnowlumaInboundAddsTrailingSpace(t *testing.T) {
	payload := map[string]any{
		"post_type":    "message",
		"message_type": "group",
		"message_id":   "10",
		"user_id":      "1001",
		"group_id":     "2001",
		"self_id":      "9999",
		"time":         float64(1700000000),
		"sender": map[string]any{
			"user_id":  "1001",
			"nickname": "Alice",
		},
		"message": []any{
			map[string]any{
				"type": "at",
				"data": map[string]any{"qq": "1002", "name": "Bob"},
			},
			map[string]any{
				"type": "text",
				"data": map[string]any{"text": "你好"},
			},
		},
	}
	out := snowluma.NormalizeInboundMessage(payload, "9999", "")
	if out == nil {
		t.Fatal("NormalizeInboundMessage returned nil")
	}
	got, _ := out["processed_plain_text"].(string)
	if got != "@Bob 你好" && got != "@1002 你好" {
		t.Fatalf("processed_plain_text = %q, want %q or %q", got, "@Bob 你好", "@1002 你好")
	}
}
