package maimbridge

import (
	"context"
	"encoding/json"
	"testing"

	"MaiGo/internal/common/message_server"
	"MaiGo/internal/platform_io"
)

// decodeSeg 解出段的 type 与 data，便于断言适配器实际收到的形状
func decodeSeg(t *testing.T, raw []byte) (string, any) {
	t.Helper()
	var envelope struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("段解析失败: %v (%s)", err, raw)
	}
	var data any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("段 data 解析失败: %v (%s)", err, envelope.Data)
	}
	return envelope.Type, data
}

// TestInboundNumericIDsAndTopLevelRoute 适配器用数字 ID 与顶层归属时不应丢帧
func TestInboundNumericIDsAndTopLevelRoute(t *testing.T) {
	raw := []byte(`{
		"account_id": 900001,
		"scope": "conn-1",
		"message_info": {
			"platform": "qq",
			"message_id": 123456789,
			"time": 1794640000,
			"group_info": {"platform": "qq", "group_id": 987654321, "group_name": "测试群"},
			"user_info": {"platform": "qq", "user_id": 10001, "user_nickname": "用户"}
		},
		"message_segment": {"type": "seglist", "data": [
			{"type": "text", "data": "你好"},
			{"type": "at", "data": "10002"},
			{"type": "reply", "data": "555"}
		]}
	}`)
	msg, err := maimmsg.UnmarshalMessage(raw)
	if err != nil {
		t.Fatalf("入站解析失败: %v", err)
	}
	envelope, err := InboundFromMessageBase(msg, "qq")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if envelope.RouteKey.Platform != "qq" || envelope.RouteKey.AccountID != "900001" || envelope.RouteKey.Scope != "conn-1" {
		t.Fatalf("路由键不符: %+v", envelope.RouteKey)
	}
	if envelope.ExternalMessageID != "123456789" {
		t.Fatalf("外部消息 ID 不符: %q", envelope.ExternalMessageID)
	}
	chat := envelope.ChatMessage
	if chat == nil {
		t.Fatal("ChatMessage 为空")
	}
	if chat.ChatType != platformio.ChatTypeGroup || chat.GroupID != "987654321" || chat.UserID != "10001" {
		t.Fatalf("会话归属不符: %+v", chat)
	}
	if len(chat.Segments) != 3 {
		t.Fatalf("段数量不符: %d", len(chat.Segments))
	}
	if chat.Segments[1].Kind != platformio.SegmentAt || chat.Segments[1].AtUserID != "10002" {
		t.Fatalf("@ 段未解析: %+v", chat.Segments[1])
	}
	if chat.Segments[2].Kind != platformio.SegmentReply || chat.Segments[2].Text != "555" {
		t.Fatalf("reply 段未解析: %+v", chat.Segments[2])
	}
}

// TestInboundMediaSegments 图片/语音/文件/dict 段应还原为可消费的媒体与负载
func TestInboundMediaSegments(t *testing.T) {
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	raw := []byte(`{
		"message_info": {"platform": "qq", "message_id": "m1", "user_info": {"platform": "qq", "user_id": "10001"}},
		"message_segment": {"type": "seglist", "data": [
			{"type": "image", "data": "` + png + `"},
			{"type": "voice", "data": "` + png + `"},
			{"type": "file", "data": {"name": "a.txt", "size": "12", "url": "https://example.invalid/a.txt"}},
			{"type": "dict", "data": {"type": "json", "data": {"text": "卡片"}}}
		]}
	}`)
	msg, err := maimmsg.UnmarshalMessage(raw)
	if err != nil {
		t.Fatalf("入站解析失败: %v", err)
	}
	envelope, err := InboundFromMessageBase(msg, "qq")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	chat := envelope.ChatMessage
	if len(chat.Segments) != 4 {
		t.Fatalf("段数量不符: %d", len(chat.Segments))
	}
	if chat.Segments[0].Kind != platformio.SegmentImage || chat.Segments[0].URL == "" {
		t.Fatalf("图片段缺少可下载引用: %+v", chat.Segments[0])
	}
	if chat.Segments[1].Kind != platformio.SegmentVoice || chat.Segments[1].URL == "" {
		t.Fatalf("语音段缺少可下载引用: %+v", chat.Segments[1])
	}
	if chat.Segments[2].Kind != platformio.SegmentFile || chat.Segments[2].Extra["name"] != "a.txt" {
		t.Fatalf("文件段缺少元信息: %+v", chat.Segments[2])
	}
	if chat.Segments[3].Kind != platformio.SegmentDict || chat.Segments[3].Data == nil {
		t.Fatalf("dict 段负载丢失: %+v", chat.Segments[3])
	}
}

