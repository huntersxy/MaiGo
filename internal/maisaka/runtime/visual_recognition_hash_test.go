package runtime

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"MaiGo/internal/common/media"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/visual"

	"github.com/cloudwego/eino/schema"
)

type recognitionHashLookup struct {
	originalHash string
	description  string
}

func (l recognitionHashLookup) IsVLMTaskConfigured() bool { return true }

func (l recognitionHashLookup) LookupCachedImageDescription(hash string) string {
	if hash == l.originalHash {
		return l.description
	}
	return ""
}

func (l recognitionHashLookup) LookupCachedEmojiDescription(string) string { return "" }

func (l recognitionHashLookup) IsImageDescriptionPending(hash string) bool {
	return hash == l.originalHash && l.description == ""
}

func TestTranscodedImageDescriptionReachesTextPlanner(t *testing.T) {
	transcoded := []byte("transcoded image bytes")
	path := filepath.Join(t.TempDir(), "picture.webp")
	if err := os.WriteFile(path, transcoded, 0o600); err != nil {
		t.Fatal(err)
	}
	originalHash := fmt.Sprintf("%x", sha256.Sum256([]byte("original image bytes")))
	refs := []media.Ref{{Path: path, Mime: "image/webp", RecognitionHash: originalHash}}
	const messageText = "用户消息 [图片:abc123]"
	messages := []*schema.AgenticMessage{
		userSchemaMessage(messageText, refs),
		attachDeferredMediaBlocks(schema.UserAgenticMessage(messageText), refs),
	}
	for _, message := range messages {
		history, ok := agenticMessageToMaisaka(message, 1)
		if !ok || len(history) != 1 || len(history[0].Images) != 1 {
			t.Fatalf("missing image: %+v", history)
		}
		lookup := recognitionHashLookup{originalHash: originalHash}
		refresher := visual.Refresher{Lookup: lookup}
		if !refresher.HasPendingImageRecognition(history) {
			t.Fatal("planner should wait on the original image recognition")
		}
		lookup.description = "一张蓝色海面的照片"
		refresher.Lookup = lookup
		if got := refresher.RefreshChatHistoryVisualPlaceholders(history); got != 1 {
			t.Fatalf("refreshed %d history messages, want 1", got)
		}
		text := maisaka.BuildContextItemsFromHistoryEntry(history[0], false)[0]
		if !strings.Contains(text.Text, lookup.description) || len(text.Images) != 0 {
			t.Fatalf("text planner input is missing the description: %+v", text)
		}
		multimodal := maisaka.BuildContextItemsFromHistoryEntry(history[0], true)[0]
		if len(multimodal.Images) != 1 || multimodal.Images[0].MediaRef != base64.StdEncoding.EncodeToString(transcoded) {
			t.Fatalf("visual planner input lost the cached image: %+v", multimodal)
		}
	}
}
