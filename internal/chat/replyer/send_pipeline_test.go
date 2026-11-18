package replyer

import (
	"context"
	"testing"
)

func TestSendSegmentsCarriesReplyAnchor(t *testing.T) {
	var sent []OutboundMessage
	hooks := SendHooks{
		AnchorReplyTo: 900,
		Send: func(_ context.Context, msg *OutboundMessage) (int64, uint64, bool, error) {
			sent = append(sent, *msg)
			return int64(100 + len(sent)), 0, true, nil
		},
	}
	segments := []Segment{{Text: "第一段"}, {Text: "第二段"}}
	if _, err := SendSegments(context.Background(), SendKindGroup, 1, segments, 900, nil, hooks); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("发送条数 = %d", len(sent))
	}
	for index, msg := range sent {
		if msg.ReplyAnchor != 900 {
			t.Fatalf("第 %d 段锚点 = %d, 期望 900", index, msg.ReplyAnchor)
		}
	}
	if sent[0].ReplyTo != 900 || sent[1].ReplyTo != 0 {
		t.Fatalf("显示引用 = %d/%d", sent[0].ReplyTo, sent[1].ReplyTo)
	}
}

func TestSendSegmentsQuotePreviousKeepsAnchor(t *testing.T) {
	var sent []OutboundMessage
	hooks := SendHooks{
		AnchorReplyTo:        900,
		QuotePreviousAllowed: true,
		Send: func(_ context.Context, msg *OutboundMessage) (int64, uint64, bool, error) {
			sent = append(sent, *msg)
			return int64(100 + len(sent)), 0, true, nil
		},
	}
	segments := []Segment{{Text: "第一段"}, {Text: "第二段", QuotePrevious: true}}
	if _, err := SendSegments(context.Background(), SendKindGroup, 1, segments, 900, nil, hooks); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("发送条数 = %d", len(sent))
	}
	if sent[1].ReplyTo != 101 {
		t.Fatalf("第二段引用 = %d, 期望上一段返回的 101", sent[1].ReplyTo)
	}
	if sent[1].ReplyAnchor != 900 {
		t.Fatalf("第二段锚点 = %d, 期望 900", sent[1].ReplyAnchor)
	}
}
