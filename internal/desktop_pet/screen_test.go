package desktoppet

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func testScreenshot(t *testing.T, width, height int, fill color.RGBA, capturedAt string) DesktopScreenshot {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return newDesktopScreenshot(buffer.Bytes(), capturedAt, width, height)
}

func TestMatchingApplicationNormalizes(t *testing.T) {
	applications := []map[string]string{{"process": "Code", "title": "proposals.md - Visual Studio Code"}}
	if got := matchingApplication("ｃｏｄｅ．ｅｘｅ", applications); got == "" {
		t.Fatal("全角进程名应匹配")
	}
	if got := matchingApplication("visual studio", applications); got == "" {
		t.Fatal("窗口标题应匹配")
	}
	if got := matchingApplication("terminal", applications); got != "" {
		t.Fatal("不相关应用不应匹配")
	}
}

func TestScreenTimelineDropsMiddleFrames(t *testing.T) {
	timeline := &ScreenTimeline{}
	for index := 0; index < 8; index++ {
		fill := color.RGBA{R: uint8((index % 2) * 240), G: uint8((index % 2) * 240), B: uint8((index % 2) * 240), A: 255}
		shot := testScreenshot(t, 64, 36, fill, fmt.Sprintf("2026-01-01T00:00:%02d+00:00", index))
		timeline.Add(shot, 0.001)
	}
	if len(timeline.Frames) != screenTimelineMaxFrames {
		t.Fatalf("帧数 = %d", len(timeline.Frames))
	}
	timeline.Delivered()
	if len(timeline.Frames) != 1 || timeline.Changed {
		t.Fatalf("投递后应只保留末帧: %#v", timeline)
	}
}

func TestDecodeRemoteScreenshotRejectsMissingTimezone(t *testing.T) {
	shot := testScreenshot(t, 32, 18, color.RGBA{G: 200, A: 255}, "2026-01-01T00:00:00+00:00")
	payload := map[string]any{
		"base64":      base64.StdEncoding.EncodeToString(shot.Data),
		"captured_at": "2026-01-01T00:00:00",
	}
	if _, err := DecodeRemoteScreenshot(payload); err == nil {
		t.Fatal("缺少时区应拒绝")
	}
	payload["captured_at"] = "2026-01-01T00:00:00Z"
	if _, err := DecodeRemoteScreenshot(payload); err != nil {
		t.Fatalf("合法截图被拒绝: %v", err)
	}
}

func TestComposeScreenTimeline(t *testing.T) {
	frames := []DesktopScreenshot{
		testScreenshot(t, 64, 36, color.RGBA{R: 200, A: 255}, "2026-01-01T00:00:00Z"),
		testScreenshot(t, 64, 36, color.RGBA{B: 200, A: 255}, "2026-01-01T00:00:01Z"),
	}
	composed, err := ComposeScreenTimeline(frames)
	if err != nil {
		t.Fatal(err)
	}
	if composed.Width != 2*screenTileWidth || composed.Height != screenTileHeight+screenTileLabelHeight {
		t.Fatalf("拼图尺寸 = %dx%d", composed.Width, composed.Height)
	}
}
