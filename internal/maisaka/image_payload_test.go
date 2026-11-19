package maisaka

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestImageComponentPayloadResolvesDeferredMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	raw := []byte("\x89PNG\r\n\x1a\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	encoded, mime, ok := ImageComponentPayload(ImageComponent{
		MediaRef: DeferredMediaMarkerPrefix + path,
		Format:   "image/png",
	})
	if !ok {
		t.Fatal("deferred-media 应解析成功")
	}
	if mime != "image/png" {
		t.Fatalf("mime=%q", mime)
	}
	if encoded != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("encoded mismatch: %q", encoded)
	}
}