// TestOutboundSegmentsMatchPythonModel 出站段类型必须落在 Python 组件模型取值域内
func TestOutboundSegmentsMatchPythonModel(t *testing.T) {
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	outbound := platformio.OutboundMessageEnvelope{
		InternalMessageID: "send_api_1",
		ChatType:          platformio.ChatTypePrivate,
		TargetID:          "10001",
		Segments: []platformio.Segment{
			{Kind: platformio.SegmentAt, AtUserID: "10002"},
			{Kind: platformio.SegmentReply, Text: "555", Extra: map[string]string{"id": "555"}},
			{Kind: platformio.SegmentImage, Extra: map[string]string{"base64": png}},
			{Kind: platformio.SegmentRecord, Extra: map[string]string{"base64": png}},
			{Kind: platformio.SegmentVideo, URL: "https://example.invalid/v.mp4"},
			{Kind: platformio.SegmentFace, Extra: map[string]string{"id": "13"}},
			{Kind: platformio.SegmentText, Text: "hi"},
		},
	}
	route := platformio.RouteKey{Platform: "qq", AccountID: "900001"}
	base, err := OutboundToMessageBase(context.Background(), outbound, route)
	if err != nil {
		t.Fatalf("出站转换失败: %v", err)
	}
	if base.MessageSegment.Type != "seglist" {
		t.Fatalf("顶层段类型必须是 seglist: %s", base.MessageSegment.Type)
	}
	if base.MessageInfo.UserInfo == nil || base.MessageInfo.UserInfo.UserID != "900001" {
		t.Fatalf("user_info 必须是机器人自身: %+v", base.MessageInfo.UserInfo)
	}
	if base.MessageInfo.ReceiverInfo == nil || base.MessageInfo.ReceiverInfo.UserInfo == nil ||
		base.MessageInfo.ReceiverInfo.UserInfo.UserID != "10001" {
		t.Fatalf("receiver_info 缺失私聊目标: %+v", base.MessageInfo.ReceiverInfo)
	}

	var items []json.RawMessage
	if err := json.Unmarshal(base.MessageSegment.Data, &items); err != nil {
		t.Fatalf("seglist 解析失败: %v", err)
	}
	allowed := map[string]bool{
		"text": true, "image": true, "emoji": true, "voice": true,
		"file": true, "at": true, "reply": true, "dict": true, "seglist": true,
	}
	for _, item := range items {
		kind, data := decodeSeg(t, item)
		if !allowed[kind] {
			t.Fatalf("适配器无法识别的段类型: %s", kind)
		}
		switch kind {
		case "at", "reply":
			if _, ok := data.(string); !ok {
				t.Fatalf("%s 段 data 必须是字符串，实际 %T", kind, data)
			}
		case "image", "voice":
			if _, ok := data.(string); !ok {
				t.Fatalf("%s 段 data 必须是 base64 字符串，实际 %T", kind, data)
			}
		}
	}
}

// TestOutboundNumericAgnosticIDs 数字 ID 与字符串 ID 混合的入站应等价
func TestOutboundNumericAgnosticIDs(t *testing.T) {
	numeric := []byte(`{"message_info":{"platform":"qq","message_id":42,"user_info":{"platform":"qq","user_id":10001}},"message_segment":{"type":"text","data":"hi"}}`)
	textual := []byte(`{"message_info":{"platform":"qq","message_id":"42","user_info":{"platform":"qq","user_id":"10001"}},"message_segment":{"type":"text","data":"hi"}}`)
	for name, raw := range map[string][]byte{"numeric": numeric, "textual": textual} {
		msg, err := maimmsg.UnmarshalMessage(raw)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", name, err)
		}
		if msg.MessageInfo.MessageID != "42" || msg.MessageInfo.UserInfo == nil || msg.MessageInfo.UserInfo.UserID != "10001" {
			t.Fatalf("%s 归一化失败: %+v", name, msg.MessageInfo)
		}
	}
}
