package runtime

import (
	"reflect"
	"testing"

	"MaiGo/internal/platform_io"
)

func TestOutboundSegmentsForTextAddsSpaceAfterMention(t *testing.T) {
	a := &Agent{}

	gotSingle := a.outboundSegmentsForText("group", 100, "你好", []int64{1001})
	wantSingle := []platformio.Segment{
		{Kind: platformio.SegmentAt, AtUserID: "1001"},
		{Kind: platformio.SegmentText, Text: " 你好"},
	}
	if !reflect.DeepEqual(gotSingle, wantSingle) {
		t.Fatalf("single mention = %#v, want %#v", gotSingle, wantSingle)
	}

	gotAlreadySpaced := a.outboundSegmentsForText("group", 100, " 你好", []int64{1001})
	if !reflect.DeepEqual(gotAlreadySpaced, wantSingle) {
		t.Fatalf("already spaced mention = %#v, want %#v", gotAlreadySpaced, wantSingle)
	}

	gotMulti := a.outboundSegmentsForText("group", 100, "你们好", []int64{1001, 1002})
	wantMulti := []platformio.Segment{
		{Kind: platformio.SegmentAt, AtUserID: "1001"},
		{Kind: platformio.SegmentText, Text: " "},
		{Kind: platformio.SegmentAt, AtUserID: "1002"},
		{Kind: platformio.SegmentText, Text: " 你们好"},
	}
	if !reflect.DeepEqual(gotMulti, wantMulti) {
		t.Fatalf("multi mention = %#v, want %#v", gotMulti, wantMulti)
	}

	gotTextOnly := a.outboundSegmentsForText("group", 100, "普通文本", nil)
	wantTextOnly := []platformio.Segment{
		{Kind: platformio.SegmentText, Text: "普通文本"},
	}
	if !reflect.DeepEqual(gotTextOnly, wantTextOnly) {
		t.Fatalf("text only = %#v, want %#v", gotTextOnly, wantTextOnly)
	}
}
