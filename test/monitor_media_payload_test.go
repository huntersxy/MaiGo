package test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"MaiGo/internal/maisaka"
	monitormod "MaiGo/internal/maisaka/monitor"
	maisaruntime "MaiGo/internal/maisaka/runtime"
)

func TestBuildMonitorMessageMediaPopulatesURLAndFallbackText(t *testing.T) {
	items := monitormod.BuildMonitorMessageMedia([]map[string]any{
		{"kind": "image", "hash": "img123"},
		{"kind": "emoji", "hash": "emo456", "text": "猫猫点头"},
		{"kind": "image", "hash": "ext789", "url": "https://example.com/a.png"},
		{"kind": "emoji", "hash": ""},
	}, true, "test")

	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3: %#v", len(items), items)
	}
	if items[0]["url"] != "/api/webui/system/maisaka-monitor/media/image/img123" {
		t.Fatalf("items[0].url = %#v", items[0]["url"])
	}
	if items[0]["text"] != monitormod.PendingImagePlaceholder {
		t.Fatalf("items[0].text = %#v, want %q", items[0]["text"], monitormod.PendingImagePlaceholder)
	}
	if items[0]["default_original"] != true {
		t.Fatalf("items[0].default_original = %#v, want true", items[0]["default_original"])
	}
	if items[1]["url"] != "/api/webui/system/maisaka-monitor/media/emoji/emo456" {
		t.Fatalf("items[1].url = %#v", items[1]["url"])
	}
	if items[2]["url"] != "https://example.com/a.png" {
		t.Fatalf("items[2].url = %#v", items[2]["url"])
	}
}

func TestAgentMonitorEmitterPopulatesMediaURLForHashAndDeferredEmoji(t *testing.T) {
	pngBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO7Z0ioAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	emojiPath := filepath.Join(t.TempDir(), "sticker.png")
	if err := os.WriteFile(emojiPath, pngBytes, 0o600); err != nil {
		t.Fatalf("write sticker: %v", err)
	}
	sum := sha256.Sum256(pngBytes)
	wantHash := hex.EncodeToString(sum[:])

	ch, unsub := monitormod.Subscribe()
	defer unsub()

	emitter := maisaruntime.NewAgentMonitorEmitter("test", nil)
	emitter.DefaultOriginal = func() bool { return true }

	msg := maisaka.ContextMessage{
		Role:      maisaka.RoleUser,
		MessageID: "msg-1",
		UserID:    "1001",
		Platform:  "qq",
		Timestamp: time.Unix(1700000000, 0),
		Images: []maisaka.ImageComponent{
			{MediaRef: "hash-img-1", Description: "一张截图", Format: "png"},
		},
		Emojis: []maisaka.ImageComponent{
			{MediaRef: maisaka.DeferredMediaMarkerPrefix + emojiPath, Description: "[表情包: 猫猫]", HasBinary: true},
		},
	}
	emitter.MessageSent("session-1", "麦麦", maisaka.SourceGuidedReply, msg)

	var event monitormod.Event
	select {
	case event = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for monitor message.sent event")
	}
	if event.Event != monitormod.EventMessageSent {
		t.Fatalf("event = %q, want %q", event.Event, monitormod.EventMessageSent)
	}
	rawMedia, ok := event.Data["media"].([]any)
	if !ok {
		if typed, okTyped := event.Data["media"].([]map[string]any); okTyped {
			for _, item := range typed {
				rawMedia = append(rawMedia, item)
			}
		} else {
			t.Fatalf("media = %#v (%T), want slice", event.Data["media"], event.Data["media"])
		}
	}
	if len(rawMedia) != 2 {
		t.Fatalf("len(media) = %d, want 2: %#v", len(rawMedia), rawMedia)
	}
	first := rawMedia[0].(map[string]any)
	second := rawMedia[1].(map[string]any)
	if first["url"] != "/api/webui/system/maisaka-monitor/media/image/hash-img-1" {
		t.Fatalf("first.url = %#v", first["url"])
	}
	if first["default_original"] != true {
		t.Fatalf("first.default_original = %#v, want true", first["default_original"])
	}
	if second["url"] != "/api/webui/system/maisaka-monitor/media/emoji/"+wantHash {
		t.Fatalf("second.url = %#v, want emoji url with %s", second["url"], wantHash)
	}
	dataURL, _ := second["data_url"].(string)
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("second.data_url = %#v, want inline data url", second["data_url"])
	}
}

func TestAgentMonitorEmitterMessageIngestedExternalURLAndEmptyMedia(t *testing.T) {
	ch, unsub := monitormod.Subscribe()
	defer unsub()

	emitter := maisaruntime.NewAgentMonitorEmitter("test", nil)
	emitter.MessageIngested("session-url", maisaka.ContextMessage{
		Role:      maisaka.RoleUser,
		MessageID: "msg-url",
		UserID:    "1002",
		Platform:  "qq",
		Timestamp: time.Unix(1700000001, 0),
		Images: []maisaka.ImageComponent{
			{MediaRef: "https://example.com/pic.jpg", Description: "外链图片"},
		},
	})
	ev1 := <-ch
	rawMedia, _ := ev1.Data["media"].([]any)
	if len(rawMedia) == 0 {
		if typed, ok := ev1.Data["media"].([]map[string]any); ok {
			for _, item := range typed {
				rawMedia = append(rawMedia, item)
			}
		}
	}
	if len(rawMedia) != 1 || rawMedia[0].(map[string]any)["url"] != "https://example.com/pic.jpg" {
		t.Fatalf("external url media = %#v", ev1.Data["media"])
	}

	emitter.MessageIngested("session-empty", maisaka.ContextMessage{
		Role:      maisaka.RoleUser,
		MessageID: "msg-empty",
		UserID:    "1003",
		Platform:  "qq",
		Content:   "纯文本",
		Timestamp: time.Unix(1700000002, 0),
	})
	ev2 := <-ch
	if _, exists := ev2.Data["media"]; !exists {
		t.Fatalf("expected media key to be present on plain text event: %#v", ev2.Data)
	}
}
