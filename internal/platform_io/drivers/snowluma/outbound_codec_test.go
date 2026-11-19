package snowluma

import (
	"testing"

	"MaiGo/internal/platform_io"
)

func TestEncodeOutboundSegmentsKeepsReplySegment(t *testing.T) {
	outbound := platformio.OutboundMessageEnvelope{
		Segments: []platformio.Segment{
			{Kind: platformio.SegmentReply, Text: "612184341", Extra: map[string]string{"id": "612184341"}},
			{Kind: platformio.SegmentText, Text: "你怎么知道我天天看这个"},
		},
	}
	segments := EncodeOutboundSegments(outbound, ClientProfile{})
	if len(segments) != 2 {
		t.Fatalf("段数 = %d, 期望 2: %+v", len(segments), segments)
	}
	if got := segments[0]["type"]; got != "reply" {
		t.Fatalf("首段类型 = %v, 期望 reply", got)
	}
	data, ok := segments[0]["data"].(map[string]any)
	if !ok || data["id"] != "612184341" {
		t.Fatalf("引用数据 = %#v, 期望 id=612184341", segments[0]["data"])
	}
	if got := segments[1]["type"]; got != "text" {
		t.Fatalf("次段类型 = %v, 期望 text", got)
	}
}

func TestEncodeOutboundSegmentsReplyWithoutIDIsDropped(t *testing.T) {
	outbound := platformio.OutboundMessageEnvelope{
		Segments: []platformio.Segment{
			{Kind: platformio.SegmentReply},
			{Kind: platformio.SegmentText, Text: "正文"},
		},
	}
	segments := EncodeOutboundSegments(outbound, ClientProfile{})
	if len(segments) != 1 || segments[0]["type"] != "text" {
		t.Fatalf("无 id 的引用段应被丢弃，实际: %+v", segments)
	}
}
