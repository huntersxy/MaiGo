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
	originalHash     string
	description      string
	emojiDescription string
	emojiPending     bool
}

func (l recognitionHashLookup) IsVLMTaskConfigured() bool { return true }

func (l recognitionHashLookup) LookupCachedImageDescription(hash string) string {
	if hash == l.originalHash {
		return l.description
	}
	return ""
}

func (l recognitionHashLookup) LookupCachedEmojiDescription(ref string) string {
	if ref == l.originalHash {
		return l.emojiDescription
	}
	return ""
}

func (l recognitionHashLookup) IsImageDescriptionPending(hash string) bool {
	return hash == l.originalHash && l.description == ""
}

func (l recognitionHashLookup) IsEmojiDescriptionPending(ref string) bool {
	return ref == l.originalHash && l.emojiPending
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

func TestInboundStickerReachesPlannerAndReplyer(t *testing.T) {
	const gif = "R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs="
	sticker, err := base64.StdEncoding.DecodeString(gif)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sticker.gif")
	if err := os.WriteFile(path, sticker, 0o600); err != nil {
		t.Fatal(err)
	}
	refs := []media.Ref{{Path: path, Mime: "image/gif", Sticker: true}}
	const messageText = "[表情包:653b6efe]再试试"
	for _, message := range []*schema.AgenticMessage{
		userSchemaMessage(messageText, refs),
		attachDeferredMediaBlocks(schema.UserAgenticMessage(messageText), refs),
	} {
		history, ok := agenticMessageToMaisaka(message, 1)
		if !ok || len(history) != 1 || len(history[0].Emojis) != 1 || len(history[0].Images) != 0 {
			t.Fatal("the sticker lost its media type")
		}
		lookup := recognitionHashLookup{originalHash: gif, emojiPending: true}
		refresher := visual.Refresher{Lookup: lookup}
		if !refresher.HasPendingImageRecognition(history) {
			t.Fatal("the text planner should wait for the pending sticker description")
		}
		lookup.emojiDescription = "惊讶的小猫"
		lookup.emojiPending = false
		refresher.Lookup = lookup
		if got := refresher.RefreshChatHistoryVisualPlaceholders(history); got != 1 || refresher.HasPendingImageRecognition(history) {
			t.Fatalf("sticker description did not replace its pending state, refreshed %d messages", got)
		}
		text := maisaka.BuildContextItemsFromHistoryEntry(history[0], false)[0]
		if !strings.Contains(text.Text, messageText) || !strings.Contains(text.Text, lookup.emojiDescription) {
			t.Fatalf("the text planner lost the sticker description or handle: %s", text.Text)
		}
		multimodal := maisaka.BuildContextItemsFromHistoryEntry(history[0], true)[0]
		if blocks := maisakaItemMediaBlocks(multimodal, false, 0); len(blocks) != 1 || blocks[0].UserInputImage == nil {
			t.Fatal("the visual planner did not receive an image block")
		}
		replies := replyerHistoryMessages(history)
		if len(replies) != 1 || len(replies[0].Parts) != 2 || !strings.Contains(replies[0].Parts[1].Description, lookup.emojiDescription) {
			t.Fatal("the replyer did not receive the sticker's description")
		}
	}
}
