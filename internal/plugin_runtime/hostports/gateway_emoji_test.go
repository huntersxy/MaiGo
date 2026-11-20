package hostports

import (
	"testing"

	"MaiGo/internal/platform_io"
)

func TestGatewayPreservesInboundEmojiMedia(t *testing.T) {
	const gif = "R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs="
	gateway := Gateway{sender: &Sender{}}
	segments, err := gateway.segments(map[string]any{"raw_message": []map[string]any{
		{"type": "emoji", "data": map[string]any{"url": "https://example.com/sticker.gif"}},
		{"type": "emoji", "binary_data_base64": gif},
		{"type": "image", "data": "https://example.com/photo.png"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if segments[0].Kind != platformio.SegmentEmoji || segments[0].URL != "https://example.com/sticker.gif" ||
		segments[1].Kind != platformio.SegmentEmoji || segments[1].URL != "data:image/gif;base64,"+gif ||
		segments[2].Kind != platformio.SegmentImage {
		t.Fatalf("unexpected media segments: %+v", segments)
	}
}
