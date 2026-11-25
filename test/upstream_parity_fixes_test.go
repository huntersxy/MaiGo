package test

import (
	"context"
	"encoding/json"
	"testing"

	tools "MaiGo/internal/maisaka/builtin_tool"
	runtime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/platform_io"
	maimbridge "MaiGo/internal/platform_io/drivers/maim_bridge"
	"MaiGo/internal/platform_io/drivers/onebot"
)

func TestDecodeExternalPluginMessagePreservesNumericReplyID(t *testing.T) {
	msg, err := runtime.DecodeExternalPluginMessage(map[string]any{
		"platform":    "qq",
		"account_id":  "2971840857",
		"group_id":    "1079643584",
		"user_id":     "123456",
		"message_id":  "200001",
		"raw_message": []any{map[string]any{"type": "reply", "data": map[string]any{"id": "1962381557"}}},
	})
	if err != nil {
		t.Fatalf("DecodeExternalPluginMessage 失败: %v", err)
	}
	if msg.Reply == nil {
		t.Fatal("预期解析出 ReplyInfo")
	}
	if msg.Reply.MessageID != 1962381557 || msg.Reply.ExternalMessageID != "1962381557" {
		t.Fatalf("纯数字回复 ID 不应被哈希: got MessageID=%d ExternalMessageID=%q", msg.Reply.MessageID, msg.Reply.ExternalMessageID)
	}

	extMsg, err := runtime.DecodeExternalPluginMessage(map[string]any{
		"platform":    "discord",
		"account_id":  "bot1",
		"group_id":    "chan1",
		"user_id":     "u1",
		"message_id":  "msg-2",
		"raw_message": []any{map[string]any{"type": "reply", "data": map[string]any{"id": "ext-reply-1"}}},
	})
	if err != nil {
		t.Fatalf("外部 ID 解码失败: %v", err)
	}
	if extMsg.Reply == nil || extMsg.Reply.MessageID != onebot.SyntheticIdentityID("reply", "ext-reply-1") {
		t.Fatalf("非数字回复 ID 应保留合成标识: got %+v", extMsg.Reply)
	}
}

func TestMaimBridgePreservesImageEmojiTypesAndNumericReply(t *testing.T) {
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	outbound := platformio.OutboundMessageEnvelope{
		InternalMessageID: "out-1",
		ChatType:          platformio.ChatTypeGroup,
		TargetID:          "1079643584",
		Segments: []platformio.Segment{
			{Kind: platformio.SegmentImage, Extra: map[string]string{"base64": png}},
			{Kind: platformio.SegmentEmoji, Extra: map[string]string{"base64": png}},
			{Kind: platformio.SegmentRecord, Extra: map[string]string{"base64": png}},
		},
	}
	base, err := maimbridge.OutboundToMessageBase(context.Background(), outbound, platformio.RouteKey{Platform: "qq", AccountID: "2971840857"})
	if err != nil {
		t.Fatalf("OutboundToMessageBase 失败: %v", err)
	}
	var items []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(base.MessageSegment.Data, &items); err != nil {
		t.Fatalf("解析 seglist 失败: %v", err)
	}
	want := []string{"image", "emoji", "voice"}
	if len(items) != len(want) {
		t.Fatalf("段数量 = %d, 期望 %d", len(items), len(want))
	}
	for i := range want {
		if items[i].Type != want[i] {
			t.Fatalf("第 %d 段类型 = %q, 期望 %q", i, items[i].Type, want[i])
		}
	}

	inboundMsg, ok := maimbridge.ConversationMessageFromInbound(platformio.InboundMessageEnvelope{
		DriverID:          maimbridge.DriverIDMaimMsg,
		ExternalMessageID: "200002",
		RouteKey:          platformio.RouteKey{Platform: "qq", AccountID: "2971840857"},
		ChatMessage: &platformio.ChatMessage{
			Platform:  "qq",
			AccountID: "2971840857",
			MessageID: "200002",
			ChatType:  platformio.ChatTypeGroup,
			GroupID:   "1079643584",
			TargetID:  "1079643584",
			UserID:    "123456",
			Segments: []platformio.Segment{
				{Kind: platformio.SegmentAt, AtUserID: "2971840857"},
				{Kind: platformio.SegmentReply, Text: "1962381557", Extra: map[string]string{"id": "1962381557"}},
			},
		},
	})
	if !ok || inboundMsg == nil {
		t.Fatal("ConversationMessageFromInbound 失败")
	}
	if inboundMsg.Reply == nil || inboundMsg.Reply.MessageID != 1962381557 {
		t.Fatalf("maim_bridge 数字回复 ID 不应哈希: got %+v", inboundMsg.Reply)
	}
	if got := inboundMsg.AtNames[2971840857]; got != "" {
		t.Fatalf("缺少群昵称时不应把纯数字 QQ 号写入 AtNames: got %q", got)
	}
}

func TestReplyToolSchemaExcludesInternalBridgeFields(t *testing.T) {
	tool, err := tools.NewReplyTool()
	if err != nil {
		t.Fatal(err)
	}
	tool, err = tools.ApplyMaisakaToolSpec("reply", tool)
	if err != nil {
		t.Fatal(err)
	}
	info, err := tool.Info(context.Background())
	if err != nil || info == nil || info.ParamsOneOf == nil {
		t.Fatalf("获取 reply Schema 失败: info=%+v err=%v", info, err)
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil || js == nil || js.Properties == nil {
		t.Fatalf("导出 JSON Schema 失败: %v", err)
	}
	for _, forbidden := range []string{"_replyer_generated", "content", "text", "reply_to"} {
		if _, exists := js.Properties.Get(forbidden); exists {
			t.Fatalf("reply 工具对 LLM 暴露了内部字段 %q", forbidden)
		}
	}
}
