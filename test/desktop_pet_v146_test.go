package test

import (
	"encoding/base64"
	"strings"
	"testing"

	desktoppet "MaiGo/internal/desktop_pet"
)

func TestDesktopPetSoundPreviews(t *testing.T) {
	for _, sound := range []string{"wood", "bubbles", "bell", "water"} {
		preview, err := desktoppet.SoundPreview(sound)
		if err != nil {
			t.Fatal(err)
		}
		encoded, ok := strings.CutPrefix(preview, "data:audio/wav;base64,")
		data, err := base64.StdEncoding.DecodeString(encoded)
		if !ok || err != nil || len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
			t.Fatalf("invalid WAV preview for %q", sound)
		}
	}
	if _, err := desktoppet.SoundPreview("off"); err == nil {
		t.Fatal("off must not resolve to a sound file")
	}
}

func TestDesktopPetHistoryCursorValidation(t *testing.T) {
	event := map[string]any{"type": "history", "request_id": "history-1", "cursors": map[string]any{"session": float64(25)}}
	if err := desktoppet.ValidateRemoteEvent(event); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []any{nil, []any{}, map[string]any{"session": true}, map[string]any{"session": -1}, map[string]any{"session": 1.25}} {
		event["cursors"] = invalid
		if err := desktoppet.ValidateRemoteEvent(event); err == nil {
			t.Fatalf("accepted invalid cursor %#v", invalid)
		}
	}
}
